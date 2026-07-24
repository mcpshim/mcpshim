package config

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
)

const (
	DefaultHTTPTimeoutSeconds   = 20
	DefaultHTTPMaxRequestBytes  = int64(1 << 20)
	DefaultHTTPMaxResponseBytes = int64(2 << 20)
	maxHTTPTemplateDepth        = 64
	maxHTTPTimeoutSeconds       = 60
)

var (
	httpTemplateArgumentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	httpFormatArgumentPattern   = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_-]*)\}`)
	httpHeaderNamePattern       = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

type HTTPService struct {
	Name        string            `yaml:"name"`
	Alias       string            `yaml:"alias,omitempty"`
	Description string            `yaml:"description,omitempty"`
	BaseURL     string            `yaml:"base_url"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	Policy      HTTPPolicy        `yaml:"policy,omitempty"`
	RawTool     *HTTPRawTool      `yaml:"raw_tool,omitempty"`
	Tools       []HTTPTool        `yaml:"tools,omitempty"`
}

type HTTPPolicy struct {
	TimeoutSeconds              int      `yaml:"timeout_seconds,omitempty"`
	MaxRequestBytes             int64    `yaml:"max_request_bytes,omitempty"`
	MaxResponseBytes            int64    `yaml:"max_response_bytes,omitempty"`
	Redirects                   string   `yaml:"redirects,omitempty"`
	AllowedRequestContentTypes  []string `yaml:"allowed_request_content_types,omitempty"`
	AllowedResponseContentTypes []string `yaml:"allowed_response_content_types,omitempty"`
	SuccessStatuses             []int    `yaml:"success_statuses,omitempty"`
	ResponseHeaders             []string `yaml:"response_headers,omitempty"`
}

type HTTPRawTool struct {
	Name           string   `yaml:"name,omitempty"`
	Description    string   `yaml:"description,omitempty"`
	Methods        []string `yaml:"methods,omitempty"`
	Paths          []string `yaml:"paths"`
	RequestHeaders []string `yaml:"request_headers,omitempty"`
}

type HTTPTool struct {
	Name        string               `yaml:"name"`
	Description string               `yaml:"description,omitempty"`
	Request     HTTPRequest          `yaml:"request"`
	Inputs      map[string]HTTPInput `yaml:"inputs,omitempty"`
}

type HTTPRequest struct {
	Method  string                 `yaml:"method"`
	Path    string                 `yaml:"path"`
	Query   map[string]interface{} `yaml:"query,omitempty"`
	Headers map[string]interface{} `yaml:"headers,omitempty"`
	Body    *HTTPBody              `yaml:"body,omitempty"`
}

type HTTPBody struct {
	ContentType string      `yaml:"content_type,omitempty"`
	Template    interface{} `yaml:"template"`
}

type HTTPInput struct {
	Type        string      `yaml:"type"`
	Description string      `yaml:"description,omitempty"`
	Required    bool        `yaml:"required,omitempty"`
	Default     interface{} `yaml:"default,omitempty"`
	Enum        []string    `yaml:"enum,omitempty"`
	Items       *HTTPInput  `yaml:"items,omitempty"`
	Minimum     *float64    `yaml:"minimum,omitempty"`
	Maximum     *float64    `yaml:"maximum,omitempty"`
}

func normalizeHTTPServices(services []HTTPService) {
	for serviceIndex := range services {
		service := &services[serviceIndex]
		if service.Alias == "" {
			service.Alias = service.Name
		}
		if service.Policy.TimeoutSeconds == 0 {
			service.Policy.TimeoutSeconds = DefaultHTTPTimeoutSeconds
		}
		if service.Policy.MaxRequestBytes == 0 {
			service.Policy.MaxRequestBytes = DefaultHTTPMaxRequestBytes
		}
		if service.Policy.MaxResponseBytes == 0 {
			service.Policy.MaxResponseBytes = DefaultHTTPMaxResponseBytes
		}
		if service.Policy.Redirects == "" {
			service.Policy.Redirects = "same-origin"
		}
		if service.RawTool != nil {
			if service.RawTool.Name == "" {
				service.RawTool.Name = "request"
			}
			if len(service.RawTool.Methods) == 0 {
				service.RawTool.Methods = []string{http.MethodGet, http.MethodHead}
			}
			for methodIndex := range service.RawTool.Methods {
				service.RawTool.Methods[methodIndex] = strings.ToUpper(strings.TrimSpace(service.RawTool.Methods[methodIndex]))
			}
		}
		for toolIndex := range service.Tools {
			tool := &service.Tools[toolIndex]
			tool.Request.Method = strings.ToUpper(strings.TrimSpace(tool.Request.Method))
			if tool.Request.Body != nil && tool.Request.Body.ContentType == "" {
				tool.Request.Body.ContentType = "application/json"
			}
		}
	}
}

