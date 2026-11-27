package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTCPingSummaryWindowTargetsAndSuccessfulLatency(t *testing.T) {
	s := newTransactionTestStore(t)
	now := time.Unix(1_800_000_000, 0)
	current := []TCPingRow{{Name: "CMv4", Target: "current:80"}, {Name: "all-failed", Target: "fail:80"}, {Name: "empty", Target: "new:80"}}
	add := func(agent int64, name, target string, success bool, latency float64, offset int64) {
		t.Helper()
		if err := s.InsertTCPing(&TCPingRow{AgentID: agent, Name: name, Target: target, Success: success, LatencyMs: latency}, now.Unix()+offset); err != nil {
			t.Fatal(err)
		}
	}
	add(1, "CMv4", "current:80", true, 1000, -3601) // Outside the hour.
	add(1, "CMv4", "current:80", true, 0, -3600)    // Inclusive edge and valid zero RTT.
	add(1, "CMv4", "current:80", true, 20, -1800)
	add(1, "CMv4", "current:80", false, 9000, 0) // A failure must not dilute/inflate mean RTT.
	add(1, "CMv4", "current:80", true, 1000, 1)  // Future data is excluded.
	add(1, "CMv4", "old:80", true, 2000, -60)
	add(1, "removed", "current:80", true, 3000, -60)
	add(2, "CMv4", "current:80", true, 4000, -60)
	add(1, "all-failed", "fail:80", false, 0, -30)
	add(1, "all-failed", "fail:80", false, 0, -10)
	rows, err := s.GetTCPingSummary(1, current, now)
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	if rows[0].Name != "CMv4" || rows[0].Target != "current:80" || rows[0].SampleCount != 3 || rows[0].SuccessCount != 2 || rows[0].LatencyMs != 10 {
		t.Fatalf("wrong window/route/latency: %+v", rows[0])
	}
	if rows[1].SampleCount != 2 || rows[1].SuccessCount != 0 || rows[1].LatencyMs != 0 {
		t.Fatalf("all-failed route: %+v", rows[1])
	}
	if rows[2].Name != "empty" || rows[2].SampleCount != 0 || rows[2].SuccessCount != 0 {
		t.Fatalf("missing samples must remain distinct from success: %+v", rows[2])
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM tcpping_results").Scan(&count); err != nil || count != 10 {
		t.Fatal("summary altered raw history", count, err)
	}
	if _, err := s.db.Exec("DROP TABLE tcpping_results"); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.GetTCPingSummary(1, nil, now); err != nil || len(rows) != 0 {
		t.Fatal("disabled probes must not query history", rows, err)
	}
}

func TestTCPingSummaryPublishesWithLatestStatusAndAgesOut(t *testing.T) {
	h, s, now, id := realtimeFixture(t)
	client, _ := s.live.subscribe()
	defer s.live.unsubscribe(client)
	if _, err := h.snapshots(); err != nil { // Prime the committed cache.
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	w := sendReport(t, h, "Bearer live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d,"tcpping":[{"name":"CTv4","target":"127.0.0.1:80","success":false}]}`, now.Unix()))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case event := <-client:
		if event.Node == nil || len(event.Node.TCPingSummary) != 1 || event.Node.TCPingSummary[0].SampleCount != 2 || event.Node.TCPingSummary[0].SuccessCount != 1 || event.Node.TCPingSummary[0].LatencyMs != 10 || event.Node.LatestTCPing[0].Success {
			t.Fatalf("event must combine hourly statistics and current failure: %+v", event.Node)
		}
	default:
		t.Fatal("missing history event")
	}
	w = httptest.NewRecorder()
	h.handleAgents(w, httptest.NewRequest("GET", "/api/agents", nil))
	var nodes []agentWithMetric
	if err := json.Unmarshal(w.Body.Bytes(), &nodes); err != nil || w.Code != 200 || len(nodes) != 1 || len(nodes[0].TCPingSummary) != 1 || nodes[0].TCPingSummary[0].SampleCount != 2 {
		t.Fatal("HTTP summary", w.Code, w.Body.String(), err)
	}
	// Statistics must expire even if the Agent stops reporting; the raw history
	// and current probe configuration are retained independently of the window.
	*now = now.Add(time.Hour + time.Second)
	nodes, err := h.snapshots()
	if err != nil || len(nodes) != 1 || !nodes[0].TCPingEnabled || len(nodes[0].TCPingSummary) != 1 || nodes[0].TCPingSummary[0].SampleCount != 0 {
		t.Fatal("stale hour retained in cache", nodes, err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM tcpping_results WHERE agent_id=?", id).Scan(&count); err != nil || count != 2 {
		t.Fatal("window expiry deleted history", count, err)
	}
}
