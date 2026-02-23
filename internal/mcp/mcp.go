package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
	"github.com/mcpshim/mcpshim/internal/store"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

type Registry struct {
	mu         sync.RWMutex
	cfg        *config.Config
	store      *store.Store
	toolCache  map[string][]protocol.ToolInfo
	cacheStamp time.Time
}

func NewRegistry(cfg *config.Config, dbStore *store.Store) *Registry {
	return &Registry{cfg: cfg, store: dbStore, toolCache: map[string][]protocol.ToolInfo{}}
}

func (r *Registry) UpdateConfig(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
	r.toolCache = map[string][]protocol.ToolInfo{}
	r.cacheStamp = time.Time{}
}

func (r *Registry) Servers() []protocol.ServerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]protocol.ServerInfo, 0, len(r.cfg.Servers))
	for _, s := range r.cfg.Servers {
		out = append(out, protocol.ServerInfo{
			Name:      s.Name,
			Alias:     s.Alias,
			URL:       s.URL,
			Transport: s.Transport,
			HasAuth:   hasAuthorizationHeader(s.Headers),
		})
	}
	return out
}

func (r *Registry) ListTools(ctx context.Context, server string) ([]protocol.ToolInfo, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	if server != "" {
		s, ok := findServer(cfg, server)
		if !ok {
			return nil, fmt.Errorf("unknown server %q", server)
		}
		return fetchToolsForServer(ctx, s, r.store, true)
	}

	all := []protocol.ToolInfo{}
	for _, s := range cfg.Servers {
		items, err := fetchToolsForServer(ctx, s, r.store, true)
		if err != nil {
			continue
		}
		all = append(all, items...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Server == all[j].Server {
			return all[i].Name < all[j].Name
		}
		return all[i].Server < all[j].Server
	})
	return all, nil
}

func (r *Registry) Refresh(ctx context.Context) error {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	cache := map[string][]protocol.ToolInfo{}
	for _, s := range cfg.Servers {
		tools, err := fetchToolsForServer(ctx, s, r.store, false)
		if err != nil {
			continue
		}
		cache[s.Name] = tools
	}

	r.mu.Lock()
	r.toolCache = cache
	r.cacheStamp = time.Now().UTC()
	r.mu.Unlock()
	return nil
}

func (r *Registry) ToolCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := 0
	for _, items := range r.toolCache {
		total += len(items)
	}
	return total
}

func (r *Registry) InspectTool(ctx context.Context, server, tool string) (*protocol.ToolDetail, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	s, ok := findServer(cfg, server)
	if !ok {
		return nil, fmt.Errorf("unknown server %q", server)
	}

	tools, err := fetchToolsRaw(ctx, s, r.store, true)
	if err != nil {
		return nil, err
	}
	for _, t := range tools {
		if t.Name == tool {
			required, _ := parseSchema(t.InputSchema)
			return &protocol.ToolDetail{
				Server:      s.Name,
				Name:        t.Name,
				Description: t.Description,
				Properties:  parseSchemaDetail(t.InputSchema, required),
			}, nil
		}
	}
	return nil, fmt.Errorf("tool %q not found on server %q", tool, server)
}