func ResolveHTTPService(service HTTPService) HTTPService {
	service.BaseURL = strings.TrimSpace(expandEnvironment(service.BaseURL))
	if service.Headers != nil {
		headers := make(map[string]string, len(service.Headers))
		for name, value := range service.Headers {
			headers[name] = expandEnvironment(value)
		}
		service.Headers = headers
	}
	return service
}

func expandEnvironment(value string) string {
	expanded, _ := expandEnvStrict(value)
	return expanded
}

func cloneHTTPServices(services []HTTPService) []HTTPService {
	data := make([]HTTPService, len(services))
	for index, service := range services {
		data[index] = service
		data[index].Headers = cloneStringMap(service.Headers)
		data[index].Policy.AllowedRequestContentTypes = append([]string(nil), service.Policy.AllowedRequestContentTypes...)
		data[index].Policy.AllowedResponseContentTypes = append([]string(nil), service.Policy.AllowedResponseContentTypes...)
		data[index].Policy.SuccessStatuses = append([]int(nil), service.Policy.SuccessStatuses...)
		data[index].Policy.ResponseHeaders = append([]string(nil), service.Policy.ResponseHeaders...)
		if service.RawTool != nil {
			raw := *service.RawTool
			raw.Methods = append([]string(nil), service.RawTool.Methods...)
			raw.Paths = append([]string(nil), service.RawTool.Paths...)
			raw.RequestHeaders = append([]string(nil), service.RawTool.RequestHeaders...)
			data[index].RawTool = &raw
		}
		data[index].Tools = make([]HTTPTool, len(service.Tools))
		for toolIndex, tool := range service.Tools {
			data[index].Tools[toolIndex] = tool
			data[index].Tools[toolIndex].Request.Query = cloneTemplateMap(tool.Request.Query)
			data[index].Tools[toolIndex].Request.Headers = cloneTemplateMap(tool.Request.Headers)
			if tool.Request.Body != nil {
				body := *tool.Request.Body
				body.Template = cloneTemplateValue(tool.Request.Body.Template)
				data[index].Tools[toolIndex].Request.Body = &body
			}
			data[index].Tools[toolIndex].Inputs = cloneHTTPInputs(tool.Inputs)
		}
	}
	return data
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneHTTPInputs(source map[string]HTTPInput) map[string]HTTPInput {
	if source == nil {
		return nil
	}
	result := make(map[string]HTTPInput, len(source))
	for key, value := range source {
		result[key] = cloneHTTPInput(value)
	}
	return result
}

func cloneHTTPInput(value HTTPInput) HTTPInput {
	value.Default = cloneTemplateValue(value.Default)
	value.Enum = append([]string(nil), value.Enum...)
	if value.Items != nil {
		items := cloneHTTPInput(*value.Items)
		value.Items = &items
	}
	return value
}

func cloneTemplateMap(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}
	result := make(map[string]interface{}, len(source))
	for key, value := range source {
		result[key] = cloneTemplateValue(value)
	}
	return result
}

func cloneTemplateValue(value interface{}) interface{} {
	switch item := value.(type) {
	case map[string]interface{}:
		return cloneTemplateMap(item)
	case []interface{}:
		result := make([]interface{}, len(item))
		for index, child := range item {
			result[index] = cloneTemplateValue(child)
		}
		return result
	default:
		return value
	}
}

