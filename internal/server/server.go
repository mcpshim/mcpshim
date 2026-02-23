package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/mcp"
	"github.com/mcpshim/mcpshim/internal/protocol"
	"github.com/mcpshim/mcpshim/internal/store"
)

type Server struct {
	configPath string
	cfg        *config.Config
	registry   *mcp.Registry
	store      *store.Store
	startedAt  time.Time
	debug      bool
}

func New(configPath string, cfg *config.Config) *Server {
	return &Server{
		configPath: configPath,
		cfg:        cfg,
		registry:   mcp.NewRegistry(cfg, nil),
		startedAt:  time.Now().UTC(),
	}
}

func (s *Server) SetDebug(debug bool) {
	s.debug = debug
}

func (s *Server) Run() error {
	if s.store == nil {
		dbStore, err := store.Open(s.cfg.Server.DBPath)
		if err != nil {
			return err
		}
		s.store = dbStore
		s.registry = mcp.NewRegistry(s.cfg, s.store)
	}

	defer func() {
		if s.store != nil {
			_ = s.store.Close()
		}
	}()

	if err := os.MkdirAll(filepath.Dir(s.cfg.Server.SocketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(s.cfg.Server.SocketPath)
	ln, err := net.Listen("unix", s.cfg.Server.SocketPath)
	if err != nil {
		return err
	}
	defer ln.Close()
	if err := os.Chmod(s.cfg.Server.SocketPath, 0o600); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	_ = s.registry.Refresh(context.Background())
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.registry.Refresh(context.Background())
			}
		}
	}()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			if s.debug {
				log.Printf("accept error: %v", err)
			}
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(w)

	var req protocol.Request
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(protocol.Response{OK: false, Error: err.Error()})
		_ = w.Flush()
		return
	}
	resp := s.handle(req)
	_ = enc.Encode(resp)
	_ = w.Flush()
}

func (s *Server) handle(req protocol.Request) protocol.Response {
	switch req.Action {
	case "status":
		return protocol.Response{OK: true, Status: &protocol.Status{
			StartedAt:   s.startedAt,
			UptimeSec:   int64(time.Since(s.startedAt).Seconds()),
			ServerCount: len(s.cfg.Servers),
			ToolCount:   s.registry.ToolCount(),
		}}
	case "servers":
		return protocol.Response{OK: true, Servers: s.registry.Servers()}
	case "tools":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		items, err := s.registry.ListTools(ctx, req.Server)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, Tools: items}
	case "history":
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		items, err := s.store.ListHistory(req.Server, req.Tool, limit)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, History: items}
	case "inspect":
		if req.Server == "" || req.Tool == "" {
			return protocol.Response{OK: false, Error: "server and tool are required"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		detail, err := s.registry.InspectTool(ctx, req.Server, req.Tool)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, ToolDetail: detail}
	case "call":
		if req.Server == "" || req.Tool == "" {
			return protocol.Response{OK: false, Error: "server and tool are required"}
		}
		started := time.Now().UTC()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		result, err := s.registry.Call(ctx, req.Server, req.Tool, req.Args)
		historyItem := protocol.HistoryItem{
			At:         started,
			Server:     req.Server,
			Tool:       req.Tool,
			Args:       req.Args,
			Success:    err == nil,
			DurationMs: int64(time.Since(started) / time.Millisecond),
		}
		if err != nil {
			historyItem.Error = err.Error()
		}
		_ = s.store.InsertHistory(historyItem)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, Result: result}
	case "add_server":
		if req.Name == "" || req.URL == "" {
			return protocol.Response{OK: false, Error: "name and url are required"}
		}
		item := config.MCPServer{
			Name:      req.Name,
			Alias:     req.Alias,
			URL:       req.URL,
			Transport: req.Transport,
			Headers:   req.Headers,
		}
		config.UpsertServer(s.cfg, item)
		if err := config.Save(s.configPath, s.cfg); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		s.registry.UpdateConfig(s.cfg)
		_ = s.registry.Refresh(context.Background())
		return protocol.Response{OK: true, Text: fmt.Sprintf("added server %s", req.Name)}
	case "remove_server":
		if req.Name == "" {
			return protocol.Response{OK: false, Error: "name is required"}
		}
		if !config.RemoveServer(s.cfg, req.Name) {
			return protocol.Response{OK: false, Error: "server not found"}
		}
		if err := config.Save(s.configPath, s.cfg); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		s.registry.UpdateConfig(s.cfg)
		_ = s.registry.Refresh(context.Background())
		return protocol.Response{OK: true, Text: fmt.Sprintf("removed server %s", req.Name)}
	case "set_auth":
		if req.Name == "" {
			return protocol.Response{OK: false, Error: "name is required"}
		}
		updated := false
		for i := range s.cfg.Servers {
			if s.cfg.Servers[i].Name == req.Name {
				if s.cfg.Servers[i].Headers == nil {
					s.cfg.Servers[i].Headers = map[string]string{}
				}
				for k, v := range req.Headers {
					s.cfg.Servers[i].Headers[k] = v
				}
				updated = true
				break
			}
		}
		if !updated {
			return protocol.Response{OK: false, Error: "server not found"}
		}
		if err := config.Save(s.configPath, s.cfg); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		s.registry.UpdateConfig(s.cfg)
		return protocol.Response{OK: true, Text: "updated authentication"}
	case "reload":
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		if strings.TrimSpace(cfg.Server.DBPath) != strings.TrimSpace(s.cfg.Server.DBPath) {
			nextStore, openErr := store.Open(cfg.Server.DBPath)
			if openErr != nil {
				return protocol.Response{OK: false, Error: openErr.Error()}
			}
			if s.store != nil {
				_ = s.store.Close()
			}
			s.store = nextStore
			s.registry = mcp.NewRegistry(cfg, nextStore)
		}
		s.cfg = cfg
		s.registry.UpdateConfig(cfg)
		_ = s.registry.Refresh(context.Background())
		return protocol.Response{OK: true, Text: "reloaded config"}
	case "login":
		if req.Server == "" {
			return protocol.Response{OK: false, Error: "server is required"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		if err := s.registry.Login(ctx, req.Server, false); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, Text: fmt.Sprintf("oauth login completed for %s", req.Server)}
	default:
		return protocol.Response{OK: false, Error: "unknown action"}
	}
}
