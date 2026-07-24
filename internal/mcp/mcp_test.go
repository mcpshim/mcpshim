package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
)

func TestParseSchema(t *testing.T) {
	schema := map[string]interface{}{
		"required": []interface{}{"query"},
		"properties": map[string]interface{}{
			"query":  map[string]interface{}{"type": "string"},
			"limit":  map[string]interface{}{"type": "integer"},
			"filter": map[string]interface{}{"type": "string"},
		},
	}

	required, props := parseSchema(schema)

	if len(required) != 1 || required[0] != "query" {
		t.Errorf("expected required=[query], got %v", required)
	}
	if len(props) != 3 {
		t.Errorf("expected 3 properties, got %d", len(props))
	}
	// properties should be sorted
	if props[0] != "filter" || props[1] != "limit" || props[2] != "query" {
		t.Errorf("expected sorted properties [filter limit query], got %v", props)
	}
}

func TestToolCallErrorPreservesResult(t *testing.T) {
	result := &mcpproto.CallToolResult{
		Content: []mcpproto.Content{mcpproto.NewTextContent("permission denied")},
		IsError: true,
	}

	err := newToolCallError(result)
	if err == nil {
		t.Fatal("newToolCallError() = nil")
	}
	if err.Error() != "permission denied" {
		t.Fatalf("error = %q, want permission denied", err)
	}
	if err.Result != result {
		t.Fatal("tool call result was not preserved")
	}
}

func TestParseSchemaDetail(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Max results",
			},
		},
	}
	required := []string{"query"}

	details := parseSchemaDetail(schema, required)

	if len(details) != 2 {
		t.Fatalf("expected 2 property details, got %d", len(details))
	}

	// details are sorted alphabetically: limit then query
	limitDetail := details[0]
	if limitDetail.Name != "limit" {
		t.Errorf("expected first detail name=limit, got %s", limitDetail.Name)
	}
	if limitDetail.Type != "integer" {
		t.Errorf("expected limit type=integer, got %s", limitDetail.Type)
	}
	if limitDetail.Required {
		t.Error("expected limit to not be required")
	}
	if limitDetail.Description != "Max results" {
		t.Errorf("expected limit description='Max results', got %s", limitDetail.Description)
	}

	queryDetail := details[1]
	if queryDetail.Name != "query" {
		t.Errorf("expected second detail name=query, got %s", queryDetail.Name)
	}
	if queryDetail.Type != "string" {
		t.Errorf("expected query type=string, got %s", queryDetail.Type)
	}
	if !queryDetail.Required {
		t.Error("expected query to be required")
	}
	if queryDetail.Description != "Search query" {
		t.Errorf("expected query description='Search query', got %s", queryDetail.Description)
	}
}

func TestParseSchemaDetailEmpty(t *testing.T) {
	details := parseSchemaDetail(nil, nil)
	if len(details) != 0 {
		t.Errorf("expected empty result for nil schema, got %v", details)
	}
}

func TestParseSchemaEmpty(t *testing.T) {
	required, props := parseSchema(map[string]interface{}{})
	if len(required) != 0 {
		t.Errorf("expected no required fields, got %v", required)
	}
	if len(props) != 0 {
		t.Errorf("expected no properties, got %v", props)
	}
}

func TestToolDetailProtocol(t *testing.T) {
	d := &protocol.ToolDetail{
		Server:      "myserver",
		Name:        "search",
		Description: "Search items",
		Properties: []protocol.PropertyDetail{
			{Name: "query", Type: "string", Description: "Search query", Required: true},
			{Name: "limit", Type: "integer", Description: "Max results", Required: false},
		},
	}

	if d.Server != "myserver" {
		t.Errorf("unexpected server: %s", d.Server)
	}
	if len(d.Properties) != 2 {
		t.Errorf("expected 2 properties, got %d", len(d.Properties))
	}
	if !d.Properties[0].Required {
		t.Error("expected first property to be required")
	}
}

func TestRegistryMetadataAndConfigUpdates(t *testing.T) {
	cfg := &config.Config{Servers: []config.MCPServer{
		{Name: "one", Alias: "first", URL: "https://one.example.com/mcp", Transport: "http"},
		{Name: "two", Alias: "second", URL: "https://two.example.com/sse", Transport: "sse", Headers: map[string]string{"Authorization": "Bearer token"}},
	}}
	registry := NewRegistry(cfg, nil)
	registry.toolCache["one"] = []protocol.ToolInfo{{Name: "search"}, {Name: "create"}}

	servers := registry.Servers()
	if len(servers) != 2 || servers[0].Alias != "first" || !servers[1].HasAuth {
		t.Fatalf("servers = %#v", servers)
	}
	if registry.ToolCount() != 2 {
		t.Fatalf("tool count = %d, want 2", registry.ToolCount())
	}

	next := &config.Config{Servers: []config.MCPServer{{Name: "three", Alias: "third", URL: "https://three.example.com/mcp"}}}
	registry.UpdateConfig(next)
	if registry.ToolCount() != 0 {
		t.Fatalf("tool cache was not cleared: %d", registry.ToolCount())
	}
	if _, ok := findServer(next, "third"); !ok {
		t.Fatal("findServer() did not match alias")
	}
	if _, ok := findServer(next, "missing"); ok {
		t.Fatal("findServer() matched missing server")
	}
}

func TestRegistryExposesHTTPServicesAsTools(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"item-1"}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		HTTPServices: []config.HTTPService{{
			Name:    "inventory",
			Alias:   "items",
			BaseURL: upstream.URL,
			RawTool: &config.HTTPRawTool{
				Name:    "request",
				Methods: []string{"GET"},
				Paths:   []string{"/v1/**"},
			},
			Tools: []config.HTTPTool{{
				Name:        "get_item",
				Description: "Get an item",
				Request: config.HTTPRequest{
					Method: "GET",
					Path:   "/v1/items/{item_id}",
				},
				Inputs: map[string]config.HTTPInput{
					"item_id": {Type: "string", Required: true},
				},
			}},
		}},
	}
	registry := NewRegistry(cfg, nil)

	servers := registry.Servers()
	if len(servers) != 1 || servers[0].Kind != "http" || servers[0].Alias != "items" {
		t.Fatalf("servers = %#v", servers)
	}
	tools, err := registry.ListTools(t.Context(), "items")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "get_item" || tools[1].Name != "request" {
		t.Fatalf("tools = %#v", tools)
	}
	detail, err := registry.InspectTool(t.Context(), "inventory", "get_item")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Properties) != 1 || !detail.Properties[0].Required {
		t.Fatalf("detail = %#v", detail)
	}
	result, err := registry.Call(t.Context(), "items", "get_item", map[string]interface{}{"item_id": "item-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("http result is nil")
	}
}