func validateHTTPServices(cfg *Config, names map[string]bool, aliases map[string]bool) error {
	for _, service := range cfg.HTTPServices {
		if strings.TrimSpace(service.Name) == "" {
			return fmt.Errorf("http service name is required")
		}
		alias := service.Alias
		if alias == "" {
			alias = service.Name
		}
		if names[service.Name] || aliases[service.Name] {
			return fmt.Errorf("duplicate server identifier %q", service.Name)
		}
		if alias != service.Name && (names[alias] || aliases[alias]) {
			return fmt.Errorf("duplicate server identifier %q", alias)
		}
		names[service.Name] = true
		aliases[alias] = true

		resolvedURL, missingVariable := expandEnvStrict(service.BaseURL)
		if missingVariable != "" {
			return fmt.Errorf("http service %q base_url references unset environment variable %q", service.Name, missingVariable)
		}
		baseURL, err := url.Parse(strings.TrimSpace(resolvedURL))
		if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
			return fmt.Errorf("http service %q base_url must be an absolute URL", service.Name)
		}
		if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
			return fmt.Errorf("http service %q base_url must use http or https", service.Name)
		}
		if baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
			return fmt.Errorf("http service %q base_url cannot contain userinfo, query, or fragment", service.Name)
		}
		for header, value := range service.Headers {
			if !validHTTPHeaderName(header) {
				return fmt.Errorf("http service %q contains invalid header name %q", service.Name, header)
			}
			if isUnsafeConfiguredHTTPHeader(header) {
				return fmt.Errorf("http service %q cannot configure transport header %q", service.Name, header)
			}
			if _, missingHeaderVariable := expandEnvStrict(value); missingHeaderVariable != "" {
				return fmt.Errorf("http service %q header %q references unset environment variable %q", service.Name, header, missingHeaderVariable)
			}
		}
		if err := validateHTTPPolicy(service.Name, service.Policy); err != nil {
			return err
		}

		toolNames := map[string]bool{}
		if service.RawTool != nil {
			if err := validateHTTPRawTool(service.Name, *service.RawTool); err != nil {
				return err
			}
			toolNames[service.RawTool.Name] = true
		}
		for _, tool := range service.Tools {
			if strings.TrimSpace(tool.Name) == "" {
				return fmt.Errorf("http service %q has a tool without a name", service.Name)
			}
			if toolNames[tool.Name] {
				return fmt.Errorf("http service %q has duplicate tool name %q", service.Name, tool.Name)
			}
			toolNames[tool.Name] = true
			if err := validateHTTPTool(service.Name, tool); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateHTTPPolicy(serviceName string, policy HTTPPolicy) error {
	if policy.TimeoutSeconds < 0 || policy.MaxRequestBytes < 0 || policy.MaxResponseBytes < 0 {
		return fmt.Errorf("http service %q policy limits cannot be negative", serviceName)
	}
	if policy.TimeoutSeconds > maxHTTPTimeoutSeconds {
		return fmt.Errorf(
			"http service %q timeout_seconds cannot exceed %d",
			serviceName,
			maxHTTPTimeoutSeconds,
		)
	}
	switch policy.Redirects {
	case "", "disabled", "same-origin", "any":
	default:
		return fmt.Errorf("http service %q has unsupported redirects policy %q", serviceName, policy.Redirects)
	}
	for _, status := range policy.SuccessStatuses {
		if status < 100 || status > 599 {
			return fmt.Errorf("http service %q has invalid success status %d", serviceName, status)
		}
	}
	for _, contentType := range append(
		append([]string(nil), policy.AllowedRequestContentTypes...),
		policy.AllowedResponseContentTypes...,
	) {
		if !validHTTPContentTypePattern(contentType) {
			return fmt.Errorf("http service %q has invalid content type %q", serviceName, contentType)
		}
	}
	for _, header := range policy.ResponseHeaders {
		if !validHTTPHeaderName(header) {
			return fmt.Errorf("http service %q has invalid response header %q", serviceName, header)
		}
		if isSensitiveHTTPResponseHeader(header) {
			return fmt.Errorf("http service %q cannot expose sensitive response header %q", serviceName, header)
		}
	}
	return nil
}

func validateHTTPRawTool(serviceName string, raw HTTPRawTool) error {
	if strings.TrimSpace(raw.Name) == "" {
		return fmt.Errorf("http service %q raw tool name is required", serviceName)
	}
	for _, method := range raw.Methods {
		if !validHTTPMethod(method) {
			return fmt.Errorf("http service %q raw tool has unsupported method %q", serviceName, method)
		}
	}
	if len(raw.Paths) == 0 {
		return fmt.Errorf("http service %q raw tool requires at least one path", serviceName)
	}
	for _, pattern := range raw.Paths {
		if !validHTTPPathPattern(pattern) {
			return fmt.Errorf("http service %q raw tool has invalid path pattern %q", serviceName, pattern)
		}
	}
	for _, header := range raw.RequestHeaders {
		if !validHTTPHeaderName(header) {
			return fmt.Errorf("http service %q raw tool has invalid header %q", serviceName, header)
		}
		if isProtectedHTTPHeader(header) {
			return fmt.Errorf("http service %q raw tool cannot expose protected header %q", serviceName, header)
		}
	}
	return nil
}

func validateHTTPTool(serviceName string, tool HTTPTool) error {
	if !validHTTPMethod(tool.Request.Method) {
		return fmt.Errorf("http service %q tool %q has unsupported method %q", serviceName, tool.Name, tool.Request.Method)
	}
	if !validHTTPToolPath(tool.Request.Path) {
		return fmt.Errorf("http service %q tool %q has invalid path %q", serviceName, tool.Name, tool.Request.Path)
	}
	for inputName, input := range tool.Inputs {
		if !httpTemplateArgumentPattern.MatchString(inputName) {
			return fmt.Errorf("http service %q tool %q has invalid input name %q", serviceName, tool.Name, inputName)
		}
		if err := validateHTTPInput(serviceName, tool.Name, inputName, input); err != nil {
			return err
		}
	}
	for _, segment := range strings.Split(tool.Request.Path, "/") {
		if !strings.Contains(segment, "{") && !strings.Contains(segment, "}") {
			continue
		}
		match := httpFormatArgumentPattern.FindStringSubmatch(segment)
		if len(match) != 2 || match[0] != segment {
			return fmt.Errorf("http service %q tool %q path placeholders must occupy a full segment", serviceName, tool.Name)
		}
		input, ok := tool.Inputs[match[1]]
		if !ok {
			return fmt.Errorf("http service %q tool %q path references unknown input %q", serviceName, tool.Name, match[1])
		}
		if !input.Required {
			return fmt.Errorf("http service %q tool %q path input %q must be required", serviceName, tool.Name, match[1])
		}
		if input.Type != "string" && input.Type != "integer" {
			return fmt.Errorf("http service %q tool %q path input %q must be string or integer", serviceName, tool.Name, match[1])
		}
	}
	for name, template := range tool.Request.Query {
		if err := validateHTTPTemplate(serviceName, tool.Name, "query."+name, template, tool.Inputs); err != nil {
			return err
		}
	}
	for name, template := range tool.Request.Headers {
		if !validHTTPHeaderName(name) {
			return fmt.Errorf("http service %q tool %q has invalid header %q", serviceName, tool.Name, name)
		}
		if isProtectedHTTPHeader(name) {
			return fmt.Errorf("http service %q tool %q cannot template protected header %q", serviceName, tool.Name, name)
		}
		if err := validateHTTPTemplate(serviceName, tool.Name, "headers."+name, template, tool.Inputs); err != nil {
			return err
		}
	}
	if tool.Request.Body != nil {
		if !supportedHTTPBodyContentType(tool.Request.Body.ContentType) {
			return fmt.Errorf(
				"http service %q tool %q has unsupported body content type %q",
				serviceName,
				tool.Name,
				tool.Request.Body.ContentType,
			)
		}
		if err := validateHTTPTemplate(serviceName, tool.Name, "body", tool.Request.Body.Template, tool.Inputs); err != nil {
			return err
		}
	}
	return nil
}

func validateHTTPInput(serviceName string, toolName string, inputName string, input HTTPInput) error {
	switch input.Type {
	case "", "string", "integer", "number", "boolean", "object", "array":
	default:
		return fmt.Errorf("http service %q tool %q input %q has unsupported type %q", serviceName, toolName, inputName, input.Type)
	}
	if input.Type == "array" && input.Items != nil {
		if err := validateHTTPInput(serviceName, toolName, inputName+"[]", *input.Items); err != nil {
			return err
		}
	} else if input.Items != nil {
		return fmt.Errorf("http service %q tool %q input %q items require type array", serviceName, toolName, inputName)
	}
	if input.Minimum != nil && input.Maximum != nil && *input.Minimum > *input.Maximum {
		return fmt.Errorf("http service %q tool %q input %q minimum exceeds maximum", serviceName, toolName, inputName)
	}
	if (input.Minimum != nil || input.Maximum != nil) && input.Type != "integer" && input.Type != "number" {
		return fmt.Errorf("http service %q tool %q input %q bounds require a numeric type", serviceName, toolName, inputName)
	}
	if len(input.Enum) > 0 && input.Type != "" && input.Type != "string" {
		return fmt.Errorf("http service %q tool %q input %q enum requires type string", serviceName, toolName, inputName)
	}
	if input.Default != nil {
		if err := validateHTTPInputDefault(input, input.Default); err != nil {
			return fmt.Errorf("http service %q tool %q input %q has invalid default: %w", serviceName, toolName, inputName, err)
		}
	}
	return nil
}

func validateHTTPInputDefault(input HTTPInput, value interface{}) error {
	valid := true
	switch input.Type {
	case "":
	case "string":
		_, valid = value.(string)
	case "integer":
		valid = isHTTPInteger(value)
	case "number":
		_, valid = httpNumericValue(value)
	case "boolean":
		_, valid = value.(bool)
	case "object":
		_, valid = value.(map[string]interface{})
	case "array":
		array := reflect.ValueOf(value)
		valid = array.IsValid() && (array.Kind() == reflect.Array || array.Kind() == reflect.Slice)
		if valid && input.Items != nil {
			for index := 0; index < array.Len(); index++ {
				if err := validateHTTPInputDefault(*input.Items, array.Index(index).Interface()); err != nil {
					return fmt.Errorf("item %d: %w", index, err)
				}
			}
		}
	}
	if !valid {
		return fmt.Errorf("must be %s", input.Type)
	}
	if len(input.Enum) > 0 {
		text, ok := value.(string)
		if !ok || !containsHTTPString(input.Enum, text) {
			return fmt.Errorf("must be one of %s", strings.Join(input.Enum, ", "))
		}
	}
	if number, ok := httpNumericValue(value); ok {
		if input.Minimum != nil && number < *input.Minimum {
			return fmt.Errorf("must be at least %v", *input.Minimum)
		}
		if input.Maximum != nil && number > *input.Maximum {
			return fmt.Errorf("must be at most %v", *input.Maximum)
		}
	}
	return nil
}

func httpNumericValue(value interface{}) (float64, bool) {
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

func isHTTPInteger(value interface{}) bool {
	number, ok := httpNumericValue(value)
	return ok && number == float64(int64(number))
}

func containsHTTPString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateHTTPTemplate(serviceName string, toolName string, location string, value interface{}, inputs map[string]HTTPInput) error {
	return validateHTTPTemplateDepth(serviceName, toolName, location, value, inputs, 0)
}

func validateHTTPTemplateDepth(
	serviceName string,
	toolName string,
	location string,
	value interface{},
	inputs map[string]HTTPInput,
	depth int,
) error {
	if depth > maxHTTPTemplateDepth {
		return fmt.Errorf(
			"http service %q tool %q %s exceeds %d template levels",
			serviceName,
			toolName,
			location,
			maxHTTPTemplateDepth,
		)
	}
	switch item := value.(type) {
	case map[string]interface{}:
		if argument, marker := item["$arg"]; marker {
			name, ok := argument.(string)
			if !ok || !httpTemplateArgumentPattern.MatchString(name) {
				return fmt.Errorf("http service %q tool %q %s has invalid $arg", serviceName, toolName, location)
			}
			input, ok := inputs[name]
			if !ok {
				return fmt.Errorf("http service %q tool %q %s references unknown input %q", serviceName, toolName, location, name)
			}
			_, hasFallback := item["$default"]
			omit, _ := item["$omit_if_missing"].(bool)
			if !input.Required && input.Default == nil && !hasFallback && !omit {
				return fmt.Errorf(
					"http service %q tool %q %s uses optional input %q without $default or $omit_if_missing",
					serviceName,
					toolName,
					location,
					name,
				)
			}
			for key, option := range item {
				switch key {
				case "$arg":
				case "$default":
					if err := validateHTTPTemplateDepth(
						serviceName,
						toolName,
						location+".$default",
						option,
						inputs,
						depth+1,
					); err != nil {
						return err
					}
				case "$omit_if_missing":
					if _, ok := option.(bool); !ok {
						return fmt.Errorf("http service %q tool %q %s $omit_if_missing must be boolean", serviceName, toolName, location)
					}
				default:
					return fmt.Errorf("http service %q tool %q %s has unsupported $arg option %q", serviceName, toolName, location, key)
				}
			}
			return nil
		}
		if format, marker := item["$format"]; marker {
			text, ok := format.(string)
			if !ok {
				return fmt.Errorf("http service %q tool %q %s $format must be a string", serviceName, toolName, location)
			}
			for _, match := range httpFormatArgumentPattern.FindAllStringSubmatch(text, -1) {
				input, ok := inputs[match[1]]
				if !ok {
					return fmt.Errorf("http service %q tool %q %s format references unknown input %q", serviceName, toolName, location, match[1])
				}
				if !input.Required && input.Default == nil {
					return fmt.Errorf(
						"http service %q tool %q %s format uses optional input %q without a default",
						serviceName,
						toolName,
						location,
						match[1],
					)
				}
			}
			if len(item) != 1 {
				return fmt.Errorf("http service %q tool %q %s $format cannot have sibling keys", serviceName, toolName, location)
			}
			return nil
		}
		for key, child := range item {
			if err := validateHTTPTemplateDepth(
				serviceName,
				toolName,
				location+"."+key,
				child,
				inputs,
				depth+1,
			); err != nil {
				return err
			}
		}
	case []interface{}:
		for index, child := range item {
			if err := validateHTTPTemplateDepth(
				serviceName,
				toolName,
				fmt.Sprintf("%s[%d]", location, index),
				child,
				inputs,
				depth+1,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func validHTTPMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

func isProtectedHTTPHeader(name string) bool {
	switch http.CanonicalHeaderKey(strings.TrimSpace(name)) {
	case "Authorization", "Proxy-Authorization", "Host", "Content-Length", "Connection", "Transfer-Encoding":
		return true
	default:
		return false
	}
}

func isSensitiveHTTPResponseHeader(name string) bool {
	switch http.CanonicalHeaderKey(strings.TrimSpace(name)) {
	case "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "Proxy-Authenticate":
		return true
	default:
		return false
	}
}

func isUnsafeConfiguredHTTPHeader(name string) bool {
	switch http.CanonicalHeaderKey(strings.TrimSpace(name)) {
	case "Proxy-Authorization", "Host", "Content-Length", "Connection", "Transfer-Encoding":
		return true
	default:
		return false
	}
}

func validHTTPHeaderName(name string) bool {
	return httpHeaderNamePattern.MatchString(strings.TrimSpace(name))
}

func validHTTPPathPattern(pattern string) bool {
	if !strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "//") || strings.ContainsAny(pattern, "?#\\") {
		return false
	}
	for _, segment := range strings.Split(pattern, "/") {
		if pathSegmentTraverses(segment) {
			return false
		}
		if strings.Contains(segment, "*") && segment != "*" && segment != "**" {
			return false
		}
	}
	return true
}

func validHTTPToolPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "?#\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || strings.ContainsAny(decoded, "/\\") || pathSegmentTraverses(segment) {
			return false
		}
	}
	return true
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

func supportedHTTPBodyContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" ||
		strings.HasSuffix(mediaType, "+json") ||
		strings.HasPrefix(mediaType, "text/")
}

func validHTTPContentTypePattern(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "/*") {
		if strings.Contains(value, ";") {
			return false
		}
		value = strings.TrimSuffix(value, "*") + "plain"
	}
	_, _, err := mime.ParseMediaType(value)
	return err == nil
}
