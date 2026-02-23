package protocol

import "time"

type Request struct {
	Action    string                 `json:"action"`
	Name      string                 `json:"name,omitempty"`
	Server    string                 `json:"server,omitempty"`
	Tool      string                 `json:"tool,omitempty"`
	Limit     int                    `json:"limit,omitempty"`
	Alias     string                 `json:"alias,omitempty"`
	URL       string                 `json:"url,omitempty"`
	Transport string                 `json:"transport,omitempty"`
	Headers   map[string]string      `json:"headers,omitempty"`
	Args      map[string]interface{} `json:"args,omitempty"`
}

type ServerInfo struct {
	Name      string `json:"name"`
	Alias     string `json:"alias,omitempty"`
	URL       string `json:"url"`
	Transport string `json:"transport"`
	HasAuth   bool   `json:"has_auth"`
}

type ToolInfo struct {
	Server      string   `json:"server"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Required    []string `json:"required,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

type PropertyDetail struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Const       string `json:"const,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
}

type ToolDetail struct {
	Server      string           `json:"server"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Properties  []PropertyDetail `json:"properties,omitempty"`
}

type Status struct {
	StartedAt   time.Time `json:"started_at"`
	UptimeSec   int64     `json:"uptime_sec"`
	ServerCount int       `json:"server_count"`
	ToolCount   int       `json:"tool_count"`
}

type HistoryItem struct {
	At         time.Time              `json:"at"`
	Server     string                 `json:"server"`
	Tool       string                 `json:"tool"`
	Args       map[string]interface{} `json:"args,omitempty"`
	Success    bool                   `json:"success"`
	Error      string                 `json:"error,omitempty"`
	DurationMs int64                  `json:"duration_ms"`
}

type Response struct {
	OK         bool         `json:"ok"`
	Error      string       `json:"error,omitempty"`
	Status     *Status      `json:"status,omitempty"`
	Servers    []ServerInfo `json:"servers,omitempty"`
	Tools      []ToolInfo   `json:"tools,omitempty"`
	History    []HistoryItem `json:"history,omitempty"`
	ToolDetail *ToolDetail  `json:"tool_detail,omitempty"`
	Result     interface{}  `json:"result,omitempty"`
	Text       string       `json:"text,omitempty"`
}
