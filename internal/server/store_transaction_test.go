package server

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type seededAgent struct {
	id    int64
	token string
}

func newTransactionTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStoreCLI(filepath.Join(t.TempDir(), "needle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func seedAgentForDelete(t *testing.T, store *Store) seededAgent {
	t.Helper()
	token := "transaction-test-token"
	if err := store.AllowToken(token); err != nil {
		t.Fatal(err)
	}
	if err := store.BindToken(token, "node-1"); err != nil {
		t.Fatal(err)
	}
	id, err := store.UpsertAgent("node-1", token, "SG", nil, "1m")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertMetric(&MetricRow{AgentID: id, CPUUsage: 10}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertTCPing(&TCPingRow{AgentID: id, Name: "CMv4", Target: "example.com:80", Success: true}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	return seededAgent{id: id, token: token}
}

func assertAgentDataCounts(t *testing.T, store *Store, agentID int64, agents, metrics, pings, tokens int) {
	t.Helper()
	checks := []struct {
		query string
		args  []any
		want  int
	}{
		{query: `SELECT COUNT(*) FROM agents WHERE id = ?`, args: []any{agentID}, want: agents},
		{query: `SELECT COUNT(*) FROM metrics WHERE agent_id = ?`, args: []any{agentID}, want: metrics},
		{query: `SELECT COUNT(*) FROM tcpping_results WHERE agent_id = ?`, args: []any{agentID}, want: pings},
		{query: `SELECT COUNT(*) FROM agent_tokens`, want: tokens},
	}
	for _, check := range checks {
		var got int
		if err := store.db.QueryRow(check.query, check.args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Fatalf("query %q count = %d, want %d", check.query, got, check.want)
		}
	}
}

func TestDeleteAgentTransaction(t *testing.T) {
	store := newTransactionTestStore(t)
	seed := seedAgentForDelete(t, store)
	if err := store.DeleteAgent(seed.id); err != nil {
		t.Fatal(err)
	}
	assertAgentDataCounts(t, store, seed.id, 0, 0, 0, 0)
}

func TestDeleteAgentRollsBackOnFailure(t *testing.T) {
	store := newTransactionTestStore(t)
	seed := seedAgentForDelete(t, store)
	if _, err := store.db.Exec(`CREATE TRIGGER fail_agent_delete BEFORE DELETE ON agents BEGIN SELECT RAISE(ABORT, 'forced failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAgent(seed.id); err == nil {
		t.Fatal("DeleteAgent succeeded despite failing trigger")
	}
	assertAgentDataCounts(t, store, seed.id, 1, 1, 1, 1)
}

func TestRevokeTokenTransaction(t *testing.T) {
	store := newTransactionTestStore(t)
	seed := seedAgentForDelete(t, store)
	if err := store.RevokeToken(seed.token); err != nil {
		t.Fatal(err)
	}
	assertAgentDataCounts(t, store, seed.id, 0, 0, 0, 0)
}

func TestRevokeTokenRollsBackOnFailure(t *testing.T) {
	store := newTransactionTestStore(t)
	seed := seedAgentForDelete(t, store)
	if _, err := store.db.Exec(`CREATE TRIGGER fail_token_delete BEFORE DELETE ON agent_tokens BEGIN SELECT RAISE(ABORT, 'forced failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeToken(seed.token); err == nil {
		t.Fatal("RevokeToken succeeded despite failing trigger")
	}
	assertAgentDataCounts(t, store, seed.id, 1, 1, 1, 1)
}

func TestRevokeUnboundToken(t *testing.T) {
	store := newTransactionTestStore(t)
	if err := store.AllowToken("unbound-token"); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeToken("unbound-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupToken("unbound-token"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("LookupToken error = %v, want ErrTokenNotFound", err)
	}
}
