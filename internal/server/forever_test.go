package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestForeverReportsAndMonthlyTraffic(t *testing.T) {
	h, store := newTestHandler(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	store.now = h.now
	if err := store.AllowToken("forever-token"); err != nil {
		t.Fatal(err)
	}
	report := func(sent, recv int64) {
		t.Helper()
		// A stale date from a previous finite configuration must be ignored.
		body := fmt.Sprintf(`{"hostname":"permanent","billing_period":"forever","expires_at":1,"cpu":{"percent":10},"network":{"total_sent":%d,"total_recv":%d}}`, sent, recv)
		w := sendReport(t, h, "Bearer forever-token", body)
		if w.Code != http.StatusOK {
			t.Fatalf("report: %d %s", w.Code, w.Body.String())
		}
	}
	report(100, 200)
	id, ok, err := store.AgentIDByHostname("permanent")
	if err != nil || !ok {
		t.Fatalf("agent lookup: %v, %v", ok, err)
	}
	assertUsage(t, store, id, 0, 0)
	now = now.Add(30 * time.Second)
	report(150, 280)
	assertUsage(t, store, id, 50, 80)
	now = now.Add(30 * time.Second)
	report(10, 20)
	assertUsage(t, store, id, 60, 100)
	now = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store.PurgeOldData()
	assertUsage(t, store, id, 60, 100)

	w := httptest.NewRecorder()
	h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var rows []struct {
		Agent   AgentRow     `json:"agent"`
		Days    int          `json:"expiry_days"`
		Date    string       `json:"expiry_date"`
		Traffic TrafficUsage `json:"traffic"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("agents: %s", w.Body.String())
	}
	row := rows[0]
	if row.Agent.BillingPeriod != "forever" || row.Agent.ExpiresAt != nil || row.Days != 0 || row.Date != "" || !row.Traffic.Available {
		t.Fatalf("permanent node: %+v", row)
	}
	now = time.Date(2026, 8, 1, 0, 0, 30, 0, time.UTC)
	report(30, 50)
	assertUsage(t, store, id, 0, 0)
	now = now.Add(30 * time.Second)
	report(40, 70)
	assertUsage(t, store, id, 10, 20)

	// Switching back to a finite period restores the configured renewal day.
	expiry := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	now = now.Add(30 * time.Second)
	body := fmt.Sprintf(`{"hostname":"permanent","billing_period":"1m","expires_at":%d,"cpu":{"percent":10},"network":{"total_sent":45,"total_recv":75}}`, expiry)
	if w := sendReport(t, h, "Bearer forever-token", body); w.Code != http.StatusOK {
		t.Fatalf("finite report: %d %s", w.Code, w.Body.String())
	}
	agents, err := store.GetAgents()
	if err != nil {
		t.Fatal(err)
	}
	if agents[0].ExpiresAt == nil || *agents[0].ExpiresAt != expiry || agents[0].BillingPeriod != "1m" {
		t.Fatalf("finite agent: %+v", agents[0])
	}
}
