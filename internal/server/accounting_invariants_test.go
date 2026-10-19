package server

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func execAuditSQL(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func auditCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPartialCollectorsDoNotDistortAverages(t *testing.T) {
	h, s := newTestHandler(t)
	now := h.now()
	h.now = func() time.Time { return now }
	s.now = h.now
	if err := s.AllowToken("partial"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"hostname":"partial","cpu":{"percent":10,"cores":4}}`,
		`{"hostname":"partial","network":{"up":100,"down":200,"total_sent":1000,"total_recv":2000}}`,
		`{"hostname":"partial","cpu":{"percent":30}}`,
	} {
		if w := sendReport(t, h, "Bearer partial", body); w.Code != http.StatusOK {
			t.Fatalf("report %d: %s", w.Code, w.Body.String())
		}
		now = now.Add(30 * time.Second)
	}
	agents, err := s.GetAgents()
	if err != nil || len(agents) != 1 || agents[0].CPUCores != 4 {
		t.Fatalf("cores must survive a partial report: %+v %v", agents, err)
	}
	id := agents[0].ID
	latest, err := s.GetLatestMetric(id)
	if err != nil || latest.MissingMetrics&missingNetwork == 0 {
		t.Fatalf("missing network: %+v %v", latest, err)
	}
	for _, hourly := range []bool{false, true} {
		var rows []MetricRow
		if hourly {
			mustRollUpHourly(t, s)
			rows, err = s.GetMetricsHourly(id, 0, 0)
		} else {
			rows, err = s.GetMetricsWindowSampled(id, 0, 0, 900)
		}
		if err != nil || len(rows) != 1 {
			t.Fatalf("hourly=%v: %+v %v", hourly, rows, err)
		}
		m := rows[0]
		if m.CPUUsage != 20 || m.CPUPeak != 30 || m.NetworkUp != 100 || m.NetworkDown != 200 || m.MissingMetrics != (missingMemory|missingDisk|missingLoad) {
			t.Fatalf("hourly=%v: missing collectors diluted valid averages: %+v", hourly, m)
		}
	}
}

func TestZeroNetworkCountersAreAValidBaseline(t *testing.T) {
	h, s := newTestHandler(t)
	now := h.now()
	h.now = func() time.Time { return now }
	s.now = h.now
	if err := s.AllowToken("zero"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"hostname":"zero","billing_period":"forever","network":{"total_sent":0,"total_recv":0},"uptime":100}`,
		`{"hostname":"zero","billing_period":"forever","cpu":{"percent":10},"uptime":101}`,
		`{"hostname":"zero","billing_period":"forever","network":{"total_sent":50,"total_recv":70},"uptime":102}`,
	} {
		if w := sendReport(t, h, "Bearer zero", body); w.Code != 200 {
			t.Fatalf("report %d %s", w.Code, w.Body.String())
		}
		now = now.Add(30 * time.Second)
	}
	rows, err := s.GetAgents()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, s, rows[0].ID, 50, 70)
}

func TestHistoryFailureRollsBackBothStreamsAndCheckpoint(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 6)
	at := now.Add(-8 * 24 * time.Hour)
	insertTrafficMetric(t, s, id, at, 100, 200)
	insertPingRaw(t, s, id, at, "CMv4", 40, true)
	execAuditSQL(t, s, `CREATE TRIGGER fail_ping_rollup BEFORE INSERT ON tcpping_hourly BEGIN SELECT RAISE(FAIL,'injected second-stream failure'); END`)
	if err := s.maintainHistory(true); err == nil {
		t.Fatal("expected rollup error")
	}
	for _, table := range []string{"metrics", "tcpping_results"} {
		if auditCount(t, s, table) != 1 {
			t.Fatalf("%s raw was lost", table)
		}
	}
	if auditCount(t, s, "metrics_hourly") != 0 {
		t.Fatal("first stream committed before the second failed")
	}
	var progress int64
	if err := s.db.QueryRow("SELECT SUM(last_id) FROM history_rollup_state").Scan(&progress); err != nil || progress != 0 {
		t.Fatalf("checkpoint advanced: %d %v", progress, err)
	}
	execAuditSQL(t, s, "DROP TRIGGER fail_ping_rollup")
	if err := s.maintainHistory(true); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"metrics", "tcpping_results"} {
		if auditCount(t, s, table) != 0 {
			t.Fatalf("%s raw not purged after successful retry", table)
		}
	}
	for _, table := range []string{"metrics_hourly", "tcpping_hourly"} {
		if auditCount(t, s, table) != 1 {
			t.Fatalf("%s archive missing", table)
		}
	}
	// A deletion failure must roll back the newly merged sample too.
	insertPingRaw(t, s, id, at.Add(time.Minute), "CMv4", 80, true)
	execAuditSQL(t, s, `CREATE TRIGGER fail_raw_delete BEFORE DELETE ON tcpping_results BEGIN SELECT RAISE(FAIL,'injected delete failure'); END`)
	if err := s.maintainHistory(true); err == nil {
		t.Fatal("expected deletion error")
	}
	var n int
	if err := s.db.QueryRow("SELECT sample_count FROM tcpping_hourly").Scan(&n); err != nil || n != 1 {
		t.Fatalf("failed purge committed aggregate: %d %v", n, err)
	}
	execAuditSQL(t, s, "DROP TRIGGER fail_raw_delete")
	if err := s.maintainHistory(true); err != nil {
		t.Fatal(err)
	}
	rows, err := s.GetTCPingHourly(id, 0, 0)
	if err != nil || len(rows) != 1 || rows[0].SampleCount != 2 || rows[0].LatencyMs != 60 {
		t.Fatalf("retry duplicated a sample: %+v %v", rows, err)
	}
}

