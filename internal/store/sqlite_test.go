package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mcpshim/mcpshim/internal/protocol"
)

func TestOpenCreatesPrivatePortableDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "mcpshim.db")
	dbStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode = %o, want 600", got)
	}
}

func TestHistoryRoundTripFiltersAndOrders(t *testing.T) {
	dbStore, err := Open(filepath.Join(t.TempDir(), "mcpshim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	items := []protocol.HistoryItem{
		{At: base, Server: "one", Tool: "search", Args: map[string]interface{}{"query": "a"}, Success: true, DurationMs: 10},
		{At: base.Add(time.Second), Server: "two", Tool: "create", Success: false, Error: "failed", DurationMs: 20},
		{At: base.Add(2 * time.Second), Server: "one", Tool: "search", Success: true, DurationMs: 30},
	}
	for _, item := range items {
		if err := dbStore.InsertHistory(item, 1000); err != nil {
			t.Fatal(err)
		}
	}

	got, err := dbStore.ListHistory("one", "search", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("history length = %d, want 2", len(got))
	}
	if !got[0].At.Equal(base) || !got[1].At.Equal(base.Add(2*time.Second)) {
		t.Fatalf("history order = %v, want chronological", got)
	}
	if got[0].Args["query"] != "a" {
		t.Fatalf("history args = %#v", got[0].Args)
	}

	latest, err := dbStore.ListHistory("", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].Server != "one" || latest[0].DurationMs != 30 {
		t.Fatalf("limited history = %#v, want latest inserted item", latest)
	}
}

func TestHistoryRetentionAndSafeClearing(t *testing.T) {
	dbStore, err := Open(filepath.Join(t.TempDir(), "mcpshim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	for i, item := range []protocol.HistoryItem{
		{At: time.Now(), Server: "one", Tool: "search", Success: true},
		{At: time.Now(), Server: "two", Tool: "search", Success: true},
		{At: time.Now(), Server: "two", Tool: "create", Success: true},
	} {
		if err := dbStore.InsertHistory(item, 2); err != nil {
			t.Fatalf("InsertHistory(%d): %v", i, err)
		}
	}

	items, err := dbStore.ListHistory("", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Server != "two" || items[0].Tool != "search" {
		t.Fatalf("retained history = %#v", items)
	}

	if _, err := dbStore.ClearHistory("", "", false); err == nil {
		t.Fatal("unscoped ClearHistory succeeded without --all")
	}
	if _, err := dbStore.ClearHistory("two", "", true); err == nil {
		t.Fatal("ClearHistory accepted both a filter and --all")
	}
	cleared, err := dbStore.ClearHistory("", "search", false)
	if err != nil {
		t.Fatal(err)
	}
	if cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
	cleared, err = dbStore.ClearHistory("", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if cleared != 1 {
		t.Fatalf("cleared all = %d, want 1", cleared)
	}
}

func TestConcurrentHistoryWritesRespectRetention(t *testing.T) {
	dbStore, err := Open(filepath.Join(t.TempDir(), "mcpshim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	const writes = 25
	const retention = 10
	var wg sync.WaitGroup
	errors := make(chan error, writes)
	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errors <- dbStore.InsertHistory(protocol.HistoryItem{
				At:      time.Now(),
				Server:  "example",
				Tool:    "search",
				Success: true,
			}, retention)
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}

	items, err := dbStore.ListHistory("", "", writes)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != retention {
		t.Fatalf("history length = %d, want %d", len(items), retention)
	}
}

func TestTokenRoundTrip(t *testing.T) {
	dbStore, err := Open(filepath.Join(t.TempDir(), "mcpshim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	if token, err := dbStore.GetToken("example"); err != nil || token != nil {
		t.Fatalf("missing token = %#v, %v", token, err)
	}
	if err := dbStore.SaveToken("example", nil); err == nil {
		t.Fatal("SaveToken(nil) succeeded, want error")
	}

	want := &mcpclient.Token{
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
	}
	if err := dbStore.SaveToken("example", want); err != nil {
		t.Fatal(err)
	}
	got, err := dbStore.GetToken("example")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Fatalf("token = %#v, want %#v", got, want)
	}

	if err := dbStore.DeleteTokens("example"); err != nil {
		t.Fatal(err)
	}
	if token, err := dbStore.GetToken("example"); err != nil || token != nil {
		t.Fatalf("deleted token = %#v, %v", token, err)
	}
}
