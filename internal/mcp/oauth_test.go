package mcp

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/store"
)

type fakeCompatibleClient struct {
	startErr      error
	initializeErr error
	started       bool
	initialized   bool
	closed        bool
}

func (f *fakeCompatibleClient) Start(context.Context) error {
	f.started = true
	return f.startErr
}

func (f *fakeCompatibleClient) Initialize(context.Context, mcpproto.InitializeRequest) (*mcpproto.InitializeResult, error) {
	f.initialized = true
	return &mcpproto.InitializeResult{}, f.initializeErr
}

func (f *fakeCompatibleClient) ListTools(context.Context, mcpproto.ListToolsRequest) (*mcpproto.ListToolsResult, error) {
	return &mcpproto.ListToolsResult{}, nil
}

func (f *fakeCompatibleClient) CallTool(context.Context, mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	return &mcpproto.CallToolResult{}, nil
}

func (f *fakeCompatibleClient) Close() error {
	f.closed = true
	return nil
}

func TestRunOperationWithClient(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		client := &fakeCompatibleClient{}
		got, err := runOperationWithClient(context.Background(), client, func(compatibleClient) (string, error) {
			return "done", nil
		})
		if err != nil || got != "done" {
			t.Fatalf("result = %q, %v", got, err)
		}
		if !client.started || !client.initialized {
			t.Fatalf("client lifecycle = %#v", client)
		}
	})

	t.Run("start failure", func(t *testing.T) {
		client := &fakeCompatibleClient{startErr: errors.New("start failed")}
		if _, err := runOperationWithClient(context.Background(), client, func(compatibleClient) (string, error) {
			return "unexpected", nil
		}); err == nil {
			t.Fatal("start failure was ignored")
		}
		if client.initialized {
			t.Fatal("Initialize called after Start failure")
		}
	})

	t.Run("initialize failure", func(t *testing.T) {
		client := &fakeCompatibleClient{initializeErr: errors.New("initialize failed")}
		if _, err := runOperationWithClient(context.Background(), client, func(compatibleClient) (string, error) {
			return "unexpected", nil
		}); err == nil {
			t.Fatal("initialize failure was ignored")
		}
	})
}

func TestOAuthFallbackConditions(t *testing.T) {
	unauthorized := transport.ErrUnauthorized
	if !shouldTryOAuthFallback(config.MCPServer{}, unauthorized) {
		t.Fatal("unauthorized response should try OAuth")
	}
	if shouldTryOAuthFallback(config.MCPServer{
		Headers: map[string]string{"authorization": "Bearer token"},
	}, unauthorized) {
		t.Fatal("explicit authorization header should disable OAuth fallback")
	}
	if shouldTryOAuthFallback(config.MCPServer{}, errors.New("other")) {
		t.Fatal("unrelated error should not try OAuth")
	}
	if !hasAuthorizationHeader(map[string]string{"AUTHORIZATION": "x"}) {
		t.Fatal("authorization header check is not case-insensitive")
	}
}

func TestOAuthCallbackServer(t *testing.T) {
	callback, err := startOAuthCallbackServer()
	if err != nil {
		t.Fatal(err)
	}
	defer callback.close()

	response, err := http.Get(callback.redirectURI + "?code=abc&state=state")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	params, err := callback.wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if params["code"] != "abc" || params["state"] != "state" {
		t.Fatalf("callback params = %#v", params)
	}
}

func TestSQLiteTokenStore(t *testing.T) {
	dbStore, err := store.Open(filepath.Join(t.TempDir(), "mcpshim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	tokenStore := newSQLiteTokenStore(dbStore, "example")
	if _, err := tokenStore.GetToken(context.Background()); !errors.Is(err, transport.ErrNoToken) {
		t.Fatalf("missing token error = %v", err)
	}

	want := &mcpclient.Token{AccessToken: "access"}
	if err := tokenStore.SaveToken(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := tokenStore.GetToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken {
		t.Fatalf("token = %#v, want %#v", got, want)
	}
	if tokenStore.String() != "sqliteTokenStore(example)" {
		t.Fatalf("String() = %q", tokenStore.String())
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tokenStore.GetToken(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled GetToken error = %v", err)
	}
}
