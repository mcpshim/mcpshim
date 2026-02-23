package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/store"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

const oauthCallbackTimeout = 5 * time.Minute

func runWithOAuthFallback[T any](ctx context.Context, s config.MCPServer, dbStore *store.Store, interactive bool, operation func(compatibleClient) (T, error)) (T, error) {
	result, err := runOperation(ctx, s, operation)
	if err == nil || !shouldTryOAuthFallback(s, err) {
		return result, err
	}

	callback := (*oauthCallbackServer)(nil)
	redirectURI := "http://127.0.0.1:53685/oauth/callback"
	if interactive {
		callback, err = startOAuthCallbackServer()
		if err != nil {
			var zero T
			return zero, err
		}
		defer callback.close()
		redirectURI = callback.redirectURI
	}

	oauthClient, closeFn, err := newOAuthClient(s, mcpclient.OAuthConfig{
		RedirectURI: redirectURI,
		TokenStore:  newSQLiteTokenStore(dbStore, s.Name),
		PKCEEnabled: true,
	})
	if err != nil {
		var zero T
		return zero, err
	}
	defer closeFn()

	result, err = runOperationWithClient(ctx, oauthClient, operation)
	if err == nil {
		return result, nil
	}

	if !mcpclient.IsOAuthAuthorizationRequiredError(err) {
		return result, err
	}
	if !interactive {
		var zero T
		return zero, fmt.Errorf("server %q requires oauth authorization; run a direct command like mcpshim tools --server %s to complete login", s.Name, s.Name)
	}
	if callback == nil {
		var zero T
		return zero, errors.New("oauth callback server is not available")
	}

	if err := completeOAuthFlow(ctx, err, callback, false); err != nil {
		var zero T
		return zero, err
	}

	return runOperationWithClient(ctx, oauthClient, operation)
}

func runOAuthLogin(ctx context.Context, s config.MCPServer, dbStore *store.Store, manual bool) error {
	callback := (*oauthCallbackServer)(nil)
	redirectURI := "http://127.0.0.1:53685/oauth/callback"
	if !manual {
		var err error
		callback, err = startOAuthCallbackServer()
		if err != nil {
			return err
		}
		defer callback.close()
		redirectURI = callback.redirectURI
	}

	oauthClient, closeFn, err := newOAuthClient(s, mcpclient.OAuthConfig{
		RedirectURI: redirectURI,
		TokenStore:  newSQLiteTokenStore(dbStore, s.Name),
		PKCEEnabled: true,
	})
	if err != nil {
		return err
	}
	defer closeFn()

	_, err = runOperationWithClient(ctx, oauthClient, func(cli compatibleClient) (struct{}, error) {
		return struct{}{}, nil
	})
	if err == nil {
		return nil
	}
	if !mcpclient.IsOAuthAuthorizationRequiredError(err) {
		return err
	}

	return completeOAuthFlow(ctx, err, callback, manual)
}

func runOperation[T any](ctx context.Context, s config.MCPServer, operation func(compatibleClient) (T, error)) (T, error) {
	client, closeFn, err := newClient(s)
	if err != nil {
		var zero T
		return zero, err
	}
	defer closeFn()

	return runOperationWithClient(ctx, client, operation)
}

func runOperationWithClient[T any](ctx context.Context, client compatibleClient, operation func(compatibleClient) (T, error)) (T, error) {
	if err := client.Start(ctx); err != nil {
		var zero T
		return zero, err
	}
	initReq := mcpproto.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpproto.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpproto.Implementation{Name: "mcpshimd", Version: "dev"}
	if _, err := client.Initialize(ctx, initReq); err != nil {
		var zero T
		return zero, err
	}

	return operation(client)
}

func shouldTryOAuthFallback(s config.MCPServer, err error) bool {
	if err == nil {
		return false
	}
	if hasAuthorizationHeader(s.Headers) {
		return false
	}
	return errors.Is(err, transport.ErrUnauthorized)
}

func hasAuthorizationHeader(headers map[string]string) bool {
	for key := range headers {
		if http.CanonicalHeaderKey(key) == "Authorization" {
			return true
		}
	}
	return false
}

type oauthCallbackServer struct {
	redirectURI string
	server      *http.Server
	listener    net.Listener
	params      chan map[string]string
}

