package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mcpshim/mcpshim/internal/protocol"
	mcpclient "github.com/mark3labs/mcp-go/client"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) initSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS call_history (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	at_utc TEXT NOT NULL,
	server TEXT NOT NULL,
	tool TEXT NOT NULL,
	args_json TEXT,
	success INTEGER NOT NULL,
	error TEXT,
	duration_ms INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_call_history_at ON call_history(at_utc, id);
CREATE INDEX IF NOT EXISTS idx_call_history_server_at ON call_history(server, at_utc, id);
CREATE INDEX IF NOT EXISTS idx_call_history_server_tool_at ON call_history(server, tool, at_utc, id);

CREATE TABLE IF NOT EXISTS oauth_tokens (
	server TEXT PRIMARY KEY,
	token_json TEXT NOT NULL,
	updated_at_utc TEXT NOT NULL
);
`)
	if err != nil {
		return fmt.Errorf("init sqlite schema: %w", err)
	}
	return nil
}

func (s *Store) InsertHistory(item protocol.HistoryItem) error {
	var argsJSON string
	if len(item.Args) > 0 {
		data, err := json.Marshal(item.Args)
		if err != nil {
			return fmt.Errorf("marshal history args: %w", err)
		}
		argsJSON = string(data)
	}

	_, err := s.db.Exec(`
INSERT INTO call_history (at_utc, server, tool, args_json, success, error, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		item.At.UTC().Format(time.RFC3339Nano),
		item.Server,
		item.Tool,
		argsJSON,
		boolToInt(item.Success),
		item.Error,
		item.DurationMs,
	)
	if err != nil {
		return fmt.Errorf("insert history: %w", err)
	}
	return nil
}

func (s *Store) ListHistory(serverFilter string, toolFilter string, limit int) ([]protocol.HistoryItem, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	query := `SELECT at_utc, server, tool, args_json, success, error, duration_ms FROM call_history`
	args := make([]any, 0, 3)
	where := ""
	if serverFilter != "" {
		where += " server = ?"
		args = append(args, serverFilter)
	}
	if toolFilter != "" {
		if where != "" {
			where += " AND"
		}
		where += " tool = ?"
		args = append(args, toolFilter)
	}
	if where != "" {
		query += " WHERE" + where
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	defer rows.Close()

	out := make([]protocol.HistoryItem, 0, limit)
	for rows.Next() {
		var atUTC string
		var server string
		var tool string
		var argsJSON string
		var success int
		var errText sql.NullString
		var durationMs int64
		if err := rows.Scan(&atUTC, &server, &tool, &argsJSON, &success, &errText, &durationMs); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, atUTC)
		if err != nil {
			at = time.Now().UTC()
		}
		item := protocol.HistoryItem{
			At:         at,
			Server:     server,
			Tool:       tool,
			Success:    success == 1,
			DurationMs: durationMs,
		}
		if errText.Valid {
			item.Error = errText.String
		}
		if argsJSON != "" {
			argsMap := map[string]interface{}{}
			if err := json.Unmarshal([]byte(argsJSON), &argsMap); err == nil {
				item.Args = argsMap
			}
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history: %w", err)
	}

	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}

	return out, nil
}

func (s *Store) GetToken(server string) (*mcpclient.Token, error) {
	var tokenJSON string
	err := s.db.QueryRow(`SELECT token_json FROM oauth_tokens WHERE server = ?`, server).Scan(&tokenJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get token: %w", err)
	}
	var token mcpclient.Token
	if err := json.Unmarshal([]byte(tokenJSON), &token); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	if token.AccessToken == "" {
		return nil, nil
	}
	return &token, nil
}

func (s *Store) SaveToken(server string, token *mcpclient.Token) error {
	if token == nil {
		return fmt.Errorf("token is required")
	}
	data, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	_, err = s.db.Exec(`
INSERT INTO oauth_tokens (server, token_json, updated_at_utc)
VALUES (?, ?, ?)
ON CONFLICT(server) DO UPDATE SET token_json=excluded.token_json, updated_at_utc=excluded.updated_at_utc
`, server, string(data), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
