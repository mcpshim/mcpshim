package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server       ServerConfig  `yaml:"server"`
	Servers      []MCPServer   `yaml:"servers"`
	HTTPServices []HTTPService `yaml:"http_services,omitempty"`
}

const DefaultHistorySize = 1000

type ServerConfig struct {
	SocketPath  string `yaml:"socket_path"`
	DBPath      string `yaml:"db_path"`
	HistorySize int    `yaml:"history_size"`

	// AllowRegistryWrites permits socket clients to edit the registry through
	// add_server, set_auth, and remove_server. It is off by default because
	// those actions persist to the config file, which makes socket access
	// equivalent to config write access. Operators can always edit the file
	// directly and reload.
	AllowRegistryWrites bool `yaml:"allow_registry_writes,omitempty"`
}

type MCPServer struct {
	Name      string            `yaml:"name"`
	Alias     string            `yaml:"alias,omitempty"`
	URL       string            `yaml:"url"`
	Transport string            `yaml:"transport,omitempty"`
	Headers   map[string]string `yaml:"headers,omitempty"`
}

func normalizeTransport(value string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "http", "streamable-http":
		return "http", nil
	case "sse":
		return "sse", nil
	default:
		return "", fmt.Errorf("unsupported transport %q (expected http or sse)", value)
	}
}

func DefaultConfigPath() string {
	if envPath := strings.TrimSpace(os.Getenv("MCPSHIM_CONFIG")); envPath != "" {
		return envPath
	}
	return filepath.Join(xdgConfigHome(), "mcpshim", "config.yaml")
}

func DefaultSocketPath() string {
	if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
		return filepath.Join(runtimeDir, "mcpshim.sock")
	}
	return fmt.Sprintf("/tmp/mcpshim-%d.sock", os.Getuid())
}

func DefaultDBPath() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, "mcpshim", "mcpshim.db")
	}
	return filepath.Join(homeDir(), ".local", "share", "mcpshim", "mcpshim.db")
}

func xdgConfigHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(), ".config")
}

func homeDir() string {
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return home
	}
	return "/tmp/mcpshim-" + strconv.Itoa(os.Getuid())
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if cfg.Server.SocketPath == "" {
		cfg.Server.SocketPath = DefaultSocketPath()
	}
	if cfg.Server.DBPath == "" {
		cfg.Server.DBPath = DefaultDBPath()
	}
	if cfg.Server.HistorySize == 0 {
		cfg.Server.HistorySize = DefaultHistorySize
	}
	for i := range cfg.Servers {
		s := &cfg.Servers[i]
		transport, transportErr := normalizeTransport(s.Transport)
		if transportErr != nil {
			return nil, transportErr
		}
		s.Transport = transport
		if s.Alias == "" {
			s.Alias = s.Name
		}
	}
	normalizeHTTPServices(cfg.HTTPServices)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ResolveServer expands environment references in a disposable server copy.
// Keeping the source config untouched prevents later CLI mutations from
// writing resolved credentials back to disk.
func ResolveServer(server MCPServer) MCPServer {
	server.URL = os.ExpandEnv(server.URL)
	if server.Headers != nil {
		headers := make(map[string]string, len(server.Headers))
		for key, value := range server.Headers {
			headers[key] = os.ExpandEnv(value)
		}
		server.Headers = headers
	}
	return server
}

func Clone(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	clone.Servers = make([]MCPServer, len(cfg.Servers))
	for i, server := range cfg.Servers {
		clone.Servers[i] = server
		if server.Headers != nil {
			clone.Servers[i].Headers = make(map[string]string, len(server.Headers))
			for key, value := range server.Headers {
				clone.Servers[i].Headers[key] = value
			}
		}
	}
	clone.HTTPServices = cloneHTTPServices(cfg.HTTPServices)
	return &clone
}

func Save(path string, cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if err := validate(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, out, 0o600); err != nil {
		return err
	}
	if _, err := Load(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("resulting config is invalid: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func LoadOrInit(path string) (*Config, error) {
	cfg, err := Load(path)
	if err == nil {
		return cfg, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	cfg = &Config{
		Server: ServerConfig{
			SocketPath:  DefaultSocketPath(),
			DBPath:      DefaultDBPath(),
			HistorySize: DefaultHistorySize,
		},
		Servers: []MCPServer{},
	}
	if err := Save(path, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if cfg.Server.HistorySize < 0 {
		return errors.New("server history_size cannot be negative")
	}
	seen := map[string]bool{}
	aliases := map[string]bool{}
	for _, s := range cfg.Servers {
		if s.Name == "" {
			return errors.New("server name is required")
		}
		resolvedURL, missingURLVar := expandEnvStrict(s.URL)
		if missingURLVar != "" {
			return fmt.Errorf("server %q url references unset environment variable %q", s.Name, missingURLVar)
		}
		if strings.TrimSpace(resolvedURL) == "" {
			return fmt.Errorf("server %q url is required", s.Name)
		}
		for header, value := range s.Headers {
			if _, missingHeaderVar := expandEnvStrict(value); missingHeaderVar != "" {
				return fmt.Errorf("server %q header %q references unset environment variable %q", s.Name, header, missingHeaderVar)
			}
		}
		if _, err := normalizeTransport(s.Transport); err != nil {
			return fmt.Errorf("server %q: %w", s.Name, err)
		}
		if seen[s.Name] || aliases[s.Name] {
			return fmt.Errorf("duplicate server identifier %q", s.Name)
		}
		alias := s.Alias
		if alias == "" {
			alias = s.Name
		}
		if alias != s.Name && (seen[alias] || aliases[alias]) {
			return fmt.Errorf("duplicate server identifier %q", alias)
		}
		seen[s.Name] = true
		aliases[alias] = true
	}
	return validateHTTPServices(cfg, seen, aliases)
}

func expandEnvStrict(value string) (string, string) {
	missing := ""
	expanded := os.Expand(value, func(name string) string {
		resolved, ok := os.LookupEnv(name)
		if !ok && missing == "" {
			missing = name
		}
		return resolved
	})
	return expanded, missing
}

func UpsertServer(cfg *Config, item MCPServer) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	transport, err := normalizeTransport(item.Transport)
	if err != nil {
		return err
	}
	item.Transport = transport
	if item.Alias == "" {
		item.Alias = item.Name
	}
	candidate := Clone(cfg)
	for i := range candidate.Servers {
		if candidate.Servers[i].Name == item.Name {
			candidate.Servers[i] = item
			if err := validate(candidate); err != nil {
				return err
			}
			*cfg = *candidate
			return nil
		}
	}
	candidate.Servers = append(candidate.Servers, item)
	if err := validate(candidate); err != nil {
		return err
	}
	*cfg = *candidate
	return nil
}

func RemoveServer(cfg *Config, name string) bool {
	for i := range cfg.Servers {
		if cfg.Servers[i].Name == name {
			cfg.Servers = append(cfg.Servers[:i], cfg.Servers[i+1:]...)
			return true
		}
	}
	return false
}
