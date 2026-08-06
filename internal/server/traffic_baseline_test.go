package server

import (
	"path/filepath"
	"testing"
	"time"
)

// newTrafficTestStore returns a store with a fixed clock and one agent whose
// billing cycle resets on the given day of month.
func newTrafficTestStore(t *testing.T, now time.Time, resetDay int) (*Store, int64) {
	t.Helper()
	store, err := NewStoreCLI(filepath.Join(t.TempDir(), "needle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	store.now = func() time.Time { return now }

	token := "traffic-test-token"
	if err := store.AllowToken(token); err != nil {
		t.Fatal(err)
	}
	if err := store.BindToken(token, "node-1"); err != nil {
		t.Fatal(err)
	}
	// expires_at only contributes its day-of-month to the cycle boundary.
	expiresAt := time.Date(now.Year(), now.Month()+1, resetDay, 12, 0, 0, 0, now.Location()).Unix()
	id, err := store.UpsertAgent("node-1", "SG", &expiresAt, "1m")
	if err != nil {
		t.Fatal(err)
	}
	return store, id
}

func insertTrafficMetric(t *testing.T, store *Store, agentID int64, at time.Time, sent, recv int64) {
	t.Helper()
	if err := store.InsertMetric(&MetricRow{AgentID: agentID, TotalSent: sent, TotalRecv: recv}, at.Unix()); err != nil {
		t.Fatal(err)
	}
}

func assertUsage(t *testing.T, store *Store, agentID int64, wantSent, wantRecv int64) {
	t.Helper()
	usage, err := store.GetTrafficUsage(agentID)
	if err != nil {
		t.Fatal(err)
	}
	if !usage.HasData {
		t.Fatalf("usage has no data (reason=%q)", usage.Reason)
	}
	if usage.Sent != wantSent || usage.Recv != wantRecv {
		t.Fatalf("usage = %d/%d, want %d/%d", usage.Sent, usage.Recv, wantSent, wantRecv)
	}
}

// The core regression: the cycle baseline must survive the 7-day metric purge.
func TestTrafficUsageSurvivesPurge(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)
	boundary := monthlyBoundary(5, now)

	// First report of the cycle, 15 days ago — outside the retention window.
	insertTrafficMetric(t, store, agentID, boundary, 100, 200)
	if err := store.EnsureTrafficBaseline(agentID, boundary.Unix()); err != nil {
		t.Fatal(err)
	}
	insertTrafficMetric(t, store, agentID, now.Add(-time.Hour), 1100, 2200)

	store.PurgeOldData()
	var metricCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM metrics WHERE agent_id = ?`, agentID).Scan(&metricCount); err != nil {
		t.Fatal(err)
	}
	if metricCount != 1 {
		t.Fatalf("metrics after purge = %d, want 1", metricCount)
	}

	assertUsage(t, store, agentID, 1000, 2000)
}

// Without a baseline row (pre-upgrade installs) usage falls back to the
// earliest surviving metric, and the next EnsureTrafficBaseline seeds from it.
func TestTrafficUsageFallbackAndSeed(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 18)
	boundary := monthlyBoundary(18, now)

	insertTrafficMetric(t, store, agentID, boundary.Add(time.Hour), 50, 60)
	insertTrafficMetric(t, store, agentID, now.Add(-time.Hour), 150, 260)
	assertUsage(t, store, agentID, 100, 200)

	if err := store.EnsureTrafficBaseline(agentID, boundary.Unix()); err != nil {
		t.Fatal(err)
	}
	var baseSent, baseRecv int64
	if err := store.db.QueryRow(
		`SELECT total_sent, total_recv FROM traffic_baselines WHERE agent_id = ? AND boundary = ?`,
		agentID, boundary.Unix(),
	).Scan(&baseSent, &baseRecv); err != nil {
		t.Fatal(err)
	}
	if baseSent != 50 || baseRecv != 60 {
		t.Fatalf("seeded baseline = %d/%d, want 50/60", baseSent, baseRecv)
	}
}

// EnsureTrafficBaseline is a no-op without metrics and never overwrites an
// existing snapshot.
func TestEnsureTrafficBaselineIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)
	boundary := monthlyBoundary(5, now).Unix()

	if err := store.EnsureTrafficBaseline(agentID, boundary); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM traffic_baselines`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("baseline rows without metrics = %d, want 0", count)
	}

	insertTrafficMetric(t, store, agentID, now.Add(-2*time.Hour), 100, 100)
	if err := store.EnsureTrafficBaseline(agentID, boundary); err != nil {
		t.Fatal(err)
	}
	insertTrafficMetric(t, store, agentID, now.Add(-time.Hour), 500, 500)
	if err := store.EnsureTrafficBaseline(agentID, boundary); err != nil {
		t.Fatal(err)
	}
	var baseSent int64
	if err := store.db.QueryRow(
		`SELECT total_sent FROM traffic_baselines WHERE agent_id = ? AND boundary = ?`,
		agentID, boundary,
	).Scan(&baseSent); err != nil {
		t.Fatal(err)
	}
	if baseSent != 100 {
		t.Fatalf("baseline total_sent = %d, want 100 (must not be overwritten)", baseSent)
	}
}

