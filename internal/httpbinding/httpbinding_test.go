package httpbinding

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
)

func TestRenderTemplateSupportsNestedTypedArguments(t *testing.T) {
	template := map[string]interface{}{
		"release": map[string]interface{}{
			"name": map[string]interface{}{"$format": "{service}:{version}"},
			"target": map[string]interface{}{
				"environment": map[string]interface{}{"$arg": "environment"},
				"region": map[string]interface{}{
					"$arg":     "region",
					"$default": "us-east-1",
				},
			},
			"labels": map[string]interface{}{"$arg": "labels"},
			"assignee": map[string]interface{}{
				"$arg":             "assignee",
				"$omit_if_missing": true,
			},
			"checks": []interface{}{
				map[string]interface{}{"type": "http", "path": "/health"},
				map[string]interface{}{
					"$arg":             "custom_check",
					"$omit_if_missing": true,
				},
			},
		},
	}
	args := map[string]interface{}{
		"service":     "billing",
		"version":     "v2",
		"environment": "production",
		"labels":      map[string]interface{}{"tier": "critical"},
	}

	got, err := RenderTemplate(template, args)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"release": map[string]interface{}{
			"name": "billing:v2",
			"target": map[string]interface{}{
				"environment": "production",
				"region":      "us-east-1",
			},
			"labels": map[string]interface{}{"tier": "critical"},
			"checks": []interface{}{
				map[string]interface{}{"type": "http", "path": "/health"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered template = %#v, want %#v", got, want)
	}
}

func TestTypedHTTPToolCompilesAndExecutesRequest(t *testing.T) {
	var captured struct {
		Method      string
		Path        string
		Query       string
		Auth        string
		Idempotency string
		Body        map[string]interface{}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.Query = r.URL.RawQuery
		captured.Auth = r.Header.Get("Authorization")
		captured.Idempotency = r.Header.Get("Idempotency-Key")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &captured.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "req-1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"dep-1","state":"pending"}`))
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "deployments",
		Alias:   "deploy",
		BaseURL: upstream.URL,
		Headers: map[string]string{"Authorization": "Bearer secret"},
		Policy: config.HTTPPolicy{
			ResponseHeaders: []string{"X-Request-ID"},
		},
		Tools: []config.HTTPTool{{
			Name: "create",
			Request: config.HTTPRequest{
				Method: "POST",
				Path:   "/v1/teams/{team}/deployments",
				Query: map[string]interface{}{
					"dry_run": map[string]interface{}{"$arg": "dry_run", "$default": false},
				},
				Headers: map[string]interface{}{
					"Idempotency-Key": map[string]interface{}{"$arg": "idempotency_key"},
				},
				Body: &config.HTTPBody{
					ContentType: "application/json",
					Template: map[string]interface{}{
						"application": map[string]interface{}{
							"name": map[string]interface{}{"$arg": "service"},
							"image": map[string]interface{}{
								"tag": map[string]interface{}{"$arg": "version"},
							},
						},
						"labels": map[string]interface{}{"$arg": "labels", "$default": map[string]interface{}{}},
					},
				},
			},
			Inputs: map[string]config.HTTPInput{
				"team":            {Type: "string", Required: true},
				"service":         {Type: "string", Required: true},
				"version":         {Type: "string", Required: true},
				"dry_run":         {Type: "boolean"},
				"idempotency_key": {Type: "string", Required: true},
				"labels":          {Type: "object"},
			},
		}},
	}

	rawResult, err := Call(context.Background(), service, "create", map[string]interface{}{
		"team":            "platform",
		"service":         "billing",
		"version":         "v2",
		"idempotency_key": "billing-v2",
		"labels":          map[string]interface{}{"tier": "critical"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := rawResult.(*Result)
	if result.Status != http.StatusCreated || result.Headers["x-request-id"] != "req-1" {
		t.Fatalf("result = %#v", result)
	}
	if captured.Method != "POST" || captured.Path != "/v1/teams/platform/deployments" || captured.Query != "dry_run=false" {
		t.Fatalf("request target = %s %s?%s", captured.Method, captured.Path, captured.Query)
	}
	if captured.Auth != "Bearer secret" || captured.Idempotency != "billing-v2" {
		t.Fatalf("request headers auth=%q idempotency=%q", captured.Auth, captured.Idempotency)
	}
	wantBody := map[string]interface{}{
		"application": map[string]interface{}{
			"name":  "billing",
			"image": map[string]interface{}{"tag": "v2"},
		},
		"labels": map[string]interface{}{"tier": "critical"},
	}
	if !reflect.DeepEqual(captured.Body, wantBody) {
		t.Fatalf("request body = %#v, want %#v", captured.Body, wantBody)
	}
}

func TestRawHTTPToolEnforcesMethodPathAndProtectedHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer configured" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "api",
		BaseURL: upstream.URL,
		Headers: map[string]string{"Authorization": "Bearer configured"},
		RawTool: &config.HTTPRawTool{
			Name:           "request",
			Methods:        []string{"GET", "POST"},
			Paths:          []string{"/v1/items/**"},
			RequestHeaders: []string{"If-Match"},
		},
	}

	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "DELETE",
		"path":   "/v1/items/1",
	}); err == nil {
		t.Fatal("raw tool accepted a disallowed method")
	}
	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/admin/secrets",
	}); err == nil {
		t.Fatal("raw tool accepted a disallowed path")
	}
	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method":  "GET",
		"path":    "/v1/items/1",
		"headers": map[string]interface{}{"Authorization": "Bearer attacker"},
	}); err == nil {
		t.Fatal("raw tool accepted a protected header override")
	}
	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items/1",
		"headers": map[string]interface{}{
			"If-Match": `"revision-1"`,
		},
	}); err != nil {
		t.Fatalf("approved raw request failed: %v", err)
	}
	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items/%2e%2e/secrets",
	}); err == nil {
		t.Fatal("raw tool accepted encoded path traversal")
	}
	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items/%252e%252e/secrets",
	}); err == nil {
		t.Fatal("raw tool accepted double-encoded path traversal")
	}
	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items/1",
		"body":   map[string]interface{}{"unexpected": true},
	}); err == nil {
		t.Fatal("raw tool accepted a GET request body")
	}
}

