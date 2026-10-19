package server

import (
	"net/http"
	"testing"
	"time"
)

func TestAuditTrafficMustNotSeedFromMissingNetwork(t *testing.T) {
	h, store := newTestHandler(t)
	store.now = h.now
	if err := store.AllowToken("audit-network"); err != nil {
		t.Fatal(err)
	}
	// A partial report does not provide kernel network counters. Its stored
	// zero placeholders must not become the billing cycle's traffic baseline.
	for _, body := range []string{
		`{"hostname":"node","billing_period":"forever","cpu":{"percent":10},"uptime":1000}`,
		`{"hostname":"node","billing_period":"forever","cpu":{"percent":10},"network":{"total_sent":1000000000,"total_recv":2000000000},"uptime":1001}`,
	} {
		if w := sendReport(t, h, "Bearer audit-network", body); w.Code != http.StatusOK {
			t.Fatalf("report: %d %s", w.Code, w.Body.String())
		}
	}
	rows, err := store.GetAgents()
	if err != nil || len(rows) != 1 {
		t.Fatalf("agents: %v %v", rows, err)
	}
	assertUsage(t, store, rows[0].ID, 0, 0)
}

func TestAuditTrafficMustAcceptNetworkOnlyReport(t *testing.T) {
	h, store := newTestHandler(t)
	store.now = h.now
	if err := store.AllowToken("audit-network"); err != nil {
		t.Fatal(err)
	}
	w := sendReport(t, h, "Bearer audit-network", `{"hostname":"node","billing_period":"forever","network":{"total_sent":100,"total_recv":200},"uptime":1000}`)
	if w.Code != http.StatusOK {
		t.Fatalf("network-only report: %d %s", w.Code, w.Body.String())
	}
	rows, err := store.GetAgents()
	if err != nil || len(rows) != 1 {
		t.Fatalf("agents: %v %v", rows, err)
	}
	assertUsage(t, store, rows[0].ID, 0, 0)
}

func TestAuditDueDateMustPreserveConfiguredCalendarDate(t *testing.T) {
	// The Agent encodes the date-only YAML setting using time.Parse, i.e.
	// midnight UTC. A Server must not turn that configured date into another
	// day, nor advance it by a month before its own local midnight arrives.
	anchor, err := time.Parse("2006-01-02", "2026-11-06")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 20, 0, 0, 0, time.FixedZone("west", -7*3600))
	days, date := calcRenewalDue(anchor.Unix(), "1m", now)
	if date != "2026-11-06" || days != 1 {
		t.Fatalf("configured date 2026-11-06 became (%d, %s) before local midnight", days, date)
	}
}

// Regression coverage for the accounting/retention guarantees.
func TestAuditPurgeMustRollUpExpiredRawAfterDowntime(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store, id := newTrafficTestStore(t, now, 6)
	at := now.Add(-8 * 24 * time.Hour)
	if err := store.InsertMetric(&MetricRow{AgentID: id, CPUUsage: 20}, at.Unix()); err != nil {
		t.Fatal(err)
	}
	insertPingRaw(t, store, id, at, "CMv4", 70, true)
	store.PurgeOldData()
	metrics, err := store.GetMetricsHourly(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	pings, err := store.GetTCPingHourly(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || len(pings) != 1 {
		t.Fatalf("8-day raw samples disappeared instead of being archived: metrics=%d probes=%d", len(metrics), len(pings))
	}
}

func TestAuditPurgeMustNotOverwritePartialRetentionHour(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	store, id := newTrafficTestStore(t, now, 6)
	at := now.Add(-7 * 24 * time.Hour).Add(time.Hour)
	for i, minute := range []int{10, 50} {
		if err := store.InsertMetric(&MetricRow{AgentID: id, CPUUsage: float64(10 + i*20)}, at.Add(time.Duration(minute)*time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
		insertPingRaw(t, store, id, at.Add(time.Duration(minute)*time.Minute), "CMv4", float64(40+i*40), true)
	}
	store.PurgeOldData() // Complete hourly averages: CPU 20, latency 60.
	store.now = func() time.Time { return now.Add(90 * time.Minute) }
	store.PurgeOldData() // The moving cutoff now bisects that same hour.
	if got := auditCount(t, store, "metrics"); got != 2 {
		t.Fatalf("boundary raw hour must stay complete: got %d samples", got)
	}
	metrics, err := store.GetMetricsHourly(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	pings, err := store.GetTCPingHourly(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].CPUUsage != 20 || len(pings) != 1 || pings[0].LatencyMs != 60 || pings[0].SampleCount != 2 {
		t.Fatalf("partly expired raw hour overwrote its complete archive: metrics=%+v probes=%+v", metrics, pings)
	}
}

func TestAuditPurgeMustRetainRawWhenRollUpFails(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store, id := newTrafficTestStore(t, now, 6)
	at := now.Add(-6 * 24 * time.Hour)
	if err := store.InsertMetric(&MetricRow{AgentID: id, CPUUsage: 25}, at.Unix()); err != nil {
		t.Fatal(err)
	}
	insertPingRaw(t, store, id, at, "CMv4", 70, true)
	if _, err := store.db.Exec(`CREATE TRIGGER reject_rollup BEFORE INSERT ON metrics_hourly BEGIN SELECT RAISE(FAIL, 'injected storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	store.PurgeOldData()
	store.now = func() time.Time { return now.Add(2 * 24 * time.Hour) }
	store.PurgeOldData()
	var raw int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM metrics WHERE agent_id=?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 1 {
		t.Fatalf("failed aggregation was followed by raw deletion: remaining=%d want=1", raw)
	}
}

func TestAuditLateSampleMustMergeIntoAlreadyArchivedHour(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store, id := newTrafficTestStore(t, now, 6)
	at := now.Add(-8 * 24 * time.Hour)
	// The original two raw samples have already been purged, leaving only
	// their complete archive (CPU values 10 and 30, average 20).
	if _, err := store.db.Exec(`INSERT INTO metrics_hourly(agent_id,hour_start,cpu_avg,cpu_peak,sample_count) VALUES(?,?,20,30,2)`, id, at.Unix()); err != nil {
		t.Fatal(err)
	}
	// A valid delayed/backfilled report for that old hour adds CPU value 50.
	if err := store.InsertMetric(&MetricRow{AgentID: id, CPUUsage: 50}, at.Add(20*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	store.PurgeOldData()
	metrics, err := store.GetMetricsHourly(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].CPUUsage != 30 || metrics[0].CPUPeak != 50 {
		t.Fatalf("late sample must merge once, without replacing/double-counting the archive: %+v", metrics)
	}
	store.PurgeOldData()
	metrics, err = store.GetMetricsHourly(id, 0, 0)
	if err != nil || len(metrics) != 1 || metrics[0].CPUUsage != 30 {
		t.Fatalf("repeated purge changed the merged archive: rows=%+v err=%v", metrics, err)
	}
}
