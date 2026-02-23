package mcp

import (
	"context"
	"fmt"

	"github.com/mcpshim/mcpshim/internal/store"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
)

type sqliteTokenStore struct {
	store      *store.Store
	serverName string
}

func newSQLiteTokenStore(dbStore *store.Store, serverName string) transport.TokenStore {
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