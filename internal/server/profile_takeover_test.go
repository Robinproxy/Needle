package server

import (
	"reflect"
	"testing"
	"time"

	"needle/internal/billing"
)

func TestModernAgentTakesOverExistingProfileWithoutClearingSettings(t *testing.T) {
	for _, period := range []string{"12m", "forever", ""} {
		t.Run(period, func(t *testing.T) {
			_, s := newAdminTest(t)
			now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return now }
			var expiry *int64
			if period == "12m" {
				value := time.Date(2027, 3, 3, 12, 0, 0, 0, time.UTC).Unix()
				expiry = &value
			}
			legacy := func(sent, recv int64) *ReportWrite {
				var cost *billing.Cost
				if period != "" {
					cost = &billing.Cost{Currency: "USD", Amount: "35"}
				}
				return &ReportWrite{Hostname: "existing", Region: "US", BillingPeriod: period, ExpiresAt: expiry,
					BillingCost: cost, MonthlyTrafficLimit: 4e12,
					CreatedAt: now.Unix(), Metric: &MetricRow{TotalSent: sent, TotalRecv: recv, Uptime: now.Unix(), CreatedAt: now.Unix()}}
			}
			id, err := s.SaveReport(legacy(100, 200))
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			if _, err := s.SaveReport(legacy(150, 300)); err != nil {
				t.Fatal(err)
			}
			before, _ := s.GetAgent(id)
			usage, _ := s.GetTrafficUsage(id)
			profiles, _, _ := s.NodeProfiles()
			staleEditor := profiles[0].NodeProfileInput
			now = now.Add(time.Minute)
			modern := &ReportWrite{Hostname: "existing", ServerManaged: true, CreatedAt: now.Unix(),
				Metric: &MetricRow{TotalSent: 150, TotalRecv: 300, Uptime: now.Unix()}}
			if _, err := s.SaveReport(modern); err != nil {
				t.Fatal(err)
			}
			after, _ := s.GetAgent(id)
			wantDay := 1
			if period == "12m" {
				wantDay = 3
			}
			if !after.ProfileManaged || after.ProfileRevision != before.ProfileRevision+1 || after.TrafficResetDay == nil || *after.TrafficResetDay != wantDay ||
				after.Region != before.Region || after.BillingPeriod != before.BillingPeriod || after.MonthlyTrafficLimit != before.MonthlyTrafficLimit ||
				!reflect.DeepEqual(after.BillingCost, before.BillingCost) || !reflect.DeepEqual(after.ExpiresAt, before.ExpiresAt) {
				t.Fatalf("modern report cleared old settings: before=%+v after=%+v", before, after)
			}
			afterUsage, _ := s.GetTrafficUsage(id)
			if period != "" && !reflect.DeepEqual(usage, afterUsage) {
				t.Fatalf("takeover changed accumulated usage: before=%+v after=%+v", usage, afterUsage)
			}
			if period == "" && !afterUsage.Available {
				t.Fatal("unconfigured legacy node did not start its independent monthly cycle")
			}
			if err := s.UpdateNodeProfile(id, staleEditor, "robin"); err != ErrProfileConflict {
				t.Fatal("pre-takeover editor was accepted", err)
			}
			// A delayed old Agent report still contributes history, without
			// undoing ownership, billing or the pinned reset day.
			old := legacy(140, 290)
			old.Region, old.BillingPeriod, old.ExpiresAt = "CN", "forever", nil
			old.CreatedAt = now.Add(-time.Minute).Unix()
			if _, err := s.SaveReport(old); err != nil {
				t.Fatal(err)
			}
			delayed, _ := s.GetAgent(id)
			if delayed.ProfileRevision != after.ProfileRevision || delayed.Region != after.Region || delayed.BillingPeriod != after.BillingPeriod ||
				!reflect.DeepEqual(delayed.ExpiresAt, after.ExpiresAt) || *delayed.TrafficResetDay != wantDay {
				t.Fatal("delayed report rolled back managed profile", delayed)
			}
			profiles, _, _ = s.NodeProfiles()
			p := profiles[0].NodeProfileInput
			p.Note = "Managed after upgrade"
			if err := s.UpdateNodeProfile(id, p, "robin"); err != nil {
				t.Fatal("transferred profile could not be edited", err)
			}
			now = now.Add(time.Minute)
			modern.CreatedAt = now.Unix()
			modern.Metric.Uptime = now.Unix()
			if _, err := s.SaveReport(modern); err != nil {
				t.Fatal(err)
			}
			updated, _ := s.GetAgent(id)
			if updated.Note != p.Note || updated.ProfileRevision != after.ProfileRevision+1 {
				t.Fatal("subsequent modern report took over again", updated)
			}
			currentUsage, _ := s.GetTrafficUsage(id)
			s.Close()
			reopened, err := NewStoreCLI(s.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.now = func() time.Time { return now }
			persisted, err := reopened.GetAgent(id)
			if err != nil || !reflect.DeepEqual(updated, persisted) {
				t.Fatalf("automatic takeover did not survive restart: before=%+v after=%+v err=%v", updated, persisted, err)
			}
			persistedUsage, err := reopened.GetTrafficUsage(id)
			if err != nil || !reflect.DeepEqual(currentUsage, persistedUsage) {
				t.Fatalf("restart changed takeover usage: before=%+v after=%+v err=%v", currentUsage, persistedUsage, err)
			}
		})
	}
}

func TestDelayedModernReportDoesNotTakeOverOrChangeCurrentProfile(t *testing.T) {
	_, s := newAdminTest(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	id, err := s.SaveReport(&ReportWrite{Hostname: "existing", Region: "US", BillingPeriod: "forever", CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetAgent(id)
	if _, err := s.SaveReport(&ReportWrite{Hostname: "existing", ServerManaged: true, CreatedAt: now.Add(-time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetAgent(id)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("delayed modern report changed current profile: before=%+v after=%+v", before, after)
	}
}

func TestModernAgentTakeoverRollsBackWhenReportFails(t *testing.T) {
	_, s := newAdminTest(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	id, err := s.SaveReport(&ReportWrite{Hostname: "existing", Region: "US", BillingPeriod: "forever", CreatedAt: now.Unix(),
		Metric: &MetricRow{TotalSent: 100, TotalRecv: 200, Uptime: 100}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetAgent(id)
	usage, _ := s.GetTrafficUsage(id)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_takeover_sample BEFORE INSERT ON metrics BEGIN SELECT RAISE(ABORT,'forced report failure'); END`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := s.SaveReport(&ReportWrite{Hostname: "existing", ServerManaged: true, CreatedAt: now.Unix(),
		Metric: &MetricRow{TotalSent: 150, TotalRecv: 300, Uptime: 160}}); err == nil {
		t.Fatal("injected report failure succeeded")
	}
	after, _ := s.GetAgent(id)
	afterUsage, _ := s.GetTrafficUsage(id)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(usage, afterUsage) {
		t.Fatalf("failed report partially transferred a profile or changed usage: before=%+v after=%+v usage=%+v/%+v", before, after, usage, afterUsage)
	}
}