func TestLegacyRollupMigrationPreservesAmbiguousHours(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 30, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 6)
	old := now.Add(-8 * 24 * time.Hour).Truncate(time.Hour)
	recent := now.Add(-time.Hour).Truncate(time.Hour)
	for _, at := range []time.Time{old, recent} {
		execAuditSQL(t, s, `INSERT INTO metrics_hourly(agent_id,hour_start,cpu_avg,cpu_peak,sample_count) VALUES(?,?,20,30,2)`, id, at.Unix())
		execAuditSQL(t, s, `INSERT INTO tcpping_hourly(agent_id,name,hour_start,latency_avg,latency_peak,sample_count,success_count) VALUES(?,'CMv4',?,40,40,2,1)`, id, at.Unix())
		if err := s.InsertMetric(&MetricRow{AgentID: id, CPUUsage: 50}, at.Add(time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
		insertPingRaw(t, s, id, at.Add(time.Minute), "CMv4", 80, true)
	}
	// Removing the checkpoint simulates a database from before this release.
	execAuditSQL(t, s, "DELETE FROM history_rollup_state")
	if err := s.migrateAccountingAudit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"metrics_rollup_legacy", "metrics_hourly_rollup_legacy", "tcpping_results_rollup_legacy", "tcpping_hourly_rollup_legacy"} {
		if auditCount(t, s, table) != 1 {
			t.Fatalf("%s must retain only ambiguous old evidence", table)
		}
	}
	rows, err := s.GetMetricsHourly(id, 0, 0)
	if err != nil || len(rows) != 2 || rows[0].CPUUsage != 20 || rows[1].CPUUsage != 50 {
		t.Fatalf("migration guessed an old overlap or missed recent rebuild: %+v %v", rows, err)
	}
	if err := s.migrateAccountingAudit(); err != nil {
		t.Fatal(err)
	}
	if auditCount(t, s, "metrics_rollup_legacy") != 1 {
		t.Fatal("migration duplicated its evidence")
	}
	// This sample was ingested after the checkpoint and can safely be merged.
	if err := s.InsertMetric(&MetricRow{AgentID: id, CPUUsage: 50}, old.Add(2*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	insertPingRaw(t, s, id, old.Add(2*time.Minute), "CMv4", 80, true)
	if err := s.maintainHistory(true); err != nil {
		t.Fatal(err)
	}
	rows, err = s.GetMetricsHourly(id, 0, 0)
	pings, pingErr := s.GetTCPingHourly(id, 0, 0)
	if err != nil || pingErr != nil || rows[0].CPUUsage != 30 || pings[0].LatencyMs != 60 || pings[0].SampleCount != 3 || pings[0].SuccessCount != 2 {
		t.Fatalf("post-migration sample was not merged once: %+v %+v %v %v", rows, pings, err, pingErr)
	}
	// Restarting must retain the checkpoint even after the raw rows are gone.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStoreCLI(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now }
	if err := reopened.maintainHistory(true); err != nil {
		t.Fatal(err)
	}
	rows, err = reopened.GetMetricsHourly(id, 0, 0)
	if err != nil || rows[0].CPUUsage != 30 {
		t.Fatalf("restart duplicated archive: %+v %v", rows, err)
	}
}

func TestCalendarMigrationCarriesUsageUntilCorrectedReset(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	previousLocal := time.Local
	time.Local = loc
	defer func() { time.Local = previousLocal }()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, loc)
	s, id := newTrafficTestStore(t, now, 6)
	expiry := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC).Unix()
	execAuditSQL(t, s, "UPDATE agents SET expires_at=? WHERE id=?", expiry, id)
	old := monthlyBoundary(5, now).Unix()
	execAuditSQL(t, s, `INSERT INTO traffic_baselines(agent_id,boundary,total_sent,total_recv,usage_sent,usage_recv,last_sent,last_recv,last_uptime,last_sample_at,created_at,accounting_version)
	 VALUES(?,?,100,200,400,600,500,800,1000,?,?,1)`, id, old, now.Add(-time.Minute).Unix(), now.Unix())
	execAuditSQL(t, s, "DELETE FROM accounting_migrations WHERE name='calendar_dates'")
	if err := s.migrateAccountingAudit(); err != nil {
		t.Fatal(err)
	}
	assertUsage(t, s, id, 400, 600)
	save := func(at time.Time, sent, recv, uptime int64) {
		s.now = func() time.Time { return at }
		_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", ExpiresAt: &expiry, BillingPeriod: "1m", CreatedAt: at.Unix(), Metric: &MetricRow{TotalSent: sent, TotalRecv: recv, Uptime: uptime}})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(now, 510, 820, 1060)
	assertUsage(t, s, id, 410, 620)
	if err := s.migrateAccountingAudit(); err != nil {
		t.Fatal(err)
	}
	assertUsage(t, s, id, 410, 620)
	// The old wrong day (5th) no longer resets the ongoing cycle.
	save(time.Date(2026, 11, 5, 12, 0, 0, 0, loc), 600, 900, 2000000)
	assertUsage(t, s, id, 500, 700)
	// DST ends during the transition; reset still occurs at local midnight.
	save(time.Date(2026, 11, 6, 0, 0, 0, 0, loc), 610, 920, 2043200)
	assertUsage(t, s, id, 0, 0)
	save(time.Date(2026, 11, 6, 0, 1, 0, 0, loc), 620, 940, 2043260)
	assertUsage(t, s, id, 10, 20)
	var untouched int64
	if err := s.db.QueryRow("SELECT usage_sent FROM traffic_baselines WHERE agent_id=? AND boundary=?", id, old).Scan(&untouched); err != nil || untouched != 500 {
		t.Fatalf("original cycle was rewritten: %d %v", untouched, err)
	}
}

func TestRenewalCountdownUsesCalendarDaysAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		now  time.Time
		due  string
		days int
	}{
		{time.Date(2026, 3, 7, 0, 0, 0, 0, loc), "2026-03-09", 2},
		{time.Date(2026, 10, 31, 0, 0, 0, 0, loc), "2026-11-02", 2},
		{time.Date(2026, 11, 5, 23, 59, 0, 0, loc), "2026-11-06", 1},
	} {
		anchor, err := time.Parse("2006-01-02", tc.due)
		if err != nil {
			t.Fatal(err)
		}
		days, date := calcRenewalDue(anchor.Unix(), "1m", tc.now)
		if days != tc.days || date != tc.due {
			t.Fatalf("%v: days/date=%d/%s want %d/%s", tc.now, days, date, tc.days, tc.due)
		}
	}
}

