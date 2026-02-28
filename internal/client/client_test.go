package client

import (
	"testing"

	"github.com/mcpshim/mcpshim/internal/protocol"
)

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
