package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadPreservesEnvironmentReferences(t *testing.T) {
	t.Setenv("MCPSHIM_TEST_URL", "https://resolved.example.com/mcp")
	t.Setenv("MCPSHIM_TEST_TOKEN", "secret-token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server: {}
servers:
  - name: example
    url: ${MCPSHIM_TEST_URL}
    headers:
      Authorization: Bearer ${MCPSHIM_TEST_TOKEN}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	server := cfg.Servers[0]
	if server.URL != "${MCPSHIM_TEST_URL}" {
		t.Fatalf("URL = %q, want environment reference", server.URL)
	}
	if server.Headers["Authorization"] != "Bearer ${MCPSHIM_TEST_TOKEN}" {
		t.Fatalf("Authorization header was materialized in memory: %q", server.Headers["Authorization"])
	}

	resolved := ResolveServer(server)
	if resolved.URL != "https://resolved.example.com/mcp" {
		t.Fatalf("resolved URL = %q", resolved.URL)
	}
	if resolved.Headers["Authorization"] != "Bearer secret-token" {
		t.Fatalf("resolved Authorization = %q", resolved.Headers["Authorization"])
	}
}

func TestSaveDoesNotMaterializeEnvironmentSecrets(t *testing.T) {
	t.Setenv("MCPSHIM_TEST_TOKEN", "secret-token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &Config{
		Servers: []MCPServer{{
			Name: "example",
			URL:  "https://example.com/mcp",
			Headers: map[string]string{
				"Authorization": "Bearer ${MCPSHIM_TEST_TOKEN}",
			},
		}},
	}

	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-token") {
		t.Fatalf("saved config contains expanded secret:\n%s", data)
	}
	if !strings.Contains(string(data), "${MCPSHIM_TEST_TOKEN}") {
		t.Fatalf("saved config lost environment reference:\n%s", data)
	}
}

func TestLoadRejectsUnknownFieldsAndInvalidServers(t *testing.T) {
	tests := map[string]string{
		"unknown field": `
server:
  mystery: true
servers: []
`,
		"duplicate name": `
server: {}
servers:
  - name: duplicate
    url: https://one.example.com/mcp
  - name: duplicate
    url: https://two.example.com/mcp
`,
		"duplicate alias": `
server: {}
servers:
  - name: one
    alias: shared
    url: https://one.example.com/mcp
  - name: two
    alias: shared
    url: https://two.example.com/mcp
`,
		"unsupported transport": `
server: {}
servers:
  - name: example
    transport: websocket
    url: https://example.com/mcp
`,
		"unset environment variable": `
server: {}
servers:
  - name: example
    url: https://example.com/mcp
    headers:
      Authorization: Bearer ${MCPSHIM_DEFINITELY_UNSET}
`,
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load() succeeded, want error")
			}
		})
	}
}

func TestLoadAppliesDefaultsAndNormalizesTransport(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server: {}
servers:
  - name: example
    transport: streamable-http
    url: https://example.com/mcp
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.SocketPath != DefaultSocketPath() {
		t.Fatalf("socket path = %q, want %q", cfg.Server.SocketPath, DefaultSocketPath())
	}
	if cfg.Server.DBPath != DefaultDBPath() {
		t.Fatalf("db path = %q, want %q", cfg.Server.DBPath, DefaultDBPath())
	}
	if cfg.Server.HistorySize != DefaultHistorySize {
		t.Fatalf("history size = %d, want %d", cfg.Server.HistorySize, DefaultHistorySize)
	}
	if cfg.Servers[0].Transport != "http" {
		t.Fatalf("transport = %q, want http", cfg.Servers[0].Transport)
	}
	if cfg.Servers[0].Alias != "example" {
		t.Fatalf("alias = %q, want example", cfg.Servers[0].Alias)
	}
}

func TestLoadRejectsNegativeHistorySize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server:
  history_size: -1
servers: []
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted a negative history size")
	}
}

