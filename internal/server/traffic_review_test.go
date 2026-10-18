package server

import (
	"math"
	"testing"
	"time"
)

func TestLegacyAccumulatorInitializationPreservesVisibleUsage(t *testing.T) {
	for _, tc := range []struct {
		name               string
		sent, recv, uptime int64
		delay              time.Duration
		wantSent, wantRecv int64
	}{
		{"late first report", 300, 400, 1460, -10 * time.Minute, 400, 600},
		{"first report confirms reboot", 10, 20, 10, 0, 410, 620},
		{"ordinary first report", 550, 900, 2060, 0, 450, 700},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			s, id := newTrafficTestStore(t, now, 1)
			if _, err := s.UpsertAgent("node-1", "SG", nil, "forever"); err != nil {
				t.Fatal(err)
			}
			start := monthlyBoundary(1, now)
			insertTrafficMetric(t, s, id, start, 100, 200)
			if err := s.EnsureTrafficBaseline(id, start.Unix()); err != nil {
				t.Fatal(err)
			}
			if err := s.InsertMetric(&MetricRow{AgentID: id, TotalSent: 500, TotalRecv: 800, Uptime: 2000}, now.Add(-time.Minute).Unix()); err != nil {
				t.Fatal(err)
			}
			assertUsage(t, s, id, 400, 600)
			for i := 0; i < 2; i++ {
				_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", BillingPeriod: "forever", CreatedAt: now.Add(tc.delay).Unix(),
					Metric: &MetricRow{TotalSent: tc.sent, TotalRecv: tc.recv, Uptime: tc.uptime}})
				if err != nil {
					t.Fatal(err)
				}
				assertUsage(t, s, id, tc.wantSent, tc.wantRecv)
			}
		})
	}
}

func TestLegacyInitializationAndBaselineStayInsideSampleCycle(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 1)
	if _, err := s.UpsertAgent("node-1", "SG", nil, "forever"); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 10, 31, 23, 58, 0, 0, time.UTC)
	insertTrafficMetric(t, s, id, old, 100, 200)
	if err := s.EnsureTrafficBaseline(id, monthlyBoundary(1, old).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertMetric(&MetricRow{AgentID: id, TotalSent: 150, TotalRecv: 270, Uptime: 1000}, old.Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	// This newer month's data must never seed the legacy October accumulator.
	insertTrafficMetric(t, s, id, now, 10000, 20000)
	_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", BillingPeriod: "forever", CreatedAt: old.Add(30 * time.Second).Unix(),
		Metric: &MetricRow{TotalSent: 130, TotalRecv: 240, Uptime: 970}})
	if err != nil {
		t.Fatal(err)
	}
	var sent, recv int64
	if err := s.db.QueryRow("SELECT usage_sent,usage_recv FROM traffic_baselines WHERE agent_id=? AND boundary=?", id, monthlyBoundary(1, old).Unix()).Scan(&sent, &recv); err != nil {
		t.Fatal(err)
	}
	if sent != 50 || recv != 70 {
		t.Fatalf("November contaminated October: %d/%d", sent, recv)
	}
	// No rows in September means no baseline, even if October data survives.
	if err := s.EnsureTrafficBaseline(id, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix()); err != nil {
		t.Fatal(err)
	}
	if got := auditCount(t, s, "traffic_baselines"); got != 1 {
		t.Fatalf("seeded a missing cycle from its successor: %d baselines", got)
	}
}

func TestLateReportCannotRevertLiveBillingAndProbeConfiguration(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 1)
	_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", Region: "JP", BillingPeriod: "forever", MonthlyTrafficLimit: 1000, CPUCores: 4, CreatedAt: now.Unix(),
		Metric: &MetricRow{TotalSent: 1000, TotalRecv: 2000, Uptime: 10000}, TCPings: []TCPingRow{{Name: "CMv6", Target: "[2001:db8::1]:80", Success: true}}})
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC).Unix()
	now = now.Add(time.Minute)
	s.now = func() time.Time { return now }
	_, err = s.SaveReport(&ReportWrite{Hostname: "node-1", Region: "US", BillingPeriod: "1m", ExpiresAt: &expiry, MonthlyTrafficLimit: 500, CPUCores: 2, CreatedAt: now.Add(-2 * time.Minute).Unix(),
		Metric: &MetricRow{TotalSent: 900, TotalRecv: 1900, Uptime: 9900}, TCPings: []TCPingRow{}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.GetAgents()
	if err != nil {
		t.Fatal(err)
	}
	a := rows[0]
	if a.BillingPeriod != "forever" || a.ExpiresAt != nil || a.MonthlyTrafficLimit != 1000 || a.Region != "JP" || a.CPUCores != 4 || a.LastSeen == nil || *a.LastSeen != now.Unix() {
		t.Fatalf("late report reverted live state: %+v", a)
	}
	pings, err := s.GetLatestTCPing(id)
	if err != nil || len(pings) != 1 || pings[0].Name != "CMv6" {
		t.Fatalf("late report disabled current probes: %+v %v", pings, err)
	}
	if auditCount(t, s, "metrics") != 2 {
		t.Fatal("late history was discarded")
	}
	assertUsage(t, s, id, 0, 0)
	// The persisted freshness marker still protects config after raw retention.
	execAuditSQL(t, s, "DELETE FROM metrics")
	_, err = s.SaveReport(&ReportWrite{Hostname: "node-1", BillingPeriod: "1m", ExpiresAt: &expiry, CreatedAt: now.Add(-2 * time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = s.GetAgents()
	if err != nil || rows[0].BillingPeriod != "forever" {
		t.Fatalf("raw purge erased metadata freshness: %+v %v", rows, err)
	}
}

func TestTrafficOverflowRollsBackEntireReport(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 1)
	_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", BillingPeriod: "forever", CreatedAt: now.Unix(), Metric: &MetricRow{TotalSent: 100, TotalRecv: 200, Uptime: 100}})
	if err != nil {
		t.Fatal(err)
	}
	execAuditSQL(t, s, "UPDATE traffic_baselines SET usage_sent=?", int64(math.MaxInt64))
	now = now.Add(time.Minute)
	s.now = func() time.Time { return now }
	_, err = s.SaveReport(&ReportWrite{Hostname: "node-1", Region: "new", BillingPeriod: "forever", MonthlyTrafficLimit: 1000, CreatedAt: now.Unix(), Metric: &MetricRow{TotalSent: 101, TotalRecv: 200, Uptime: 160}})
	if err == nil {
		t.Fatal("overflowing report must fail")
	}
	if auditCount(t, s, "metrics") != 1 {
		t.Fatal("failed report left a raw sample")
	}
	rows, err := s.GetAgents()
	if err != nil {
		t.Fatal(err)
	}
	if a := rows[0]; a.Region == "new" || a.MonthlyTrafficLimit != 0 || *a.LastSeen == now.Unix() {
		t.Fatalf("failed report committed metadata: %+v", a)
	}
	assertUsage(t, s, id, math.MaxInt64, 0)
}