func TestTypedHTTPToolRejectsNullForTypedInput(t *testing.T) {
	service := config.HTTPService{
		Name:    "api",
		BaseURL: "https://api.example.com",
		Tools: []config.HTTPTool{{
			Name: "get",
			Request: config.HTTPRequest{
				Method: "GET",
				Path:   "/v1/items/{id}",
			},
			Inputs: map[string]config.HTTPInput{
				"id": {Type: "string", Required: true},
			},
		}},
	}

	if _, err := Call(context.Background(), service, "get", map[string]interface{}{
		"id": nil,
	}); err == nil {
		t.Fatal("typed tool accepted null for a string input")
	}
}

func TestHTTPFailuresPreserveBoundedResult(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"conflict","detail":"too much response data"}`))
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "api",
		BaseURL: upstream.URL,
		Policy: config.HTTPPolicy{
			MaxResponseBytes: 1024,
		},
		RawTool: &config.HTTPRawTool{
			Name:    "request",
			Methods: []string{"GET"},
			Paths:   []string{"/v1/**"},
		},
	}

	rawResult, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items/1",
	})
	if err == nil {
		t.Fatal("non-success status did not fail")
	}
	result, ok := rawResult.(*Result)
	if !ok || result.Status != http.StatusConflict || result.Truncated {
		t.Fatalf("result = %#v", rawResult)
	}
	body, ok := result.Body.(map[string]interface{})
	if !ok || body["code"] != "conflict" {
		t.Fatalf("body = %#v", result.Body)
	}

	service.Policy.MaxResponseBytes = 8
	rawResult, err = Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items/1",
	})
	if err == nil {
		t.Fatal("truncated response did not fail")
	}
	result = rawResult.(*Result)
	if !result.Truncated || len(result.Body.(string)) > 8 {
		t.Fatalf("truncated result = %#v", result)
	}
}

func TestSameOriginRedirectPolicyRejectsHostChange(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("cross-origin redirect reached destination")
	}))
	defer destination.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/stolen", http.StatusFound)
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "api",
		BaseURL: upstream.URL,
		Policy:  config.HTTPPolicy{Redirects: "same-origin"},
		RawTool: &config.HTTPRawTool{
			Name:    "request",
			Methods: []string{"GET"},
			Paths:   []string{"/v1/**"},
		},
	}
	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/redirect",
	}); err == nil {
		t.Fatal("cross-origin redirect succeeded")
	}
}

func TestRawHTTPToolSendsStructuredQueryAndBody(t *testing.T) {
	var capturedQuery url.Values
	var capturedBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "api",
		BaseURL: upstream.URL,
		RawTool: &config.HTTPRawTool{
			Name:    "request",
			Methods: []string{"POST"},
			Paths:   []string{"/v1/items/**"},
		},
		Policy: config.HTTPPolicy{SuccessStatuses: []int{http.StatusAccepted}},
	}
	result, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "POST",
		"path":   "/v1/items/one",
		"query": map[string]interface{}{
			"include": []interface{}{"stock", "location"},
		},
		"body": map[string]interface{}{
			"name":    "Example",
			"enabled": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.(*Result).Status != http.StatusAccepted {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(capturedQuery["include"], []string{"stock", "location"}) {
		t.Fatalf("query = %#v", capturedQuery)
	}
	if capturedBody["name"] != "Example" || capturedBody["enabled"] != true {
		t.Fatalf("body = %#v", capturedBody)
	}
}

func TestRawHTTPToolPreservesTrailingSlash(t *testing.T) {
	var receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "api",
		BaseURL: upstream.URL,
		RawTool: &config.HTTPRawTool{
			Name:    "request",
			Methods: []string{"GET"},
			Paths:   []string{"/v1/items/**"},
		},
	}
	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items/",
	}); err != nil {
		t.Fatal(err)
	}
	if receivedPath != "/v1/items/" {
		t.Fatalf("received path = %q, want trailing slash preserved", receivedPath)
	}
}

func TestInspectRawHTTPToolDescribesAllowlist(t *testing.T) {
	service := config.HTTPService{
		Name: "api",
		RawTool: &config.HTTPRawTool{
			Name:           "request",
			Methods:        []string{"GET", "PATCH"},
			Paths:          []string{"/v1/items/*", "/v1/jobs/**"},
			RequestHeaders: []string{"If-Match", "X-Request-ID"},
		},
	}

	detail, err := InspectTool(service, "request")
	if err != nil {
		t.Fatal(err)
	}
	properties := map[string]protocol.PropertyDetail{}
	for _, property := range detail.Properties {
		properties[property.Name] = property
	}
	if got := properties["path"].Description; !strings.Contains(got, "/v1/items/*") || !strings.Contains(got, "/v1/jobs/**") {
		t.Fatalf("path description = %q", got)
	}
	if got := properties["headers"].Description; !strings.Contains(got, "If-Match") || !strings.Contains(got, "X-Request-ID") {
		t.Fatalf("headers description = %q", got)
	}
}

func TestContentTypePolicySupportsAnyWildcard(t *testing.T) {
	if !contentTypeAllowed("application/problem+json", []string{"*/*"}) {
		t.Fatal("*/* did not allow a response content type")
	}
}

func TestTypedInputEnumIsCaseSensitive(t *testing.T) {
	err := validateInputValue(
		"environment",
		config.HTTPInput{Type: "string", Enum: []string{"production"}},
		"Production",
	)
	if err == nil {
		t.Fatal("case-mismatched enum value was accepted")
	}
}

func TestHTTPCallPreservesNonSuccessResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"conflict"}`))
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "api",
		BaseURL: upstream.URL,
		RawTool: &config.HTTPRawTool{
			Name:    "request",
			Methods: []string{"GET"},
			Paths:   []string{"/v1/**"},
		},
	}
	rawResult, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items/one",
	})
	if err == nil {
		t.Fatal("non-success response did not fail")
	}
	result := rawResult.(*Result)
	if result.Status != http.StatusConflict {
		t.Fatalf("result = %#v", result)
	}
	body := result.Body.(map[string]interface{})
	if body["code"] != "conflict" {
		t.Fatalf("body = %#v", body)
	}
	callErr, ok := err.(*CallError)
	if !ok || callErr.Result != result {
		t.Fatalf("error = %#v", err)
	}
}

