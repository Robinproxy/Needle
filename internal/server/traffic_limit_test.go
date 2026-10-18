package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestTrafficAllowancePersistsWithoutResettingUsage(t *testing.T) {
	h, store := newTestHandler(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	store.now = h.now
	if err := store.AllowToken("quota-test"); err != nil {
		t.Fatal(err)
	}
	uptime := int64(10000)
	send := func(limit, sent, recv int64) {
		t.Helper()
		body := fmt.Sprintf(`{"hostname":"quota-node","billing_period":"forever","monthly_traffic_limit":%d,"uptime":%d,"cpu":{"percent":10},"network":{"total_sent":%d,"total_recv":%d}}`, limit, uptime, sent, recv)
		if w := sendReport(t, h, "Bearer quota-test", body); w.Code != http.StatusOK {
			t.Fatalf("report: %d %s", w.Code, w.Body.String())
		}
		now = now.Add(30 * time.Second)
	}
	send(1_000_000_000_000, 100, 200)
	send(1_000_000_000_000, 300, 700)
	rows, err := store.GetAgents()
	if err != nil {
		t.Fatal(err)
	}
	id := rows[0].ID
	assertUsage(t, store, id, 200, 500)
	send(2_000_000_000_000, 400, 900)
	assertUsage(t, store, id, 300, 700)
	w := httptest.NewRecorder()
	h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var api []struct {
		Agent   AgentRow     `json:"agent"`
		Traffic TrafficUsage `json:"traffic"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &api); err != nil {
		t.Fatal(err)
	}
	if len(api) != 1 || api[0].Agent.MonthlyTrafficLimit != 2_000_000_000_000 || api[0].Traffic.Sent+api[0].Traffic.Recv != 1000 {
		t.Fatalf("API: %s", w.Body.String())
	}
	// An invalid allowance cannot replace the persisted value.
	bad := `{"hostname":"quota-node","monthly_traffic_limit":-1}`
	if w := sendReport(t, h, "Bearer quota-test", bad); w.Code != http.StatusBadRequest {
		t.Fatalf("bad quota: %d", w.Code)
	}
	rows, _ = store.GetAgents()
	if rows[0].MonthlyTrafficLimit != 2_000_000_000_000 {
		t.Fatal("invalid report changed allowance")
	}
	// Counter reset and allowance removal preserve this month's accumulated usage.
	uptime = 10
	send(0, 10, 20)
	assertUsage(t, store, id, 310, 720)
	rows, _ = store.GetAgents()
	if rows[0].MonthlyTrafficLimit != 0 {
		t.Fatal("quota removal failed")
	}
	now = time.Date(2026, 11, 1, 0, 0, 30, 0, time.UTC)
	send(1_000_000_000_000, 30, 50)
	assertUsage(t, store, id, 0, 0)
}

func TestTrafficAllowanceMigrationAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an existing installation with no quota column.
	_, err = db.Exec(`CREATE TABLE agents(id INTEGER PRIMARY KEY, hostname TEXT UNIQUE NOT NULL, region TEXT DEFAULT '', expires_at INTEGER, billing_period TEXT DEFAULT '', last_seen INTEGER, created_at INTEGER DEFAULT (strftime('%s','now')));
 INSERT INTO agents(hostname,billing_period) VALUES('legacy','forever');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.GetAgents()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].MonthlyTrafficLimit != 0 {
		t.Fatal("legacy node must default to no limit")
	}
	_, err = store.SaveReport(&ReportWrite{Hostname: "legacy", BillingPeriod: "forever", MonthlyTrafficLimit: 1e12, CPUCores: 4})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err = store.GetAgents()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].MonthlyTrafficLimit != 1e12 || rows[0].CPUCores != 4 {
		t.Fatal("migration on reopen lost the saved quota")
	}
}
