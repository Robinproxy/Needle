package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTCPingCurrentTracksLatestReportAndRetainsHistory(t *testing.T) {
	h, store := newTestHandler(t)
	now := h.now()
	reportedAt := now.Unix()
	h.now = func() time.Time { return now }
	store.now = h.now
	if err := store.AllowToken("ping-token"); err != nil {
		t.Fatal(err)
	}
	check := func(enabled bool, names ...string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("agents: %d %s", w.Code, w.Body.String())
		}
		var rows []struct {
			Agent   AgentRow    `json:"agent"`
			Enabled bool        `json:"tcpping_enabled"`
			Pings   []TCPingRow `json:"latest_tcpping"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Enabled != enabled || len(rows[0].Pings) != len(names) {
			t.Fatalf("current: %s", w.Body.String())
		}
		for i, name := range names {
			if rows[0].Pings[i].Name != name {
				t.Fatalf("current: %+v", rows[0].Pings)
			}
		}
	}
	// Keep the same second for all changes: old and new history must not mix.
	reports := []struct {
		body    string
		enabled bool
		names   []string
	}{
		{`{"hostname":"node-1","tcpping":[{"name":"CMv4","target":"v4.example:80","success":true,"latency_ms":10},{"name":"CMv6","target":"v6.example:80","success":true,"latency_ms":20}]}`, true, []string{"CMv4", "CMv6"}},
		{`{"hostname":"node-1","tcpping":[{"name":"CMv6","target":"new-v6.example:443","success":false}]}`, true, []string{"CMv6"}},
		{`{"hostname":"node-1","tcpping":[]}`, false, nil},
		// Older Agents omit the field when disabled. This must also clear current results.
		{`{"hostname":"node-1"}`, false, nil},
		{`{"hostname":"node-1","tcpping":[{"name":"VPS","target":"example.com:443","success":true,"latency_ms":5}]}`, true, []string{"VPS"}},
	}
	for i, r := range reports {
		if i > 0 && i%2 == 0 {
			now = now.Add(time.Second)
		}
		body := strings.Replace(r.body, "{", fmt.Sprintf(`{"created_at":%d,`, reportedAt), 1)
		if w := sendReport(t, h, "Bearer ping-token", body); w.Code != http.StatusOK {
			t.Fatalf("report: %d %s", w.Code, w.Body.String())
		}
		check(r.enabled, r.names...)
	}
	id, _, err := store.AgentIDByHostname("node-1")
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.GetTCPingResultsWindowSampled(id, h.now().Add(-time.Hour).Unix(), 0, 0)
	if err != nil || len(history) != 4 {
		t.Fatalf("history: %+v, %v", history, err)
	}
	if err := store.DeleteAgent(id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM agent_tcpping_current WHERE agent_id = ?", id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("snapshot not deleted: %d %v", count, err)
	}
	reused, err := store.UpsertAgent("new-node", "CN", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.GetLatestTCPing(reused)
	if err != nil || len(latest) != 0 {
		t.Fatalf("reused node inherits results: %+v %v", latest, err)
	}
}

func TestTCPingCurrentPersistsDisabledStateAndLegacyFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "needle.db")
	store, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	id, err := store.UpsertAgent("legacy", "CN", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertTCPing(&TCPingRow{AgentID: id, Name: "CMv4", Target: "legacy.example:80", Success: true}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	latest, err := store.GetLatestTCPing(id)
	if err != nil || len(latest) != 1 {
		t.Fatalf("legacy fallback: %+v %v", latest, err)
	}
	if _, err := store.SaveReport(&ReportWrite{Hostname: "legacy", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	latest, err = store.GetLatestTCPing(id)
	if err != nil || len(latest) != 0 {
		t.Fatalf("disabled state lost on restart: %+v %v", latest, err)
	}
}

func TestTCPingSnapshotWriteFailureRollsBackWholeReport(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("ping-token"); err != nil {
		t.Fatal(err)
	}
	if w := sendReport(t, h, "Bearer ping-token", `{"hostname":"node-1","cpu":{"percent":10},"tcpping":[{"name":"CMv4","target":"example.com:80","success":true}]}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_current_update BEFORE UPDATE ON agent_tcpping_current BEGIN SELECT RAISE(ABORT, 'forced failure'); END`); err != nil {
		t.Fatal(err)
	}
	w := sendReport(t, h, "Bearer ping-token", `{"hostname":"node-1","cpu":{"percent":50},"tcpping":[{"name":"CMv6","target":"example.com:443","success":true}]}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected rollback: %d %s", w.Code, w.Body.String())
	}
	id, _, _ := store.AgentIDByHostname("node-1")
	latest, err := store.GetLatestTCPing(id)
	if err != nil || len(latest) != 1 || latest[0].Name != "CMv4" {
		t.Fatalf("snapshot changed: %+v %v", latest, err)
	}
	metric, err := store.GetLatestMetric(id)
	if err != nil || metric.CPUUsage != 10 {
		t.Fatalf("metric changed: %+v %v", metric, err)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM tcpping_results WHERE agent_id = ?", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial history write: %d %v", count, err)
	}
}

func TestTCPingDuplicateNamesRejectedBeforeBinding(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("ping-token"); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"hostname":"node-1","tcpping":[{"name":%q,"target":"one.example:80"},{"name":%q,"target":"two.example:80"}]}`, "CMv4", " CMv4 ")
	if w := sendReport(t, h, "Bearer ping-token", body); w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate accepted: %d %s", w.Code, w.Body.String())
	}
	token, err := store.LookupToken("ping-token")
	if err != nil || token.Hostname != "" {
		t.Fatalf("invalid report bound token: %+v %v", token, err)
	}
}