func TestDelayedPreviousMonthDoesNotChangeCurrentUsage(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 2, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 1)
	save := func(at time.Time, sent int64) {
		_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", BillingPeriod: "forever", CreatedAt: at.Unix(), Metric: &MetricRow{TotalSent: sent, TotalRecv: sent, Uptime: at.Unix()}})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(now.Add(-3*time.Minute), 1000)
	save(now.Add(-time.Minute), 1100)
	save(now, 1120)
	assertUsage(t, s, id, 20, 20)
	save(now.Add(-150*time.Second), 1050) // late sample in October
	assertUsage(t, s, id, 20, 20)
	save(now.Add(-30*time.Second), 1110) // out-of-order sample in November
	assertUsage(t, s, id, 20, 20)
}

func TestDeleteCleansAccountingEvidenceBeforeIDReuse(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 6)
	insertTrafficMetric(t, s, id, now.Add(-8*24*time.Hour), 100, 200)
	insertPingRaw(t, s, id, now.Add(-8*24*time.Hour), "CMv4", 40, true)
	mustRollUpHourly(t, s)
	for _, table := range []string{"metrics", "metrics_hourly", "tcpping_results", "tcpping_hourly"} {
		execAuditSQL(t, s, "INSERT INTO "+table+"_rollup_legacy SELECT * FROM "+table)
	}
	execAuditSQL(t, s, `INSERT INTO traffic_cycle_transition VALUES(?,1,'1m',1,?)`, id, now.Add(time.Hour).Unix())
	if err := s.DeleteAgent(id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agents", "agent_tokens", "metrics", "metrics_hourly", "tcpping_results", "tcpping_hourly", "traffic_baselines", "traffic_accounting_legacy", "agent_tcpping_current", "traffic_cycle_transition", "metrics_rollup_legacy", "metrics_hourly_rollup_legacy", "tcpping_results_rollup_legacy", "tcpping_hourly_rollup_legacy"} {
		if auditCount(t, s, table) != 0 {
			t.Fatalf("leftover data in %s", table)
		}
	}
	newID, err := s.UpsertAgent("new-node", "", nil, "forever")
	if err != nil || newID != id {
		t.Fatalf("ID reuse: %d want %d (%v)", newID, id, err)
	}
	insertTrafficMetric(t, s, newID, now, 10000, 20000)
	mustRollUpHourly(t, s)
	rows, err := s.GetMetricsHourly(newID, 0, 0)
	if err != nil || len(rows) != 1 || rows[0].TotalSent != 10000 {
		t.Fatalf("reused ID inherited history: %+v %v", rows, err)
	}
}