// Crossing into a new cycle starts a fresh baseline; usage resets.
func TestTrafficUsageNewCycle(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)
	julyBoundary := monthlyBoundary(5, now)

	insertTrafficMetric(t, store, agentID, julyBoundary, 100, 100)
	if err := store.EnsureTrafficBaseline(agentID, julyBoundary.Unix()); err != nil {
		t.Fatal(err)
	}

	// Advance past Aug 5.
	later := time.Date(2026, 8, 6, 12, 0, 0, 0, time.Local)
	store.now = func() time.Time { return later }
	augBoundary := monthlyBoundary(5, later)
	insertTrafficMetric(t, store, agentID, augBoundary.Add(time.Hour), 5000, 6000)
	if err := store.EnsureTrafficBaseline(agentID, augBoundary.Unix()); err != nil {
		t.Fatal(err)
	}
	insertTrafficMetric(t, store, agentID, later.Add(-time.Hour), 5300, 6400)

	assertUsage(t, store, agentID, 300, 400)
}

// A counter reset (reboot) mid-cycle keeps the pre-reboot usage and adds the
// new counters reported since boot.
func TestTrafficUsageSurvivesCounterReset(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)
	boundary := monthlyBoundary(5, now)
	expiresAt := time.Date(2026, 8, 5, 12, 0, 0, 0, time.Local).Unix()
	report := func(at time.Time, sent, recv int64) {
		t.Helper()
		_, err := store.SaveReport(&ReportWrite{
			Hostname:         "node-1",
			Region:           "SG",
			ExpiresAt:        &expiresAt,
			BillingPeriod:    "1m",
			Metric:           &MetricRow{TotalSent: sent, TotalRecv: recv},
			CreatedAt:        at.Unix(),
			BaselineBoundary: int64Ptr(boundary.Unix()),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	report(boundary, 1000, 1000)               // cycle baseline
	report(now.Add(-2*time.Hour), 2500, 3000)  // 1500 / 2000 used
	report(now.Add(-time.Hour), 300, 400)      // reboot resets counters
	report(now.Add(-30*time.Minute), 500, 700) // another 200 / 300
	assertUsage(t, store, agentID, 2000, 2700) // pre- and post-reboot total
}

// The first report after upgrading from the baseline-only implementation must
// seed the accumulator with all usage already visible in the current cycle.
func TestTrafficAccumulatorUpgradeKeepsCurrentUsage(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)
	boundary := monthlyBoundary(5, now)
	expiresAt := time.Date(2026, 8, 5, 12, 0, 0, 0, time.Local).Unix()

	insertTrafficMetric(t, store, agentID, boundary, 100, 200)
	if err := store.EnsureTrafficBaseline(agentID, boundary.Unix()); err != nil {
		t.Fatal(err)
	}
	// Simulate the last metric written by the old server version.
	insertTrafficMetric(t, store, agentID, now.Add(-time.Hour), 600, 900)
	assertUsage(t, store, agentID, 500, 700)

	_, err := store.SaveReport(&ReportWrite{
		Hostname:         "node-1",
		Region:           "SG",
		ExpiresAt:        &expiresAt,
		BillingPeriod:    "1m",
		Metric:           &MetricRow{TotalSent: 800, TotalRecv: 1200},
		CreatedAt:        now.Unix(),
		BaselineBoundary: int64Ptr(boundary.Unix()),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, store, agentID, 700, 1000)
}

// A new due-day boundary still starts a fresh accumulator even though reboot
// resets no longer clear traffic inside a cycle.
func TestTrafficAccumulatorResetsAtDueDay(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)
	expiresAt := time.Date(2026, 8, 5, 12, 0, 0, 0, time.Local).Unix()
	julyBoundary := monthlyBoundary(5, now)

	for _, sample := range []struct {
		at         time.Time
		sent, recv int64
	}{
		{julyBoundary, 100, 200},
		{now, 600, 900},
	} {
		_, err := store.SaveReport(&ReportWrite{
			Hostname:         "node-1",
			Region:           "SG",
			ExpiresAt:        &expiresAt,
			BillingPeriod:    "1m",
			Metric:           &MetricRow{TotalSent: sample.sent, TotalRecv: sample.recv},
			CreatedAt:        sample.at.Unix(),
			BaselineBoundary: int64Ptr(julyBoundary.Unix()),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	assertUsage(t, store, agentID, 500, 700)

	later := time.Date(2026, 8, 6, 12, 0, 0, 0, time.Local)
	store.now = func() time.Time { return later }
	augustBoundary := monthlyBoundary(5, later)
	for _, sample := range []struct {
		sent, recv int64
	}{
		{7000, 8000}, // first report in the new cycle becomes its baseline
		{7300, 8400},
	} {
		_, err := store.SaveReport(&ReportWrite{
			Hostname:         "node-1",
			Region:           "SG",
			ExpiresAt:        &expiresAt,
			BillingPeriod:    "1m",
			Metric:           &MetricRow{TotalSent: sample.sent, TotalRecv: sample.recv},
			CreatedAt:        later.Unix(),
			BaselineBoundary: int64Ptr(augustBoundary.Unix()),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	assertUsage(t, store, agentID, 300, 400)
}

func int64Ptr(v int64) *int64 { return &v }

func TestDeleteAgentRemovesBaselines(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)
	boundary := monthlyBoundary(5, now)
	insertTrafficMetric(t, store, agentID, now.Add(-time.Hour), 100, 100)
	if err := store.EnsureTrafficBaseline(agentID, boundary.Unix()); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteAgent(agentID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM traffic_baselines WHERE agent_id = ?`, agentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("baseline rows after DeleteAgent = %d, want 0", count)
	}
}

func TestPurgeRemovesStaleBaselines(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	store, agentID := newTrafficTestStore(t, now, 5)
	insertTrafficMetric(t, store, agentID, now.Add(-time.Hour), 100, 100)

	stale := now.AddDate(0, -3, 0).Unix()
	current := monthlyBoundary(5, now).Unix()
	for _, b := range []int64{stale, current} {
		if _, err := store.db.Exec(
			`INSERT INTO traffic_baselines(agent_id, boundary, total_sent, total_recv, created_at) VALUES(?, ?, 0, 0, ?)`,
			agentID, b, now.Unix(),
		); err != nil {
			t.Fatal(err)
		}
	}

	store.PurgeOldData()

	rows, err := store.db.Query(`SELECT boundary FROM traffic_baselines WHERE agent_id = ?`, agentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var boundaries []int64
	for rows.Next() {
		var b int64
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		boundaries = append(boundaries, b)
	}
	if len(boundaries) != 1 || boundaries[0] != current {
		t.Fatalf("baselines after purge = %v, want only %d", boundaries, current)
	}
}

func TestCalcNextReset(t *testing.T) {
	loc := time.Local
	date := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	}
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, loc)
	cases := []struct {
		name     string
		anchor   time.Time
		period   string
		now      time.Time
		wantDays int
		wantDate string
	}{
		{"future annual anchor counts to renewal", date(2027, 2, 15), "12m", now, 204, "2027-02-15"},
		{"future monthly anchor", date(2026, 8, 23), "1m", now, 28, "2026-08-23"},
		{"lapsed day-31 monthly stays on month end", date(2026, 1, 31), "1m", now, 5, "2026-07-31"},
		{"lapsed day-31 quarterly clamps to feb", date(2025, 8, 31), "3m",
			time.Date(2026, 1, 15, 12, 0, 0, 0, loc), 44, "2026-02-28"},
		{"leap-day annual clamps to feb 28", date(2028, 2, 29), "12m",
			time.Date(2029, 1, 10, 12, 0, 0, 0, loc), 49, "2029-02-28"},
		{"unknown period disables the badge", date(2026, 8, 23), "", now, 0, ""},
	}
	for _, tc := range cases {
		days, dateStr := calcNextReset(tc.anchor.Unix(), tc.period, tc.now)
		if days != tc.wantDays || dateStr != tc.wantDate {
			t.Errorf("%s: calcNextReset = (%d, %q), want (%d, %q)",
				tc.name, days, dateStr, tc.wantDays, tc.wantDate)
		}
	}
}

// The due badge and the traffic reset must land on the same calendar day for
// monthly billing, whatever the anchor's day of month.
func TestDueAndTrafficResetAgree(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, loc)
	for day := 1; day <= 31; day++ {
		anchor := time.Date(2026, 1, day, 0, 0, 0, 0, loc)
		_, dueDate := calcNextReset(anchor.Unix(), "1m", now)
		due, err := time.ParseInLocation("2006-01-02", dueDate, loc)
		if err != nil {
			t.Fatalf("day %d: parse due date %q: %v", day, dueDate, err)
		}
		cycleStart := monthlyBoundary(anchor.Day(), now)
		next := monthlyBoundary(anchor.Day(), due.Add(12*time.Hour))
		if !next.Equal(due) {
			t.Errorf("day %d: due %s but traffic resets %s (cycle started %s)",
				day, due.Format("2006-01-02"), next.Format("2006-01-02"), cycleStart.Format("2006-01-02"))
		}
	}
}

func TestMonthlyBoundary(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		name string
		day  int
		now  time.Time
		want time.Time
	}{
		{"mid-month after reset", 5, time.Date(2026, 7, 20, 12, 0, 0, 0, loc), time.Date(2026, 7, 5, 0, 0, 0, 0, loc)},
		{"mid-month before reset", 25, time.Date(2026, 7, 20, 12, 0, 0, 0, loc), time.Date(2026, 6, 25, 0, 0, 0, 0, loc)},
		{"reset day itself", 20, time.Date(2026, 7, 20, 0, 0, 0, 0, loc), time.Date(2026, 7, 20, 0, 0, 0, 0, loc)},
		{"day 31 after short month", 31, time.Date(2026, 3, 5, 12, 0, 0, 0, loc), time.Date(2026, 2, 28, 0, 0, 0, 0, loc)},
		{"day 31 in leap february", 31, time.Date(2028, 3, 5, 12, 0, 0, 0, loc), time.Date(2028, 2, 29, 0, 0, 0, 0, loc)},
		{"day 31 in 30-day month", 31, time.Date(2026, 4, 30, 12, 0, 0, 0, loc), time.Date(2026, 4, 30, 0, 0, 0, 0, loc)},
		{"year boundary", 31, time.Date(2026, 1, 5, 12, 0, 0, 0, loc), time.Date(2025, 12, 31, 0, 0, 0, 0, loc)},
	}
	for _, tc := range cases {
		if got := monthlyBoundary(tc.day, tc.now); !got.Equal(tc.want) {
			t.Errorf("%s: monthlyBoundary(%d, %s) = %s, want %s",
				tc.name, tc.day, tc.now.Format("2006-01-02"), got.Format("2006-01-02"), tc.want.Format("2006-01-02"))
		}
	}
}
