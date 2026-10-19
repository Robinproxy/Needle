package server

import (
	"testing"
	"time"
)

func TestAgentDateWireEncodingUsesLocalCalendarDate(t *testing.T) {
	west, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range []*time.Location{time.UTC, west, time.FixedZone("UTC-12", -12*3600), time.FixedZone("UTC+12", 12*3600), time.FixedZone("UTC+14", 14*3600)} {
		for _, hour := range []int{0, 12} {
			expiry := time.Date(2026, 11, 6, hour, 0, 0, 0, time.UTC).Unix()
			now := time.Date(2026, 11, 5, 23, 59, 0, 0, loc)
			days, date := calcRenewalDue(expiry, "1m", now)
			if days != 1 || date != "2026-11-06" {
				t.Fatalf("%s hour=%d: due %d/%s", loc, hour, days, date)
			}
			now = now.Add(time.Minute)
			days, date = calcRenewalDue(expiry, "1m", now)
			if days != 0 || date != "2026-11-06" {
				t.Fatalf("%s hour=%d: renewal day must remain due today: %d/%s", loc, hour, days, date)
			}
			day, ok := trafficResetDay("1m", &expiry)
			if !ok || day != 6 || monthlyBoundary(day, now).Unix() != now.Unix() {
				t.Fatalf("%s hour=%d: traffic and due day disagree", loc, hour)
			}
			now = now.AddDate(0, 0, 1)
			days, date = calcRenewalDue(expiry, "1m", now)
			if days != 29 || date != "2026-12-06" {
				t.Fatalf("%s hour=%d: next-day monthly renewal did not advance: %d/%s", loc, hour, days, date)
			}
		}
	}
}

func noonTransitionStore(t *testing.T) (*Store, int64, *int64, time.Time) {
	t.Helper()
	loc := time.FixedZone("UTC+14", 14*3600)
	previous := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = previous })
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, loc)
	s, id := newTrafficTestStore(t, now, 6)
	expiry := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC).Unix() // actual Agent wire
	execAuditSQL(t, s, "UPDATE agents SET expires_at=? WHERE id=?", expiry, id)
	old := monthlyBoundary(7, now).Unix() // old server shifted UTC noon to the 7th
	execAuditSQL(t, s, `INSERT INTO traffic_baselines(agent_id,boundary,total_sent,total_recv,usage_sent,usage_recv,last_sent,last_recv,last_uptime,last_sample_at,created_at,accounting_version)
	 VALUES(?,?,100,200,400,600,500,800,1000,?,?,1)`, id, old, now.Add(-time.Minute).Unix(), now.Unix())
	execAuditSQL(t, s, "DELETE FROM accounting_migrations WHERE name='calendar_dates'")
	if err := s.migrateAccountingAudit(); err != nil {
		t.Fatal(err)
	}
	return s, id, &expiry, now
}

func TestNoonDateMigrationSurvivesRenewalUntilCorrectedReset(t *testing.T) {
	s, id, expiry, now := noonTransitionStore(t)
	assertUsage(t, s, id, 400, 600)
	// A renewal and payment-period change do not change monthly reset day 6.
	renewed := time.Date(2026, 12, 6, 12, 0, 0, 0, time.UTC).Unix()
	if trafficResetDayValue := billingAnchor(*expiry, now.Location()).Day(); trafficResetDayValue != 6 {
		t.Fatal("invalid fixture")
	}
	save := func(at time.Time, sent, recv, uptime int64) {
		s.now = func() time.Time { return at }
		_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", ExpiresAt: &renewed, BillingPeriod: "3m", CreatedAt: at.Unix(), Metric: &MetricRow{TotalSent: sent, TotalRecv: recv, Uptime: uptime}})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(now.Add(time.Minute), 510, 820, 1060)
	assertUsage(t, s, id, 410, 620)
	if err := s.migrateAccountingAudit(); err != nil {
		t.Fatal(err)
	}
	assertUsage(t, s, id, 410, 620)
	save(time.Date(2026, 11, 5, 23, 59, 0, 0, now.Location()), 600, 900, 2000000)
	assertUsage(t, s, id, 500, 700)
	save(time.Date(2026, 11, 6, 0, 0, 0, 0, now.Location()), 610, 920, 2000060)
	assertUsage(t, s, id, 0, 0)
	save(time.Date(2026, 11, 6, 0, 1, 0, 0, now.Location()), 620, 940, 2000120)
	assertUsage(t, s, id, 10, 20)
}

func TestChangingResetDayDoesNotReviveTransitionWhenChangedBack(t *testing.T) {
	s, id, expiry, now := noonTransitionStore(t)
	changed := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).Unix()
	save := func(date *int64, sent, recv int64) {
		now = now.Add(time.Minute)
		s.now = func() time.Time { return now }
		_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", ExpiresAt: date, BillingPeriod: "1m", CreatedAt: now.Unix(), Metric: &MetricRow{TotalSent: sent, TotalRecv: recv, Uptime: 2000 + sent}})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(&changed, 600, 900)
	if auditCount(t, s, "traffic_cycle_transition") != 0 {
		t.Fatal("reset-day change retained an obsolete transition")
	}
	save(expiry, 610, 920)
	assertUsage(t, s, id, 10, 20)
}

func TestTrafficCycleEndClampsOriginalDayInsteadOfDrifting(t *testing.T) {
	now := time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 31)
	expiry := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC).Unix()
	cycle, configured, err := activeTrafficCycle(s.db, id, "1m", &expiry, now)
	if err != nil || !configured || cycle.Start != time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC).Unix() || cycle.End != time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("clamped cycle drifted: %+v configured=%v err=%v", cycle, configured, err)
	}
}
