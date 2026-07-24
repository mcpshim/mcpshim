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
	"sync"
	"syscall"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/mcp"
	"github.com/mcpshim/mcpshim/internal/protocol"
	"github.com/mcpshim/mcpshim/internal/store"
)

type Server struct {
	mu         sync.RWMutex
	configPath string
	cfg        *config.Config
	registry   *mcp.Registry
	store      *store.Store
	startedAt  time.Time
	debug      bool
}

func New(configPath string, cfg *config.Config) *Server {
	cfg = config.Clone(cfg)
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

	if err := os.MkdirAll(filepath.Dir(s.cfg.Server.SocketPath), 0o700); err != nil {
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

	refreshCtx, refreshCancel := context.WithTimeout(ctx, 20*time.Second)
	_ = s.registry.Refresh(refreshCtx)
	refreshCancel()
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.RLock()
				refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				_ = s.registry.Refresh(refreshCtx)
				cancel()
				s.mu.RUnlock()
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
	case "add_server", "remove_server", "set_auth", "reload":
		s.mu.Lock()
		defer s.mu.Unlock()
	default:
		s.mu.RLock()
		defer s.mu.RUnlock()
	}

	// Registry mutations persist to the config file, so socket access would
	// otherwise imply config write access. Reload is not gated: it only
	// re-reads whatever is already on disk.
	switch req.Action {
	case "add_server", "remove_server", "set_auth":
		if !s.cfg.Server.AllowRegistryWrites {
			return protocol.Response{
				OK: false,
				Error: "registry writes are disabled; edit the config file and run 'mcpshim reload', " +
					"or set server.allow_registry_writes: true to permit socket clients to change the registry",
			}
		}
	}

	switch req.Action {
	case "status":
		return protocol.Response{OK: true, Status: &protocol.Status{
			StartedAt:   s.startedAt,
			UptimeSec:   int64(time.Since(s.startedAt).Seconds()),
			ServerCount: len(s.cfg.Servers) + len(s.cfg.HTTPServices),
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
		items, err := s.store.ListHistory(s.canonicalServerName(req.Server), req.Tool, limit)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, History: items}
	case "clear_history":
		cleared, err := s.store.ClearHistory(s.canonicalServerName(req.Server), req.Tool, req.All)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{
			OK:      true,
			Cleared: cleared,
			Text:    fmt.Sprintf("cleared %d history entries", cleared),
		}
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
			Server:     s.canonicalServerName(req.Server),
			Tool:       req.Tool,
			Args:       req.Args,
			Success:    err == nil,
			DurationMs: int64(time.Since(started) / time.Millisecond),
		}
		if err != nil {
			historyItem.Error = err.Error()
		}
		_ = s.store.InsertHistory(historyItem, s.cfg.Server.HistorySize)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error(), Result: result}
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
		var previous *config.MCPServer
		for _, server := range s.cfg.Servers {
			if server.Name == req.Name {
				copy := server
				previous = &copy
				break
			}
		}
		candidate := config.Clone(s.cfg)
		if err := config.UpsertServer(candidate, item); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		if err := config.Save(s.configPath, candidate); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		if previous != nil && config.ResolveServer(*previous).URL != config.ResolveServer(item).URL && s.store != nil {
			_ = s.store.DeleteTokens(previous.Name, mcp.TokenStoreKey(*previous))
		}
		s.cfg = candidate
		s.registry.UpdateConfig(candidate)
		refreshCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = s.registry.Refresh(refreshCtx)
		cancel()
		return protocol.Response{OK: true, Text: fmt.Sprintf("added server %s", req.Name)}
	case "remove_server":
		if req.Name == "" {
			return protocol.Response{OK: false, Error: "name is required"}
		}
		candidate := config.Clone(s.cfg)
		var removed config.MCPServer
		for _, server := range candidate.Servers {
			if server.Name == req.Name {
				removed = server
				break
			}
		}
		if !config.RemoveServer(candidate, req.Name) {
			return protocol.Response{OK: false, Error: "server not found"}
		}
		if err := config.Save(s.configPath, candidate); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		if s.store != nil {
			_ = s.store.DeleteTokens(removed.Name, mcp.TokenStoreKey(removed))
		}
		s.cfg = candidate
		s.registry.UpdateConfig(candidate)
		refreshCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = s.registry.Refresh(refreshCtx)
		cancel()
		return protocol.Response{OK: true, Text: fmt.Sprintf("removed server %s", req.Name)}
	case "set_auth":
		if req.Name == "" {
			return protocol.Response{OK: false, Error: "name is required"}
		}
		updated := false
		candidate := config.Clone(s.cfg)
		for i := range candidate.Servers {
			if candidate.Servers[i].Name == req.Name {
				if candidate.Servers[i].Headers == nil {
					candidate.Servers[i].Headers = map[string]string{}
				}
				for k, v := range req.Headers {
					candidate.Servers[i].Headers[k] = v
				}
				updated = true
				break
			}
		}
		if !updated {
			return protocol.Response{OK: false, Error: "server not found"}
		}
		if err := config.Save(s.configPath, candidate); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		s.cfg = candidate
		s.registry.UpdateConfig(candidate)
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
			previousStore := s.store
			s.store = nextStore
			s.registry = mcp.NewRegistry(cfg, nextStore)
			if previousStore != nil {
				_ = previousStore.Close()
			}
		}
		s.cfg = cfg
		s.registry.UpdateConfig(cfg)
		refreshCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = s.registry.Refresh(refreshCtx)
		cancel()
		return protocol.Response{OK: true, Text: "reloaded config"}
	default:
		return protocol.Response{OK: false, Error: "unknown action"}
	}
}

func (s *Server) canonicalServerName(name string) string {
	for _, server := range s.cfg.Servers {
		if server.Name == name || server.Alias == name {
			return server.Name
		}
	}
	for _, service := range s.cfg.HTTPServices {
		if service.Name == name || service.Alias == name {
			return service.Name
		}
	}
	return name
}
