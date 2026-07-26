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
	id, err := store.UpsertAgent("node-1", "SG", nil, "1m")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertMetric(&MetricRow{AgentID: id, CPUUsage: 10}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertTCPing(&TCPingRow{AgentID: id, Name: "CMv4", Target: "example.com:80", Success: true}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	hour := time.Now().Truncate(time.Hour).Unix()
	if _, err := store.db.Exec(
		`INSERT INTO metrics_hourly(agent_id, hour_start, cpu_avg, sample_count) VALUES(?, ?, 1, 1)`, id, hour,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO tcpping_hourly(agent_id, name, hour_start, latency_avg, latency_peak, sample_count, success_count) VALUES(?, 'CMv4', ?, 1, 1, 1, 1)`, id, hour,
	); err != nil {
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
		{query: `SELECT COUNT(*) FROM metrics_hourly WHERE agent_id = ?`, args: []any{agentID}, want: metrics},
		{query: `SELECT COUNT(*) FROM tcpping_results WHERE agent_id = ?`, args: []any{agentID}, want: pings},
		{query: `SELECT COUNT(*) FROM tcpping_hourly WHERE agent_id = ?`, args: []any{agentID}, want: pings},
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

// Agent ids are plain max(id)+1 and get reused after a delete; the new owner
// of an id must not inherit the previous agent's hourly history.
func TestReusedAgentIDStartsClean(t *testing.T) {
	store := newTransactionTestStore(t)
	seed := seedAgentForDelete(t, store)
	if err := store.DeleteAgent(seed.id); err != nil {
		t.Fatal(err)
	}

	id, err := store.UpsertAgent("node-2", "US", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if id != seed.id {
		t.Fatalf("expected id reuse (%d), got %d", seed.id, id)
	}
	hourly, err := store.GetMetricsHourly(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hourly) != 0 {
		t.Fatalf("reused id inherited %d hourly rows", len(hourly))
	}
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
