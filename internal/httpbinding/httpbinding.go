package httpbinding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
)

type Result struct {
	Status      int               `json:"status"`
	ContentType string            `json:"content_type,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        interface{}       `json:"body,omitempty"`
	Truncated   bool              `json:"truncated"`
}

type CallError struct {
	Result *Result
	text   string
}

func (e *CallError) Error() string {
	return e.text
}

type requestPlan struct {
	method      string
	escapedPath string
	decodedPath string
	query       url.Values
	headers     http.Header
	body        []byte
	contentType string
}

func ListTools(service config.HTTPService) []protocol.ToolInfo {
	items := make([]protocol.ToolInfo, 0, len(service.Tools)+1)
	if service.RawTool != nil {
		items = append(items, protocol.ToolInfo{
			Server:      service.Name,
			Name:        rawToolName(service.RawTool),
			Description: service.RawTool.Description,
			Required:    []string{"method", "path"},
			Properties:  []string{"body", "content_type", "headers", "method", "path", "query"},
		})
	}
	for _, tool := range service.Tools {
		required, properties := toolProperties(tool)
		items = append(items, protocol.ToolInfo{
			Server:      service.Name,
			Name:        tool.Name,
			Description: tool.Description,
			Required:    required,
			Properties:  properties,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
	return items
}

func InspectTool(service config.HTTPService, toolName string) (*protocol.ToolDetail, error) {
	if service.RawTool != nil && rawToolName(service.RawTool) == toolName {
		methods := normalizedMethods(service.RawTool.Methods)
		pathDescription := "Relative path under the configured base URL. Allowed patterns: " +
			strings.Join(service.RawTool.Paths, ", ")
		headerDescription := "No caller-provided request headers are allowed"
		if len(service.RawTool.RequestHeaders) > 0 {
			headerDescription = "Allowlisted request headers: " +
				strings.Join(service.RawTool.RequestHeaders, ", ")
		}
		return &protocol.ToolDetail{
			Server:      service.Name,
			Name:        toolName,
			Description: service.RawTool.Description,
			Properties: []protocol.PropertyDetail{
				{Name: "body", Description: "Optional JSON or text request body"},
				{Name: "content_type", Type: "string", Description: "Request body content type"},
				{Name: "headers", Type: "object", Description: headerDescription},
				{Name: "method", Type: "string", Enum: methods, Description: "HTTP method", Required: true},
				{Name: "path", Type: "string", Description: pathDescription, Required: true},
				{Name: "query", Type: "object", Description: "Query parameters"},
			},
		}, nil
	}
	for _, tool := range service.Tools {
		if tool.Name != toolName {
			continue
		}
		names := make([]string, 0, len(tool.Inputs))
		for name := range tool.Inputs {
			names = append(names, name)
		}
		sort.Strings(names)
		properties := make([]protocol.PropertyDetail, 0, len(names))
		for _, name := range names {
			input := tool.Inputs[name]
			properties = append(properties, protocol.PropertyDetail{
				Name:        name,
				Type:        input.Type,
				Enum:        append([]string(nil), input.Enum...),
				Description: input.Description,
				Required:    input.Required,
			})
		}
		return &protocol.ToolDetail{
			Server:      service.Name,
			Name:        tool.Name,
			Description: tool.Description,
			Properties:  properties,
		}, nil
	}
	return nil, fmt.Errorf("tool %q not found on http service %q", toolName, service.Name)
}

func Call(ctx context.Context, service config.HTTPService, toolName string, args map[string]interface{}) (interface{}, error) {
	service = config.ResolveHTTPService(service)
	if args == nil {
		args = map[string]interface{}{}
	}

	var plan *requestPlan
	var err error
	if service.RawTool != nil && rawToolName(service.RawTool) == toolName {
		plan, err = compileRawRequest(service, *service.RawTool, args)
	} else {
		var tool *config.HTTPTool
		for index := range service.Tools {
			if service.Tools[index].Name == toolName {
				tool = &service.Tools[index]
				break
			}
		}
		if tool == nil {
			return nil, fmt.Errorf("tool %q not found on http service %q", toolName, service.Name)
		}
		plan, err = compileTypedRequest(service, *tool, args)
	}
	if err != nil {
		return nil, err
	}
	return execute(ctx, service, plan)
}

func compileTypedRequest(service config.HTTPService, tool config.HTTPTool, args map[string]interface{}) (*requestPlan, error) {
	effective, err := validateTypedArguments(tool, args)
	if err != nil {
		return nil, err
	}
	escapedPath, decodedPath, err := renderTypedPath(tool.Request.Path, effective)
	if err != nil {
		return nil, err
	}
	query, err := renderQuery(tool.Request.Query, effective)
	if err != nil {
		return nil, err
	}
	headers := serviceHeaders(service)
	for name, template := range tool.Request.Headers {
		value, present, err := renderTemplate(template, effective, 0)
		if err != nil {
			return nil, fmt.Errorf("render header %q: %w", name, err)
		}
		if !present {
			continue
		}
		text, err := scalarString(value)
		if err != nil {
			return nil, fmt.Errorf("render header %q: %w", name, err)
		}
		headers.Set(name, text)
	}

	var body []byte
	contentType := ""
	if tool.Request.Body != nil {
		rendered, err := RenderTemplate(tool.Request.Body.Template, effective)
		if err != nil {
			return nil, fmt.Errorf("render request body: %w", err)
		}
		contentType = normalizedContentType(tool.Request.Body.ContentType)
		body, err = encodeBody(rendered, contentType)
		if err != nil {
			return nil, err
		}
	}
	if err := validateRequestPolicy(service.Policy, tool.Request.Method, contentType, int64(len(body))); err != nil {
		return nil, err
	}
	return &requestPlan{
		method:      strings.ToUpper(tool.Request.Method),
		escapedPath: escapedPath,
		decodedPath: decodedPath,
		query:       query,
		headers:     headers,
		body:        body,
		contentType: contentType,
	}, nil
}

func compileRawRequest(service config.HTTPService, raw config.HTTPRawTool, args map[string]interface{}) (*requestPlan, error) {
	allowedArgs := map[string]bool{
		"method": true, "path": true, "query": true, "headers": true, "body": true, "content_type": true,
	}
	for name := range args {
		if !allowedArgs[name] {
			return nil, fmt.Errorf("unknown raw request argument %q", name)
		}
	}

	method, ok := args["method"].(string)
	if !ok || strings.TrimSpace(method) == "" {
		return nil, fmt.Errorf("raw request method is required")
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if !containsFold(normalizedMethods(raw.Methods), method) {
		return nil, fmt.Errorf("raw request method %q is not allowed", method)
	}

	rawPath, ok := args["path"].(string)
	if !ok || strings.TrimSpace(rawPath) == "" {
		return nil, fmt.Errorf("raw request path is required")
	}
	escapedPath, decodedPath, err := canonicalizeRawPath(rawPath)
	if err != nil {
		return nil, err
	}
	if !matchesAnyPath(raw.Paths, decodedPath) {
		return nil, fmt.Errorf("raw request path %q is not allowed", decodedPath)
	}

	query := url.Values{}
	if rawQuery, exists := args["query"]; exists {
		queryMap, ok := rawQuery.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("raw request query must be an object")
		}
		if err := addQueryMap(query, queryMap); err != nil {
			return nil, err
		}
	}

	headers := serviceHeaders(service)
	allowedHeaders := make(map[string]bool, len(raw.RequestHeaders))
	for _, name := range raw.RequestHeaders {
		allowedHeaders[http.CanonicalHeaderKey(name)] = true
	}
	if rawHeaders, exists := args["headers"]; exists {
		headerMap, ok := rawHeaders.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("raw request headers must be an object")
		}
		for name, value := range headerMap {
			canonical := http.CanonicalHeaderKey(name)
			if protectedHeader(canonical) || !allowedHeaders[canonical] {
				return nil, fmt.Errorf("raw request header %q is not allowed", name)
			}
			text, err := scalarString(value)
			if err != nil {
				return nil, fmt.Errorf("raw request header %q: %w", name, err)
			}
			headers.Set(canonical, text)
		}
	}

	contentType := ""
	if rawContentType, exists := args["content_type"]; exists {
		var ok bool
		contentType, ok = rawContentType.(string)
		if !ok {
			return nil, fmt.Errorf("raw request content_type must be a string")
		}
		contentType = normalizedContentType(contentType)
	}
	var body []byte
	if rawBody, exists := args["body"]; exists {
		if contentType == "" {
			contentType = "application/json"
		}
		body, err = encodeBody(rawBody, contentType)
		if err != nil {
			return nil, err
		}
	} else if contentType != "" {
		return nil, fmt.Errorf("raw request content_type requires a body")
	}
	if err := validateRequestPolicy(service.Policy, method, contentType, int64(len(body))); err != nil {
		return nil, err
	}
	return &requestPlan{
		method:      method,
		escapedPath: escapedPath,
		decodedPath: decodedPath,
		query:       query,
		headers:     headers,
		body:        body,
		contentType: contentType,
	}, nil
}

func execute(ctx context.Context, service config.HTTPService, plan *requestPlan) (interface{}, error) {
	target, err := buildTargetURL(service.BaseURL, plan.escapedPath, plan.decodedPath, plan.query)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if plan.body != nil {
		body = bytes.NewReader(plan.body)
	}
	request, err := http.NewRequestWithContext(ctx, plan.method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	request.Header = plan.headers.Clone()
	if plan.contentType != "" {
		request.Header.Set("Content-Type", plan.contentType)
	}

	policy := effectivePolicy(service.Policy)
	client := &http.Client{Timeout: time.Duration(policy.TimeoutSeconds) * time.Second}
	switch policy.Redirects {
	case "disabled":
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	case "same-origin":
		originScheme := target.Scheme
		originHost := target.Host
		client.CheckRedirect = func(next *http.Request, _ []*http.Request) error {
			if next.URL.Scheme != originScheme || next.URL.Host != originHost {
				return fmt.Errorf("redirect to a different origin is not allowed")
			}
			return nil
		}
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("execute http request: %w", err)
	}
	defer response.Body.Close()

	maxBytes := policy.MaxResponseBytes
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read http response: %w", err)
	}
	truncated := int64(len(data)) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}

	contentType := normalizedContentType(response.Header.Get("Content-Type"))
	result := &Result{
		Status:      response.StatusCode,
		ContentType: contentType,
		Headers:     filteredHeaders(response.Header, policy.ResponseHeaders),
		Truncated:   truncated,
	}

	// A disallowed content type must withhold the payload, otherwise the
	// allowlist only annotates the response it was meant to keep out. Status
	// and headers still come back so the caller can see what happened.
	if len(data) > 0 && !contentTypeAllowed(contentType, policy.AllowedResponseContentTypes) {
		return result, &CallError{
			Result: result,
			text:   fmt.Sprintf("http response content type %q is not allowed", contentType),
		}
	}

	if len(data) > 0 {
		if isJSONContentType(contentType) {
			var parsed interface{}
			if err := json.Unmarshal(data, &parsed); err == nil {
				result.Body = parsed
			} else {
				result.Body = string(data)
			}
		} else {
			result.Body = string(data)
		}
	}

	if truncated {
		return result, &CallError{
			Result: result,
			text:   fmt.Sprintf("http response exceeded %d bytes", maxBytes),
		}
	}
	if !successfulStatus(response.StatusCode, policy.SuccessStatuses) {
		return result, &CallError{
			Result: result,
			text:   fmt.Sprintf("http request failed with status %d", response.StatusCode),
		}
	}
	return result, nil
}

func validateTypedArguments(tool config.HTTPTool, args map[string]interface{}) (map[string]interface{}, error) {
	effective := make(map[string]interface{}, len(args)+len(tool.Inputs))
	for name, value := range args {
		if _, ok := tool.Inputs[name]; !ok {
			return nil, fmt.Errorf("unknown argument %q for tool %q", name, tool.Name)
		}
		effective[name] = value
	}
	for name, input := range tool.Inputs {
		value, exists := effective[name]
		if !exists && input.Default != nil {
			value = input.Default
			effective[name] = value
			exists = true
		}
		if !exists {
			if input.Required {
				return nil, fmt.Errorf("missing required argument %q", name)
			}
			continue
		}
		if err := validateInputValue(name, input, value); err != nil {
			return nil, err
		}
	}
	return effective, nil
}

func validateInputValue(name string, input config.HTTPInput, value interface{}) error {
	if value == nil {
		if input.Type == "" {
			return nil
		}
		return fmt.Errorf("argument %q must be %s", name, input.Type)
	}
	valid := true
	switch input.Type {
	case "", "any":
	case "string":
		_, valid = value.(string)
	case "boolean":
		_, valid = value.(bool)
	case "integer":
		valid = isInteger(value)
	case "number":
		_, valid = numericValue(value)
	case "object":
		_, valid = value.(map[string]interface{})
	case "array":
		rv := reflect.ValueOf(value)
		valid = rv.IsValid() && (rv.Kind() == reflect.Array || rv.Kind() == reflect.Slice)
		if valid && input.Items != nil {
			for index := 0; index < rv.Len(); index++ {
				if err := validateInputValue(fmt.Sprintf("%s[%d]", name, index), *input.Items, rv.Index(index).Interface()); err != nil {
					return err
				}
			}
		}
	}
	if !valid {
		return fmt.Errorf("argument %q must be %s", name, input.Type)
	}
	if len(input.Enum) > 0 {
		text, ok := value.(string)
		if !ok || !containsString(input.Enum, text) {
			return fmt.Errorf("argument %q must be one of %s", name, strings.Join(input.Enum, ", "))
		}
	}
	if number, ok := numericValue(value); ok {
		if input.Minimum != nil && number < *input.Minimum {
			return fmt.Errorf("argument %q must be at least %v", name, *input.Minimum)
		}
		if input.Maximum != nil && number > *input.Maximum {
			return fmt.Errorf("argument %q must be at most %v", name, *input.Maximum)
		}
	}
	return nil
}

func renderTypedPath(template string, args map[string]interface{}) (string, string, error) {
	segments := strings.Split(template, "/")
	escaped := make([]string, len(segments))
	decoded := make([]string, len(segments))
	for index, segment := range segments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
			value, exists := args[name]
			if !exists {
				return "", "", fmt.Errorf("path argument %q is missing", name)
			}
			text, err := scalarString(value)
			if err != nil {
				return "", "", fmt.Errorf("path argument %q: %w", name, err)
			}
			if text == "" {
				return "", "", fmt.Errorf("path argument %q cannot be empty", name)
			}
			if text == "." || text == ".." || strings.ContainsAny(text, "/\\") {
				return "", "", fmt.Errorf("path argument %q must be one path segment", name)
			}
			decoded[index] = text
			escaped[index] = url.PathEscape(text)
			continue
		}
		unescaped, err := url.PathUnescape(segment)
		if err != nil {
			return "", "", fmt.Errorf("invalid configured path segment %q", segment)
		}
		decoded[index] = unescaped
		escaped[index] = url.PathEscape(unescaped)
	}
	return strings.Join(escaped, "/"), strings.Join(decoded, "/"), nil
}

func canonicalizeRawPath(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "?#\\") {
		return "", "", fmt.Errorf("raw request path must be an absolute relative path without query or fragment")
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return "", "", fmt.Errorf("decode raw request path: %w", err)
	}
	if strings.Contains(decoded, "\\") {
		return "", "", fmt.Errorf("raw request path cannot contain backslashes")
	}
	if strings.HasPrefix(decoded, "//") {
		return "", "", fmt.Errorf("raw request path cannot use a cross-origin form")
	}
	for _, segment := range strings.Split(decoded, "/") {
		if pathSegmentTraverses(segment) {
			return "", "", fmt.Errorf("raw request path cannot contain traversal segments")
		}
	}
	segments := strings.Split(decoded, "/")
	escaped := make([]string, len(segments))
	for index, segment := range segments {
		escaped[index] = url.PathEscape(segment)
	}
	return strings.Join(escaped, "/"), decoded, nil
}

func pathSegmentTraverses(segment string) bool {
	for depth := 0; depth < 4; depth++ {
		if segment == "." || segment == ".." {
			return true
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == segment {
			return false
		}
		segment = decoded
	}
	return segment == "." || segment == ".."
}

func renderQuery(templates map[string]interface{}, args map[string]interface{}) (url.Values, error) {
	query := url.Values{}
	for name, template := range templates {
		value, present, err := renderTemplate(template, args, 0)
		if err != nil {
			return nil, fmt.Errorf("render query %q: %w", name, err)
		}
		if !present {
			continue
		}
		if err := addQueryValue(query, name, value); err != nil {
			return nil, err
		}
	}
	return query, nil
}

func addQueryMap(query url.Values, values map[string]interface{}) error {
	for name, value := range values {
		if err := addQueryValue(query, name, value); err != nil {
			return err
		}
	}
	return nil
}

func addQueryValue(query url.Values, name string, value interface{}) error {
	if value == nil {
		query.Add(name, "")
		return nil
	}
	rv := reflect.ValueOf(value)
	if rv.IsValid() && (rv.Kind() == reflect.Array || rv.Kind() == reflect.Slice) {
		for index := 0; index < rv.Len(); index++ {
			text, err := scalarString(rv.Index(index).Interface())
			if err != nil {
				return fmt.Errorf("query %q: %w", name, err)
			}
			query.Add(name, text)
		}
		return nil
	}
	if _, ok := value.(map[string]interface{}); ok {
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode query %q: %w", name, err)
		}
		query.Add(name, string(data))
		return nil
	}
	text, err := scalarString(value)
	if err != nil {
		return fmt.Errorf("query %q: %w", name, err)
	}
	query.Add(name, text)
	return nil
}

func scalarString(value interface{}) (string, error) {
	switch item := value.(type) {
	case string:
		return item, nil
	case bool:
		return strconv.FormatBool(item), nil
	case int:
		return strconv.Itoa(item), nil
	case int8, int16, int32, int64:
		return fmt.Sprintf("%d", item), nil
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", item), nil
	case float32:
		return strconv.FormatFloat(float64(item), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("value must be a scalar")
	}
}

func encodeBody(value interface{}, contentType string) ([]byte, error) {
	if isJSONContentType(contentType) {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode json request body: %w", err)
		}
		return data, nil
	}
	if strings.HasPrefix(contentType, "text/") {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("text request body must be a string")
		}
		return []byte(text), nil
	}
	return nil, fmt.Errorf("unsupported request content type %q", contentType)
}

func buildTargetURL(base string, escapedPath string, decodedPath string, query url.Values) (*url.URL, error) {
	target, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	baseDecoded := strings.TrimSuffix(target.Path, "/")
	baseEscaped := strings.TrimSuffix(target.EscapedPath(), "/")
	target.Path = baseDecoded + decodedPath
	target.RawPath = baseEscaped + escapedPath
	target.RawQuery = query.Encode()
	target.Fragment = ""
	return target, nil
}

func validateRequestPolicy(policy config.HTTPPolicy, method string, contentType string, bodySize int64) error {
	policy = effectivePolicy(policy)
	if bodySize > policy.MaxRequestBytes {
		return fmt.Errorf("http request body exceeds %d bytes", policy.MaxRequestBytes)
	}
	if bodySize > 0 && !contentTypeAllowed(contentType, policy.AllowedRequestContentTypes) {
		return fmt.Errorf("http request content type %q is not allowed", contentType)
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if bodySize > 0 && (method == http.MethodGet || method == http.MethodHead) {
		return fmt.Errorf("%s requests cannot include a body", method)
	}
	return nil
}

func effectivePolicy(policy config.HTTPPolicy) config.HTTPPolicy {
	if policy.TimeoutSeconds == 0 {
		policy.TimeoutSeconds = config.DefaultHTTPTimeoutSeconds
	}
	if policy.MaxRequestBytes == 0 {
		policy.MaxRequestBytes = config.DefaultHTTPMaxRequestBytes
	}
	if policy.MaxResponseBytes == 0 {
		policy.MaxResponseBytes = config.DefaultHTTPMaxResponseBytes
	}
	if policy.Redirects == "" {
		policy.Redirects = "same-origin"
	}
	return policy
}

func serviceHeaders(service config.HTTPService) http.Header {
	headers := http.Header{}
	for name, value := range service.Headers {
		headers.Set(name, value)
	}
	return headers
}

func normalizedContentType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(value)
	}
	return strings.ToLower(mediaType)
}

func isJSONContentType(value string) bool {
	value = normalizedContentType(value)
	return value == "application/json" || strings.HasSuffix(value, "+json")
}

func contentTypeAllowed(value string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	value = normalizedContentType(value)
	for _, candidate := range allowed {
		candidate = normalizedContentType(candidate)
		if candidate == "*/*" {
			return true
		}
		if candidate == value {
			return true
		}
		if strings.HasSuffix(candidate, "/*") && strings.HasPrefix(value, strings.TrimSuffix(candidate, "*")) {
			return true
		}
	}
	return false
}

func successfulStatus(status int, allowed []int) bool {
	if len(allowed) == 0 {
		return status >= 200 && status <= 299
	}
	for _, candidate := range allowed {
		if candidate == status {
			return true
		}
	}
	return false
}

func filteredHeaders(headers http.Header, allowed []string) map[string]string {
	if len(allowed) == 0 {
		return nil
	}
	result := map[string]string{}
	for _, name := range allowed {
		if value := headers.Get(name); value != "" {
			result[strings.ToLower(http.CanonicalHeaderKey(name))] = value
		}
	}
	return result
}

func matchesAnyPath(patterns []string, candidate string) bool {
	candidateSegments := splitPathSegments(candidate)
	for _, pattern := range patterns {
		if matchPathSegments(splitPathSegments(pattern), candidateSegments) {
			return true
		}
	}
	return false
}

func splitPathSegments(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func matchPathSegments(pattern []string, candidate []string) bool {
	if len(pattern) == 0 {
		return len(candidate) == 0
	}
	if pattern[0] == "**" {
		if matchPathSegments(pattern[1:], candidate) {
			return true
		}
		return len(candidate) > 0 && matchPathSegments(pattern, candidate[1:])
	}
	if len(candidate) == 0 {
		return false
	}
	if pattern[0] != "*" && pattern[0] != candidate[0] {
		return false
	}
	return matchPathSegments(pattern[1:], candidate[1:])
}

func normalizedMethods(methods []string) []string {
	if len(methods) == 0 {
		return []string{http.MethodGet, http.MethodHead}
	}
	result := make([]string, len(methods))
	for index, method := range methods {
		result[index] = strings.ToUpper(strings.TrimSpace(method))
	}
	return result
}

func rawToolName(raw *config.HTTPRawTool) string {
	if raw == nil || strings.TrimSpace(raw.Name) == "" {
		return "request"
	}
	return raw.Name
}

func toolProperties(tool config.HTTPTool) ([]string, []string) {
	required := make([]string, 0, len(tool.Inputs))
	properties := make([]string, 0, len(tool.Inputs))
	for name, input := range tool.Inputs {
		properties = append(properties, name)
		if input.Required {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	sort.Strings(properties)
	return required, properties
}

func numericValue(value interface{}) (float64, bool) {
	switch item := value.(type) {
	case int:
		return float64(item), true
	case int8:
		return float64(item), true
	case int16:
		return float64(item), true
	case int32:
		return float64(item), true
	case int64:
		return float64(item), true
	case uint:
		return float64(item), true
	case uint8:
		return float64(item), true
	case uint16:
		return float64(item), true
	case uint32:
		return float64(item), true
	case uint64:
		return float64(item), true
	case float32:
		return float64(item), true
	case float64:
		return item, true
	default:
		return 0, false
	}
}

func isInteger(value interface{}) bool {
	number, ok := numericValue(value)
	return ok && number == float64(int64(number))
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func protectedHeader(name string) bool {
	switch http.CanonicalHeaderKey(strings.TrimSpace(name)) {
	case "Authorization", "Proxy-Authorization", "Host", "Content-Length", "Connection", "Transfer-Encoding":
		return true
	default:
		return false
	}
}