func (r *Registry) Call(ctx context.Context, server string, tool string, args map[string]interface{}) (interface{}, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	s, ok := findServer(cfg, server)
	if !ok {
		return nil, fmt.Errorf("unknown server %q", server)
	}
	if args == nil {
		args = map[string]interface{}{}
	}

	res, err := runWithOAuthFallback(ctx, s, r.store, true, func(cli compatibleClient) (interface{}, error) {
		req := mcpproto.CallToolRequest{}
		req.Params.Name = tool
		req.Params.Arguments = args

		result, err := cli.CallTool(ctx, req)
		if err != nil {
			return nil, err
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (r *Registry) Login(ctx context.Context, server string, manual bool) error {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	s, ok := findServer(cfg, server)
	if !ok {
		return fmt.Errorf("unknown server %q", server)
	}

	return runOAuthLogin(ctx, s, r.store, manual)
}

func fetchToolsForServer(ctx context.Context, s config.MCPServer, dbStore *store.Store, interactive bool) ([]protocol.ToolInfo, error) {
	raw, err := fetchToolsRaw(ctx, s, dbStore, interactive)
	if err != nil {
		return nil, err
	}
	items := make([]protocol.ToolInfo, 0, len(raw))
	for _, t := range raw {
		required, properties := parseSchema(t.InputSchema)
		items = append(items, protocol.ToolInfo{
			Server:      s.Name,
			Name:        t.Name,
			Description: t.Description,
			Required:    required,
			Properties:  properties,
		})
	}
	return items, nil
}

func fetchToolsRaw(ctx context.Context, s config.MCPServer, dbStore *store.Store, interactive bool) ([]mcpproto.Tool, error) {
	return runWithOAuthFallback(ctx, s, dbStore, interactive, func(cli compatibleClient) ([]mcpproto.Tool, error) {
		list, err := cli.ListTools(ctx, mcpproto.ListToolsRequest{})
		if err != nil {
			return nil, err
		}
		return list.Tools, nil
	})
}

func parseSchema(schema interface{}) ([]string, []string) {
	type inputSchema struct {
		Required   []string               `json:"required"`
		Properties map[string]interface{} `json:"properties"`
	}
	var parsed inputSchema
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, nil
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil, nil
	}
	props := make([]string, 0, len(parsed.Properties))
	for key := range parsed.Properties {
		props = append(props, key)
	}
	sort.Strings(props)
	return parsed.Required, props
}

func parseSchemaDetail(schema interface{}, requiredList []string) []protocol.PropertyDetail {
	type propEntry struct {
		Type        string `json:"type"`
		Enum        []interface{} `json:"enum"`
		Const       interface{} `json:"const"`
		Description string `json:"description"`
	}
	type inputSchema struct {
		Properties map[string]propEntry `json:"properties"`
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var parsed inputSchema
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil
	}

	required := map[string]bool{}
	for _, r := range requiredList {
		required[r] = true
	}

	keys := make([]string, 0, len(parsed.Properties))
	for k := range parsed.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]protocol.PropertyDetail, 0, len(keys))
	for _, k := range keys {
		p := parsed.Properties[k]
		enum := []string{}
		for _, v := range p.Enum {
			enum = append(enum, fmt.Sprintf("%v", v))
		}
		constValue := ""
		if p.Const != nil {
			constValue = fmt.Sprintf("%v", p.Const)
		}
		out = append(out, protocol.PropertyDetail{
			Name:        k,
			Type:        p.Type,
			Enum:        enum,
			Const:       constValue,
			Description: p.Description,
			Required:    required[k],
		})
	}
	return out
}

type compatibleClient interface {
	Start(ctx context.Context) error
	Initialize(ctx context.Context, request mcpproto.InitializeRequest) (*mcpproto.InitializeResult, error)
	ListTools(ctx context.Context, req mcpproto.ListToolsRequest) (*mcpproto.ListToolsResult, error)
	CallTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error)
	Close() error
}

func newClient(s config.MCPServer) (compatibleClient, func(), error) {
	var cli compatibleClient
	if s.Transport == "sse" {
		headers := map[string]string{}
		for k, v := range s.Headers {
			headers[k] = v
		}
		opts := []transport.ClientOption{}
		if len(headers) > 0 {
			opts = append(opts, transport.WithHeaders(headers))
		}
		c, err := mcpclient.NewSSEMCPClient(s.URL, opts...)
		if err != nil {
			return nil, nil, err
		}
		cli = c
	} else {
		opts := []transport.StreamableHTTPCOption{}
		headers := map[string]string{}
		for k, v := range s.Headers {
			headers[k] = v
		}
		if len(headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(headers))
		}
		c, err := mcpclient.NewStreamableHttpClient(s.URL, opts...)
		if err != nil {
			return nil, nil, err
		}
		cli = c
	}
	return cli, func() { _ = cli.Close() }, nil
}

func findServer(cfg *config.Config, nameOrAlias string) (config.MCPServer, bool) {
	for _, s := range cfg.Servers {
		if s.Name == nameOrAlias || s.Alias == nameOrAlias {
			return s, true
		}
	}
	return config.MCPServer{}, false
}