func TestRawHTTPToolRejectsTraversalAndCrossOriginRedirect(t *testing.T) {
	destinationReached := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationReached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/stolen", http.StatusFound)
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "api",
		BaseURL: upstream.URL,
		RawTool: &config.HTTPRawTool{
			Name:    "request",
			Methods: []string{"GET"},
			Paths:   []string{"/v1/**"},
		},
	}
	for _, unsafePath := range []string{"/v1/../admin", "/v1/%2e%2e/admin", "//other.example/path"} {
		if _, err := Call(context.Background(), service, "request", map[string]interface{}{
			"method": "GET",
			"path":   unsafePath,
		}); err == nil {
			t.Fatalf("unsafe path %q was accepted", unsafePath)
		}
	}
	if _, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/redirect",
	}); err == nil {
		t.Fatal("cross-origin redirect was accepted")
	}
	if destinationReached {
		t.Fatal("cross-origin redirect reached its destination")
	}
}

func TestHTTPResponseSizeLimitReturnsBoundedResult(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "api",
		BaseURL: upstream.URL,
		Policy: config.HTTPPolicy{
			MaxResponseBytes: 5,
		},
		RawTool: &config.HTTPRawTool{
			Name:    "request",
			Methods: []string{"GET"},
			Paths:   []string{"/v1/**"},
		},
	}
	rawResult, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items",
	})
	if err == nil {
		t.Fatal("oversized response did not fail")
	}
	result := rawResult.(*Result)
	if !result.Truncated || result.Body != "01234" {
		t.Fatalf("bounded result = %#v", result)
	}
}

func TestDisallowedResponseContentTypeWithholdsBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Request-Id", "req-1")
		_, _ = w.Write([]byte("<html>secret</html>"))
	}))
	defer upstream.Close()

	service := config.HTTPService{
		Name:    "api",
		BaseURL: upstream.URL,
		Policy: config.HTTPPolicy{
			AllowedResponseContentTypes: []string{"application/json"},
			ResponseHeaders:             []string{"X-Request-Id"},
		},
		RawTool: &config.HTTPRawTool{
			Name:    "request",
			Methods: []string{"GET"},
			Paths:   []string{"/v1/**"},
		},
	}

	rawResult, err := Call(context.Background(), service, "request", map[string]interface{}{
		"method": "GET",
		"path":   "/v1/items",
	})
	if err == nil {
		t.Fatal("disallowed response content type did not fail")
	}
	result, ok := rawResult.(*Result)
	if !ok {
		t.Fatalf("result = %#v", rawResult)
	}
	if result.Body != nil {
		t.Fatalf("disallowed content type returned a body: %#v", result.Body)
	}
	if result.Status != http.StatusOK || result.ContentType != "text/html" {
		t.Fatalf("result lost diagnostic detail: %#v", result)
	}
	if result.Headers["x-request-id"] != "req-1" {
		t.Fatalf("result headers = %#v", result.Headers)
	}
}