func startOAuthCallbackServer() (*oauthCallbackServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	params := make(chan map[string]string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		values := map[string]string{}
		for key, all := range r.URL.Query() {
			if len(all) > 0 {
				values[key] = all[0]
			}
		}
		select {
		case params <- values:
		default:
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body><h1>Authorization complete</h1><p>You can close this window.</p><script>window.close();</script></body></html>")
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()

	return &oauthCallbackServer{
		redirectURI: fmt.Sprintf("http://%s/oauth/callback", listener.Addr().String()),
		server:      server,
		listener:    listener,
		params:      params,
	}, nil
}

func (s *oauthCallbackServer) close() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
	_ = s.listener.Close()
}

func completeOAuthFlow(ctx context.Context, authErr error, callback *oauthCallbackServer, manual bool) error {
	oauthHandler := mcpclient.GetOAuthHandler(authErr)
	if oauthHandler == nil {
		return authErr
	}

	codeVerifier, err := mcpclient.GenerateCodeVerifier()
	if err != nil {
		return err
	}
	state, err := mcpclient.GenerateState()
	if err != nil {
		return err
	}
	codeChallenge := mcpclient.GenerateCodeChallenge(codeVerifier)

	if oauthHandler.GetClientID() == "" {
		if err := oauthHandler.RegisterClient(ctx, "mcpshim"); err != nil {
			return err
		}
	}

	authURL, err := oauthHandler.GetAuthorizationURL(ctx, state, codeChallenge)
	if err != nil {
		return err
	}

	fmt.Printf("oauth login required; authorize here: %s\n", authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("failed to open browser automatically: %v\n", err)
	}
	if manual {
		fmt.Println("manual mode: complete login in any browser/device, then paste the final redirect URL (or code).")
		params, err := readManualOAuthInput(state)
		if err != nil {
			return err
		}
		if code := params["code"]; code != "" {
			return oauthHandler.ProcessAuthorizationResponse(ctx, code, state, codeVerifier)
		}
		if oauthError := params["error"]; oauthError != "" {
			return fmt.Errorf("oauth authorization failed: %s", oauthError)
		}
		return errors.New("oauth authorization did not return a code")
	}
	if callback == nil {
		return errors.New("oauth callback server is not available")
	}
	fmt.Println("waiting for oauth callback...")

	waitCtx, cancel := context.WithTimeout(ctx, oauthCallbackTimeout)
	defer cancel()

	params, err := callback.wait(waitCtx)
	if err != nil {
		return err
	}
	if params["state"] != state {
		return fmt.Errorf("oauth state mismatch")
	}
	if code := params["code"]; code != "" {
		return oauthHandler.ProcessAuthorizationResponse(ctx, code, state, codeVerifier)
	}
	if oauthError := params["error"]; oauthError != "" {
		return fmt.Errorf("oauth authorization failed: %s", oauthError)
	}
	return errors.New("oauth authorization did not return a code")
}

func readManualOAuthInput(expectedState string) (map[string]string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("paste redirect URL or code: ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, errors.New("empty input")
	}

	if strings.Contains(line, "://") {
		u, err := url.Parse(line)
		if err != nil {
			return nil, err
		}
		params := map[string]string{}
		for key, values := range u.Query() {
			if len(values) > 0 {
				params[key] = values[0]
			}
		}
		if st := params["state"]; st != "" && st != expectedState {
			return nil, fmt.Errorf("oauth state mismatch")
		}
		return params, nil
	}

	return map[string]string{"code": line}, nil
}

func (s *oauthCallbackServer) wait(ctx context.Context) (map[string]string, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case params := <-s.params:
		return params, nil
	}
}

func newOAuthClient(s config.MCPServer, oauthConfig mcpclient.OAuthConfig) (compatibleClient, func(), error) {
	if s.Transport == "sse" {
		opts := []transport.ClientOption{}
		if len(s.Headers) > 0 {
			opts = append(opts, transport.WithHeaders(s.Headers))
		}
		cli, err := mcpclient.NewOAuthSSEClient(s.URL, oauthConfig, opts...)
		if err != nil {
			return nil, nil, err
		}
		return cli, func() { _ = cli.Close() }, nil
	}

	opts := []transport.StreamableHTTPCOption{}
	if len(s.Headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(s.Headers))
	}
	cli, err := mcpclient.NewOAuthStreamableHttpClient(s.URL, oauthConfig, opts...)
	if err != nil {
		return nil, nil, err
	}
	return cli, func() { _ = cli.Close() }, nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform %q", runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}