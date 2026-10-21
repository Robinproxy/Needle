package server

import (
	"strings"
	"testing"
	"time"
)

const bootA = "11111111-1111-1111-1111-111111111111"
const bootB = "22222222-2222-2222-2222-222222222222"

func identityMetric(at, sent, recv, uptime int64, boot, scope string) *MetricRow {
	return &MetricRow{CreatedAt: at, TotalSent: sent, TotalRecv: recv, Uptime: uptime, BootID: boot, ScopeID: scope}
}
func TestStrongTrafficIdentityStateMachine(t *testing.T) {
	scopeA, scopeB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	s := trafficCounterState{Sent: 1000, Recv: 2000, SampleAt: 100, Uptime: 100}
	samples := []struct {
		m          *MetricRow
		sent, recv int64
		reason     string
	}{
		{identityMetric(110, 1100, 2100, 110, bootA, scopeA), 0, 0, "identity_migration_baseline"},
		{identityMetric(120, 1150, 2180, 120, bootA, scopeA), 50, 80, "continuous"},
		{identityMetric(130, 1100, 2100, 130, bootA, scopeA), 0, 0, "counter_regression_high_watermark_retained"},
		{identityMetric(140, 1160, 2200, 140, bootA, scopeA), 10, 20, "continuous"},
		{identityMetric(150, 1000000, 2000000, 150, bootA, scopeB), 0, 0, "network_scope_rebaseline"},
		{identityMetric(160, 1000010, 2000020, 160, bootA, scopeB), 10, 20, "continuous"},
		// Long outage: the new boot already has higher uptime. Boot ID still proves reboot.
		{identityMetric(10000, 100, 200, 9000, bootB, scopeB), 100, 200, "confirmed_system_reboot"},
		{identityMetric(170, 1000020, 2000030, 170, bootA, scopeB), 0, 0, "confirmed_system_reboot"},
		{identityMetric(10010, 110, 220, 9010, bootB, scopeB), 10, 20, "continuous"},
		{identityMetric(10020, 9999999, 9999999, 9020, "", ""), 0, 0, "identity_missing"},
		{identityMetric(10030, 120, 240, 9030, bootB, scopeB), 10, 20, "continuous"},
	}
	for i, tc := range samples {
		sent, recv := s.consume(tc.m)
		if sent != tc.sent || recv != tc.recv {
			t.Fatalf("%d: got %d/%d want %d/%d state=%+v", i, sent, recv, tc.sent, tc.recv, s)
		}
	}
	if s.BootID != bootB {
		t.Fatal("old boot resurrected")
	}
}
func TestStrongTrafficMigrationPreservesUsageAndScopeChange(t *testing.T) {
	s := newTransactionTestStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	scope := strings.Repeat("a", 64)
	save := func(m *MetricRow) {
		t.Helper()
		if _, err := s.SaveReport(&ReportWrite{Hostname: "identity", BillingPeriod: "forever", CreatedAt: m.CreatedAt, Metric: m}); err != nil {
			t.Fatal(err)
		}
	}
	save(identityMetric(now.Unix(), 100, 200, 100, "", ""))
	now = now.Add(30 * time.Second)
	save(identityMetric(now.Unix(), 150, 280, 130, "", ""))
	id, _, _ := s.AgentIDByHostname("identity")
	before, _ := s.GetTrafficUsage(id)
	now = now.Add(30 * time.Second)
	save(identityMetric(now.Unix(), 160, 290, 160, bootA, scope))
	after, _ := s.GetTrafficUsage(id)
	if before.Sent != after.Sent || before.Recv != after.Recv {
		t.Fatalf("migration changed usage %+v %+v", before, after)
	}
	now = now.Add(30 * time.Second)
	save(identityMetric(now.Unix(), 170, 310, 190, bootA, scope))
	after, _ = s.GetTrafficUsage(id)
	if after.Sent != before.Sent+10 || after.Recv != before.Recv+20 {
		t.Fatal(after)
	}
	now = now.Add(30 * time.Second)
	save(identityMetric(now.Unix(), 9999999, 9999999, 220, bootA, strings.Repeat("b", 64)))
	changed, _ := s.GetTrafficUsage(id)
	if changed.Sent != after.Sent || changed.Recv != after.Recv {
		t.Fatalf("scope caused spike %+v %+v", after, changed)
	}
}

func TestOldBootCannotReturnEvenWithSkewedNewerTimestamp(t *testing.T) {
	s := newTransactionTestStore(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	scope := strings.Repeat("a", 64)
	save := func(boot string, counter int64) {
		t.Helper()
		_, err := s.SaveReport(&ReportWrite{Hostname: "skew", BillingPeriod: "forever", CreatedAt: now.Unix(), Metric: identityMetric(now.Unix(), counter, counter, 1000, boot, scope)})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(bootA, 1000)
	now = now.Add(time.Minute)
	save(bootB, 100)
	now = now.Add(time.Minute)
	save(bootA, 9999999)
	var current string
	if err := s.db.QueryRow(`SELECT boot_id FROM traffic_counter_identity`).Scan(&current); err != nil || current != bootB {
		t.Fatal(current, err)
	}
	id, _, _ := s.AgentIDByHostname("skew")
	usage, _ := s.GetTrafficUsage(id)
	if usage.Sent != 100 || usage.Recv != 100 {
		t.Fatal(usage)
	}
}
