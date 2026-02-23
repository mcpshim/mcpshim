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
	Server  ServerConfig `yaml:"server"`
	Servers []MCPServer  `yaml:"servers"`
}

type ServerConfig struct {
	SocketPath string `yaml:"socket_path"`
	DBPath     string `yaml:"db_path"`
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
	for i := range cfg.Servers {
		s := &cfg.Servers[i]
		s.URL = os.ExpandEnv(s.URL)
		if s.Headers != nil {
			for k, v := range s.Headers {
				s.Headers[k] = os.ExpandEnv(v)
			}
		}
		transport, transportErr := normalizeTransport(s.Transport)
		if transportErr != nil {
			return nil, transportErr
		}
		s.Transport = transport
		if s.Alias == "" {
			s.Alias = s.Name
		}
	}
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
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
			SocketPath: DefaultSocketPath(),
			DBPath:     DefaultDBPath(),
		},
		Servers: []MCPServer{},
	}
	if err := Save(path, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	seen := map[string]bool{}
	aliases := map[string]bool{}
	for _, s := range cfg.Servers {
		if s.Name == "" {
			return errors.New("server name is required")
		}
		if s.URL == "" {
			return fmt.Errorf("server %q url is required", s.Name)
		}
		if _, err := normalizeTransport(s.Transport); err != nil {
			return fmt.Errorf("server %q: %w", s.Name, err)
		}
		if seen[s.Name] {
			return fmt.Errorf("duplicate server name %q", s.Name)
		}
		seen[s.Name] = true
		alias := s.Alias
		if alias == "" {
			alias = s.Name
		}
		if aliases[alias] {
			return fmt.Errorf("duplicate alias %q", alias)
		}
		aliases[alias] = true
	}
	return nil
}

func UpsertServer(cfg *Config, item MCPServer) {
	transport, err := normalizeTransport(item.Transport)
	if err != nil {
		transport = "http"
	}
	item.Transport = transport
	if item.Alias == "" {
		item.Alias = item.Name
	}
	for i := range cfg.Servers {
		if cfg.Servers[i].Name == item.Name {
			cfg.Servers[i] = item
			return
		}
	}
	cfg.Servers = append(cfg.Servers, item)
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
