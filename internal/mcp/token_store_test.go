package mcp

import (
	"strings"
	"testing"

	"github.com/mcpshim/mcpshim/internal/config"
)

func TestTokenStoreKeyBindsTokenToServerURL(t *testing.T) {
	one := TokenStoreKey(config.MCPServer{Name: "example", URL: "https://one.example.com/mcp"})
	same := TokenStoreKey(config.MCPServer{Name: "example", URL: "https://one.example.com/mcp"})
	two := TokenStoreKey(config.MCPServer{Name: "example", URL: "https://two.example.com/mcp"})

	if one != same {
		t.Fatalf("token key is not stable: %q != %q", one, same)
	}
	if one == two {
		t.Fatalf("different endpoints share token key %q", one)
	}
	if strings.Contains(one, "https://") {
		t.Fatalf("token key exposes endpoint: %q", one)
	}
}
