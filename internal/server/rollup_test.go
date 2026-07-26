package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func insertPingRaw(t *testing.T, store *Store, agentID int64, at time.Time, name string, latency float64, success bool) {
	t.Helper()
	if err := store.InsertTCPing(&TCPingRow{AgentID: agentID, Name: name, Target: "t:80", LatencyMs: latency, Success: success}, at.Unix()); err != nil {
		t.Fatal(err)
	}
}

// Raw rows must survive as hourly aggregates after the raw retention purge.
func TestRollUpSurvivesRawPurge(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)

	// Two samples in one hour, six days ago (still inside raw retention).
	at := now.Add(-6 * 24 * time.Hour).Truncate(time.Hour)
	for i, m := range []MetricRow{
		{AgentID: agentID, CPUUsage: 10, MemoryTotal: 100, MemoryUsed: 40, NetworkUp: 100, NetworkDown: 200, TotalSent: 1000, TotalRecv: 2000},
		{AgentID: agentID, CPUUsage: 30, MemoryTotal: 100, MemoryUsed: 80, NetworkUp: 300, NetworkDown: 400, TotalSent: 1500, TotalRecv: 2500},
	} {
		if err := store.InsertMetric(&m, at.Add(time.Duration(i)*time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	insertPingRaw(t, store, agentID, at, "CMv4", 40, true)
	insertPingRaw(t, store, agentID, at.Add(time.Minute), "CMv4", 0, false)

	store.PurgeOldData() // rolls up, raw still inside retention

	// Advance two days: raw rows are now 8d old and get purged.
	store.now = func() time.Time { return now.Add(2 * 24 * time.Hour) }
	store.PurgeOldData()

	var rawCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM metrics WHERE agent_id = ?`, agentID).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != 0 {
		t.Fatalf("raw metrics after purge = %d, want 0", rawCount)
	}

	metrics, err := store.GetMetricsHourly(agentID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 {
		t.Fatalf("hourly metric rows = %d, want 1", len(metrics))
	}
	m := metrics[0]
	if m.CPUUsage != 20 || m.CPUPeak != 30 || m.MemoryPeakPct != 80 ||
		m.NetworkUp != 200 || m.NetworkUpPeak != 300 || m.TotalSent != 1500 || m.CreatedAt != at.Unix() {
		t.Fatalf("hourly metric = %+v, want avg cpu 20 / peak 30 / mem peak 80%% / net avg 200 peak 300", m)
	}

	pings, err := store.GetTCPingHourly(agentID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pings) != 1 {
		t.Fatalf("hourly tcpping rows = %d, want 1", len(pings))
	}
	p := pings[0]
	if p.Name != "CMv4" || p.LatencyMs != 40 || p.SampleCount != 2 || p.SuccessCount != 1 || !p.Success || p.LatencyPeak != 40 {
		t.Fatalf("hourly tcpping = %+v, want avg 40ms over 2 samples with 1 success", p)
	}
}

// Re-running the rollup updates a partially-filled hour instead of freezing it.
func TestRollUpIdempotentAndUpdating(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)
	at := now.Add(-2 * time.Hour).Truncate(time.Hour)

	if err := store.InsertMetric(&MetricRow{AgentID: agentID, CPUUsage: 10}, at.Unix()); err != nil {
		t.Fatal(err)
	}
	store.rollUpHourly()
	store.rollUpHourly() // idempotent

	if err := store.InsertMetric(&MetricRow{AgentID: agentID, CPUUsage: 50}, at.Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	store.rollUpHourly()

	metrics, err := store.GetMetricsHourly(agentID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].CPUUsage != 30 {
		t.Fatalf("hourly rows = %+v, want single row with cpu avg 30", metrics)
	}
}

func TestPurgeRemovesExpiredHourlyRows(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)

	stale := now.Add(-91 * 24 * time.Hour).Truncate(time.Hour).Unix()
	fresh := now.Add(-30 * 24 * time.Hour).Truncate(time.Hour).Unix()
	for _, h := range []int64{stale, fresh} {
		if _, err := store.db.Exec(
			`INSERT INTO metrics_hourly(agent_id, hour_start, cpu_avg, sample_count) VALUES(?, ?, 1, 1)`,
			agentID, h,
		); err != nil {
			t.Fatal(err)
		}
	}
	store.PurgeOldData()

	metrics, err := store.GetMetricsHourly(agentID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].CreatedAt != fresh {
		t.Fatalf("hourly rows after purge = %+v, want only hour_start %d", metrics, fresh)
	}
}

// range=720h serves from the hourly tables; short ranges stay on raw data.
func TestHandleAgentDetailHourlyRange(t *testing.T) {
	h, store := newTestHandler(t)
	store.now = h.now
	now := h.now()
	if err := store.AllowToken("test-token"); err != nil {
		t.Fatal(err)
	}
	if w := sendReport(t, h, "Bearer test-token", `{"hostname":"node-1","cpu":{"percent":10}}`); w.Code != http.StatusOK {
		t.Fatalf("report status = %d, body=%s", w.Code, w.Body.String())
	}
	agents, err := store.GetAgents()
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents = %d, err = %v", len(agents), err)
	}
	agentID := agents[0].ID

	hour := now.Add(-20 * 24 * time.Hour).Truncate(time.Hour).Unix()
	if _, err := store.db.Exec(
		`INSERT INTO metrics_hourly(agent_id, hour_start, cpu_avg, cpu_peak, sample_count) VALUES(?, ?, 42, 90, 120)`,
		agentID, hour,
	); err != nil {
		t.Fatal(err)
	}

	get := func(path string) []MetricRow {
		t.Helper()
		w := httptest.NewRecorder()
		h.handleAgentDetail(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body=%s", path, w.Code, w.Body.String())
		}
		var out []MetricRow
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	long := get("/api/agents/" + strconv.FormatInt(agentID, 10) + "/metrics?range=720h")
	if len(long) != 1 || long[0].CPUUsage != 42 || long[0].CPUPeak != 90 || long[0].CreatedAt != hour {
		t.Fatalf("720h rows = %+v, want the hourly row (cpu 42, peak 90)", long)
	}

	short := get("/api/agents/" + strconv.FormatInt(agentID, 10) + "/metrics?range=24h")
	if len(short) != 1 || short[0].CPUUsage != 10 {
		t.Fatalf("24h rows = %+v, want the raw report row (cpu 10)", short)
	}

	// Day drill-down beyond raw retention routes to the hourly table too.
	dayStart := hour - hour%86400
	oldDay := get("/api/agents/" + strconv.FormatInt(agentID, 10) + "/metrics?since=" + strconv.FormatInt(dayStart, 10) + "&until=" + strconv.FormatInt(dayStart+86400, 10))
	if len(oldDay) != 1 || oldDay[0].CPUUsage != 42 {
		t.Fatalf("old day window rows = %+v, want the hourly row (cpu 42)", oldDay)
	}

	// A recent day window stays on raw data.
	recentStart := now.Add(-2 * time.Hour).Unix()
	recent := get("/api/agents/" + strconv.FormatInt(agentID, 10) + "/metrics?since=" + strconv.FormatInt(recentStart, 10) + "&until=" + strconv.FormatInt(now.Add(time.Minute).Unix(), 10))
	if len(recent) != 1 || recent[0].CPUUsage != 10 {
		t.Fatalf("recent window rows = %+v, want the raw report row (cpu 10)", recent)
	}
}