func TestLoadHTTPServiceWithRawAndNestedTypedTools(t *testing.T) {
	t.Setenv("DEPLOY_API_TOKEN", "secret")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server: {}
servers: []
http_services:
  - name: deployment-api
    alias: deploy
    base_url: https://deployments.example.com
    headers:
      Authorization: Bearer ${DEPLOY_API_TOKEN}
    policy:
      timeout_seconds: 15
      redirects: same-origin
      max_response_bytes: 2048
    raw_tool:
      name: request
      methods: [get, post]
      paths:
        - /v1/deployments/**
      request_headers:
        - If-Match
    tools:
      - name: create
        description: Create a deployment
        request:
          method: post
          path: /v1/teams/{team}/deployments
          query:
            dry_run:
              $arg: dry_run
              $default: false
          body:
            content_type: application/json
            template:
              application:
                name:
                  $arg: service
                image:
                  tag:
                    $arg: version
              labels:
                $arg: labels
                $default: {}
        inputs:
          team:
            type: string
            required: true
          service:
            type: string
            required: true
          version:
            type: string
            required: true
          dry_run:
            type: boolean
          labels:
            type: object
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HTTPServices) != 1 {
		t.Fatalf("http services = %#v", cfg.HTTPServices)
	}
	service := cfg.HTTPServices[0]
	if service.BaseURL != "https://deployments.example.com" {
		t.Fatalf("base URL was unexpectedly resolved or changed: %q", service.BaseURL)
	}
	if service.Headers["Authorization"] != "Bearer ${DEPLOY_API_TOKEN}" {
		t.Fatalf("header reference was materialized: %q", service.Headers["Authorization"])
	}
	if service.RawTool == nil || !reflect.DeepEqual(service.RawTool.Methods, []string{"GET", "POST"}) {
		t.Fatalf("raw tool = %#v", service.RawTool)
	}
	if service.Tools[0].Request.Method != "POST" {
		t.Fatalf("typed method = %q", service.Tools[0].Request.Method)
	}
	if service.Policy.MaxRequestBytes != DefaultHTTPMaxRequestBytes {
		t.Fatalf("max request bytes = %d", service.Policy.MaxRequestBytes)
	}

	resolved := ResolveHTTPService(service)
	if resolved.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("resolved header = %q", resolved.Headers["Authorization"])
	}
}

func TestLoadRejectsInvalidHTTPBindings(t *testing.T) {
	tests := map[string]string{
		"duplicate server name": `
server: {}
servers:
  - name: api
    url: https://mcp.example.com
http_services:
  - name: api
    base_url: https://api.example.com
`,
		"absolute tool url": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: unsafe
        request:
          method: GET
          path: https://attacker.example.com/data
`,
		"unknown template input": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: broken
        request:
          method: POST
          path: /v1/items
          body:
            template:
              value:
                $arg: missing
`,
		"protected raw header": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    raw_tool:
      paths: ["/v1/**"]
      request_headers: [Authorization]
`,
		"optional path input": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: get
        request:
          method: GET
          path: /v1/items/{item_id}
        inputs:
          item_id:
            type: string
`,
		"invalid omit option": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: create
        request:
          method: POST
          path: /v1/items
          body:
            template:
              value:
                $arg: value
                $omit_if_missing: yes
        inputs:
          value:
            type: string
`,
		"malformed raw path wildcard": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    raw_tool:
      paths: ["/v1/items*"]
`,
		"sensitive response header": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    policy:
      response_headers: [Set-Cookie]
`,
		"typed path traversal": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: unsafe
        request:
          method: GET
          path: /v1/%252e%252e/admin
`,
		"unsupported typed body content type": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: upload
        request:
          method: POST
          path: /v1/uploads
          body:
            content_type: image/png
            template:
              $arg: data
        inputs:
          data:
            type: string
            required: true
`,
		"optional input used without omission or default": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: create
        request:
          method: POST
          path: /v1/items
          body:
            template:
              note:
                $arg: note
        inputs:
          note:
            type: string
`,
		"invalid input default": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: list
        request:
          method: GET
          path: /v1/items
        inputs:
          limit:
            type: integer
            default: many
`,
		"cross-kind alias collision": `
server: {}
servers:
  - name: mcp-api
    alias: shared
    url: https://mcp.example.com
http_services:
  - name: rest-api
    alias: shared
    base_url: https://api.example.com
`,
		"unsafe configured transport header": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    headers:
      Host: attacker.example.com
`,
		"untyped path input": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: get
        request:
          method: GET
          path: /v1/items/{item_id}
        inputs:
          item_id:
            required: true
`,
		"timeout above daemon call limit": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    policy:
      timeout_seconds: 61
`,
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load() succeeded, want error")
			}
		})
	}
}

func TestLoadHTTPServiceAppliesDefaultsAndPreservesSecrets(t *testing.T) {
	t.Setenv("DEPLOY_API_TOKEN", "secret-token")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server: {}
servers: []
http_services:
  - name: deployment-api
    alias: deploy
    base_url: https://deployments.example.com
    headers:
      Authorization: Bearer ${DEPLOY_API_TOKEN}
    raw_tool:
      paths:
        - /v1/deployments/**
    tools:
      - name: create
        request:
          method: post
          path: /v1/teams/{team}/deployments
          body:
            template:
              release:
                name:
                  $format: "{service}:{version}"
                labels:
                  $arg: labels
                  $default: {}
        inputs:
          team:
            type: string
            required: true
          service:
            type: string
            required: true
          version:
            type: string
            required: true
          labels:
            type: object
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HTTPServices) != 1 {
		t.Fatalf("http services = %#v", cfg.HTTPServices)
	}
	service := cfg.HTTPServices[0]
	if service.Headers["Authorization"] != "Bearer ${DEPLOY_API_TOKEN}" {
		t.Fatalf("stored authorization = %q", service.Headers["Authorization"])
	}
	if got := ResolveHTTPService(service).Headers["Authorization"]; got != "Bearer secret-token" {
		t.Fatalf("resolved authorization = %q", got)
	}
	if service.Policy.TimeoutSeconds != DefaultHTTPTimeoutSeconds ||
		service.Policy.MaxRequestBytes != DefaultHTTPMaxRequestBytes ||
		service.Policy.MaxResponseBytes != DefaultHTTPMaxResponseBytes {
		t.Fatalf("http policy defaults = %#v", service.Policy)
	}
	if service.RawTool.Name != "request" || len(service.RawTool.Methods) != 2 {
		t.Fatalf("raw tool defaults = %#v", service.RawTool)
	}
	if service.Tools[0].Request.Method != "POST" ||
		service.Tools[0].Request.Body.ContentType != "application/json" {
		t.Fatalf("typed request defaults = %#v", service.Tools[0].Request)
	}
}

func TestLoadRejectsUnsafeHTTPBindings(t *testing.T) {
	tests := map[string]string{
		"duplicate server name": `
server: {}
servers:
  - name: shared
    url: https://mcp.example.com
http_services:
  - name: shared
    base_url: https://api.example.com
`,
		"unknown template argument": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: create
        request:
          method: POST
          path: /v1/items
          body:
            template:
              value:
                $arg: missing
        inputs: {}
`,
		"protected raw header": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    raw_tool:
      paths: ["/v1/**"]
      request_headers: [Authorization]
`,
		"path placeholder embedded in segment": `
server: {}
servers: []
http_services:
  - name: api
    base_url: https://api.example.com
    tools:
      - name: get
        request:
          method: GET
          path: /v1/items/prefix-{id}
        inputs:
          id:
            type: string
`,
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load() succeeded, want error")
			}
		})
	}
}

func TestValidateHTTPTemplateRejectsExcessiveNesting(t *testing.T) {
	var template interface{} = "value"
	for index := 0; index < 66; index++ {
		template = map[string]interface{}{"nested": template}
	}
	if err := validateHTTPTemplate("api", "tool", "body", template, nil); err == nil {
		t.Fatal("deep template was accepted")
	}
}

func TestUpsertServerRejectsInvalidTransport(t *testing.T) {
	cfg := &Config{}
	err := UpsertServer(cfg, MCPServer{
		Name:      "example",
		URL:       "https://example.com/mcp",
		Transport: "websocket",
	})
	if err == nil {
		t.Fatal("UpsertServer() succeeded, want error")
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("invalid server mutated config: %#v", cfg.Servers)
	}
}

func TestLoadOrInitCreatesPrivateConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	cfg, err := LoadOrInit(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("new config contains servers: %#v", cfg.Servers)
	}
	if cfg.Server.HistorySize != DefaultHistorySize {
		t.Fatalf("history size = %d, want %d", cfg.Server.HistorySize, DefaultHistorySize)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
}
