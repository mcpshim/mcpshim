package client

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
)

func TestHeaderArgs(t *testing.T) {
	var headers headerArgs
	if err := headers.Set("Authorization=Bearer token"); err != nil {
		t.Fatal(err)
	}
	if err := headers.Set("X-Test = value=with=equals"); err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "Bearer token" || headers["X-Test"] != "value=with=equals" {
		t.Fatalf("headers = %#v", headers)
	}
	if err := headers.Set("missing-separator"); err == nil {
		t.Fatal("invalid header succeeded")
	}
	if err := headers.Set(" =value"); err == nil {
		t.Fatal("empty header name succeeded")
	}
}

func TestSanitizeAliasName(t *testing.T) {
	cases := map[string]string{
		"notion":         "notion",
		"my-server":      "my_server",
		"my server!!":    "my_server",
		"  __x__  ":      "x",
		"123-notion-api": "s_123_notion_api",
		"!!!":            "",
	}

	for input, want := range cases {
		got := sanitizeAliasName(input)
		if got != want {
			t.Fatalf("sanitizeAliasName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseDynamicArgsSupportsStructuredAndNegativeValues(t *testing.T) {
	got := parseDynamicArgs([]string{
		"--offset", "-2",
		"--ratio=-1.5",
		"--filter", `{"status":"open"}`,
		"--ids", `[1,2,3]`,
		"--nothing", "null",
		"--enabled", "false",
		"--verbose",
	})
	want := map[string]interface{}{
		"offset":  int64(-2),
		"ratio":   -1.5,
		"filter":  map[string]interface{}{"status": "open"},
		"ids":     []interface{}{float64(1), float64(2), float64(3)},
		"nothing": nil,
		"enabled": false,
		"verbose": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDynamicArgs() = %#v, want %#v", got, want)
	}
}

func TestParseDynamicArgsUsesToolSchemaForStringValues(t *testing.T) {
	got := parseDynamicArgsForProperties(
		[]string{"--account", "00123", "--limit", "10", "--active", "false"},
		[]protocol.PropertyDetail{
			{Name: "account", Type: "string"},
			{Name: "limit", Type: "integer"},
			{Name: "active", Type: "boolean"},
		},
	)
	want := map[string]interface{}{
		"account": "00123",
		"limit":   int64(10),
		"active":  false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDynamicArgsForProperties() = %#v, want %#v", got, want)
	}
}

func TestParseCallArgsPreservesReservedToolArgumentsAfterSeparator(t *testing.T) {
	server, tool, rest, help, parseJSON, err := parseCallArgs([]string{
		"--server", "example",
		"--tool=search",
		"--json",
		"--",
		"--server", "nested",
		"--help",
	})
	if err != nil {
		t.Fatal(err)
	}
	if server != "example" || tool != "search" || !parseJSON || help {
		t.Fatalf("unexpected parsed call: server=%q tool=%q help=%v json=%v", server, tool, help, parseJSON)
	}
	wantRest := []string{"--server", "nested", "--help"}
	if !reflect.DeepEqual(rest, wantRest) {
		t.Fatalf("rest = %#v, want %#v", rest, wantRest)
	}
}

func TestParseCallArgsRejectsMissingSelectorValues(t *testing.T) {
	for _, args := range [][]string{{"--server"}, {"--tool"}} {
		if _, _, _, _, _, err := parseCallArgs(args); err == nil {
			t.Fatalf("parseCallArgs(%v) succeeded", args)
		}
	}
}

func TestParseJSONLikeContentText(t *testing.T) {
	input := map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": `{"ok":true}`},
			map[string]interface{}{"type": "text", "text": "plain text"},
		},
	}
	got := parseJSONLikeContentText(input).(map[string]interface{})
	content := got["content"].([]interface{})
	parsed := content[0].(map[string]interface{})["text"].(map[string]interface{})
	if parsed["ok"] != true {
		t.Fatalf("parsed content = %#v", parsed)
	}
	if content[1].(map[string]interface{})["text"] != "plain text" {
		t.Fatalf("plain text was changed: %#v", content[1])
	}
}

func TestBuildAliasTargetsDeduplicates(t *testing.T) {
	items := []protocol.ServerInfo{
		{Name: "notion-main", Alias: "notion-main"},
		{Name: "notion_alt", Alias: "notion main"},
		{Name: "notion_3", Alias: "notion_main"},
		{Name: "other", Alias: "!!!"},
	}

	targets := buildAliasTargets(items)
	if len(targets) != 3 {
		t.Fatalf("expected 3 alias targets, got %d", len(targets))
	}

	if targets[0].Sanitized != "notion_main" {
		t.Fatalf("unexpected first alias: %q", targets[0].Sanitized)
	}
	if targets[1].Sanitized != "notion_main_2" {
		t.Fatalf("unexpected second alias: %q", targets[1].Sanitized)
	}
	if targets[2].Sanitized != "notion_main_3" {
		t.Fatalf("unexpected third alias: %q", targets[2].Sanitized)
	}
}

func TestInstallAliasScripts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	items := []protocol.ServerInfo{{Name: "server-name", Alias: "my server"}}
	if err := installAliasScripts(dir, items); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "my_server")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "'server-name'") {
		t.Fatalf("wrapper does not target server:\n%s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("wrapper mode = %o, want 755", info.Mode().Perm())
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's"); got != "'it'\\''s'" {
		t.Fatalf("shellQuote() = %q", got)
	}
	if got := shellQuote(""); got != "''" {
		t.Fatalf("shellQuote(empty) = %q", got)
	}
}

func TestDescriptionFormatting(t *testing.T) {
	input := "\n\n# Search things\n\n<examples>\n{\"query\":\"x\"}\n"
	if got := summarizeDescription(input); got != "Search things" {
		t.Fatalf("summary = %q", got)
	}
	if got := normalizeMultiline(" one \r\n\r\n\r\n two "); got != "one\n\ntwo" {
		t.Fatalf("normalized = %q", got)
	}
	lines := splitNonEmptyLines(" first \n\n second ")
	if !reflect.DeepEqual(lines, []string{"first", "second"}) {
		t.Fatalf("lines = %#v", lines)
	}
}

func TestFallbackSocketPathReadsConfigOverride(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tempDir, "run"))

	override := filepath.Join(tempDir, "custom.sock")
	cfg := &config.Config{Server: config.ServerConfig{SocketPath: override}}
	if err := config.Save(config.DefaultConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := fallbackSocketPath(config.DefaultSocketPath()); got != override {
		t.Fatalf("fallback = %q, want %q", got, override)
	}
	if got := fallbackSocketPath("/explicit.sock"); got != "" {
		t.Fatalf("explicit socket fallback = %q", got)
	}
}

func TestCallRoundTrip(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "mcpshim.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer conn.Close()
		var request protocol.Request
		if decodeErr := json.NewDecoder(bufio.NewReader(conn)).Decode(&request); decodeErr != nil {
			done <- decodeErr
			return
		}
		done <- json.NewEncoder(conn).Encode(protocol.Response{OK: true, Text: request.Action})
	}()

	response, err := call(protocol.Request{Action: "status"}, socket)
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Text != "status" {
		t.Fatalf("response = %#v", response)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("socket server did not finish")
	}
}
