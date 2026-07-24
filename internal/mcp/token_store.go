package mcp

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/store"
)

// TokenStoreKey binds a stored OAuth token to both the logical server and its
// endpoint. Re-pointing a server name must never send an existing token to the
// replacement URL.
func TokenStoreKey(server config.MCPServer) string {
	resolved := config.ResolveServer(server)
	sum := sha256.Sum256([]byte(resolved.URL))
	return fmt.Sprintf("%s:%x", server.Name, sum)
}

type sqliteTokenStore struct {
	store      *store.Store
	serverName string
}

func newSQLiteTokenStore(dbStore *store.Store, serverName string) *sqliteTokenStore {
	return &sqliteTokenStore{store: dbStore, serverName: serverName}
}

func (s *sqliteTokenStore) GetToken(ctx context.Context) (*client.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, transport.ErrNoToken
	}
	token, err := s.store.GetToken(s.serverName)
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, transport.ErrNoToken
	}
	return token, nil
}

func (s *sqliteTokenStore) SaveToken(ctx context.Context, token *client.Token) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.store == nil {
		return fmt.Errorf("sqlite store is not available")
	}
	return s.store.SaveToken(s.serverName, token)
}

func (s *sqliteTokenStore) String() string {
	return fmt.Sprintf("sqliteTokenStore(%s)", s.serverName)
}