func TestTrafficLegacyUnknownZeroIsNotAConfirmedNetworkBaseline(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 6)
	insertTrafficMetric(t, s, id, now.Add(-time.Minute), 0, 0)
	execAuditSQL(t, s, "UPDATE metrics SET missing_metrics=NULL")
	insertTrafficMetric(t, s, id, now, 1000, 2000)
	boundary := monthlyBoundary(6, now).Unix()
	if err := s.EnsureTrafficBaseline(id, boundary); err != nil {
		t.Fatal(err)
	}
	assertUsage(t, s, id, 0, 0)
}

func TestRollupMigrationFailureIsAtomic(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 6)
	insertTrafficMetric(t, s, id, now, 100, 200)
	insertPingRaw(t, s, id, now, "CMv4", 40, true)
	execAuditSQL(t, s, "DELETE FROM history_rollup_state")
	execAuditSQL(t, s, `CREATE TRIGGER reject_bootstrap BEFORE INSERT ON tcpping_hourly BEGIN SELECT RAISE(FAIL,'injected bootstrap failure'); END`)
	if err := s.migrateAccountingAudit(); err == nil {
		t.Fatal("expected bootstrap failure")
	}
	if auditCount(t, s, "history_rollup_state") != 0 || auditCount(t, s, "metrics_hourly") != 0 {
		t.Fatal("migration partially committed")
	}
	execAuditSQL(t, s, "DROP TRIGGER reject_bootstrap")
	if err := s.migrateAccountingAudit(); err != nil {
		t.Fatal(err)
	}
	if auditCount(t, s, "history_rollup_state") != 2 || auditCount(t, s, "metrics_hourly") != 1 {
		t.Fatal("migration could not be retried")
	}
}

func TestEmptyUpgradedDatabaseCanReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	s, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var id int64
	if err := s.db.QueryRow("SELECT id FROM metrics").Scan(&id); err != sql.ErrNoRows {
		t.Fatalf("unexpected data %v", err)
	}
}

func TestIncrementalHourlyMeansKeepPrecisionAndResourceCounts(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 6)
	// Each sample lands in a different maintenance batch. Rounding the first
	// batch's memory average (2.5) before the final merge would yield 4, not 3.
	for i, m := range []MetricRow{
		{CPUUsage: 10, MemoryTotal: 10, MemoryUsed: 2, NetworkUp: 100, MissingMetrics: missingLoad},
		{CPUUsage: 30, MemoryTotal: 10, MemoryUsed: 3, NetworkUp: 300, MissingMetrics: missingLoad},
		{MemoryTotal: 10, MemoryUsed: 5, Load1: 2, MissingMetrics: missingCPU | missingNetwork},
	} {
		m.AgentID = id
		if err := s.InsertMetric(&m, now.Add(time.Duration(i)*time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
		mustRollUpHourly(t, s)
	}
	metrics, err := s.GetMetricsHourly(id, 0, 0)
	if err != nil || len(metrics) != 1 {
		t.Fatalf("rows %+v %v", metrics, err)
	}
	m := metrics[0]
	if m.CPUUsage != 20 || m.MemoryUsed != 3 || m.NetworkUp != 200 || m.Load1 != 2 || m.MissingMetrics != 0 {
		t.Fatalf("incremental weighted averages lost precision or diluted missing data: %+v", m)
	}
}

func TestFastAgentClockCannotSeedNextMonthBeforeReset(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 1)
	s.now = func() time.Time { return now }
	save := func(at time.Time, sent int64) {
		_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", BillingPeriod: "forever", CreatedAt: at.Unix(), Metric: &MetricRow{TotalSent: sent, TotalRecv: sent, Uptime: sent}})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(now.Add(3*time.Minute), 1000)
	latest, err := s.GetLatestMetric(id)
	if err != nil || latest.CreatedAt != now.Unix() {
		t.Fatalf("future sample time was not capped: %+v %v", latest, err)
	}
	now = now.Add(30 * time.Second)
	save(now, 1050)
	assertUsage(t, s, id, 50, 50)
	now = now.Add(time.Minute)
	save(now, 1100)
	assertUsage(t, s, id, 0, 0)
}
