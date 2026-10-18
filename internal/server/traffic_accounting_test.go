package server

import (
	"testing"
	"time"
)

func saveTrafficSample(t *testing.T, s *Store, at time.Time, sent, recv, uptime int64) {
	t.Helper()
	s.now = func() time.Time { return at }
	expiry := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC).Unix()
	_, err := s.SaveReport(&ReportWrite{Hostname: "node-1", Region: "SG", ExpiresAt: &expiry, BillingPeriod: "1m", CreatedAt: at.Unix(), Metric: &MetricRow{TotalSent: sent, TotalRecv: recv, Uptime: uptime}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMalibuCounterRegressionDoesNotChargeLifetimeTraffic(t *testing.T) {
	now := time.Date(2026, 10, 18, 4, 7, 19, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 6)
	boundary := monthlyBoundary(6, now)
	saveTrafficSample(t, s, boundary, 403080392907, 480446842075, 5900000)
	saveTrafficSample(t, s, now, 621593958449, 711820379053, 6965412)
	saveTrafficSample(t, s, now.Add(14*time.Second), 617770990288, 708005318972, 6965426)
	saveTrafficSample(t, s, now.Add(45*time.Second), 617792170688, 708026637168, 6965457)
	// Preserve known pre-dip usage. Do not add 617/708 GB as new traffic.
	assertUsage(t, s, id, 218513565542, 231373536978)
	// A rebound does not count the temporarily missing counters again.
	saveTrafficSample(t, s, now.Add(75*time.Second), 621593958549, 711820379253, 6965487)
	assertUsage(t, s, id, 218513565642, 231373537178)
}

func TestTrafficCounterState(t *testing.T) {
	cases := []struct {
		name       string
		samples    []MetricRow
		sent, recv int64
	}{
		{"temporary dip and rebound", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100, CreatedAt: 1000}, {TotalSent: 900, TotalRecv: 1800, Uptime: 110, CreatedAt: 1010}, {TotalSent: 1010, TotalRecv: 2010, Uptime: 120, CreatedAt: 1020}}, 10, 10},
		{"persistent drop catches up without duplicates", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100, CreatedAt: 1000}, {TotalSent: 900, TotalRecv: 1900, Uptime: 110, CreatedAt: 1010}, {TotalSent: 950, TotalRecv: 1950, Uptime: 120, CreatedAt: 1020}, {TotalSent: 1020, TotalRecv: 2030, Uptime: 130, CreatedAt: 1030}}, 20, 30},
		{"only one direction drops", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100, CreatedAt: 1000}, {TotalSent: 900, TotalRecv: 2010, Uptime: 110, CreatedAt: 1010}}, 0, 10},
		{"confirmed reboot", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100, CreatedAt: 1000}, {TotalSent: 10, TotalRecv: 20, Uptime: 10, CreatedAt: 1010}, {TotalSent: 30, TotalRecv: 50, Uptime: 20, CreatedAt: 1020}}, 30, 50},
		{"reboot counts both directions even if a counter increased", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100, CreatedAt: 1000}, {TotalSent: 1200, TotalRecv: 20, Uptime: 10, CreatedAt: 1010}}, 1200, 20},
		{"missing uptime does not prove reboot", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, CreatedAt: 1000}, {TotalSent: 10, TotalRecv: 20, CreatedAt: 1010}}, 0, 0},
		{"one missing uptime does not erase reboot evidence", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100, CreatedAt: 1000}, {TotalSent: 1010, TotalRecv: 2020, CreatedAt: 1010}, {TotalSent: 10, TotalRecv: 20, Uptime: 10, CreatedAt: 1020}}, 20, 40},
		{"missing network does not reset watermarks", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100, CreatedAt: 1000}, {MissingMetrics: missingNetwork, Uptime: 110, CreatedAt: 1010}, {TotalSent: 1010, TotalRecv: 2020, Uptime: 120, CreatedAt: 1020}}, 10, 20},
		{"same-second delayed sample is not a reboot", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100, CreatedAt: 1000}, {TotalSent: 900, TotalRecv: 1800, Uptime: 90, CreatedAt: 1000}, {TotalSent: 1010, TotalRecv: 2010, Uptime: 110, CreatedAt: 1010}}, 10, 10},
		{"delayed sample is not a reboot", []MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100, CreatedAt: 1000}, {TotalSent: 900, TotalRecv: 1800, Uptime: 90, CreatedAt: 990}, {TotalSent: 1010, TotalRecv: 2010, Uptime: 110, CreatedAt: 1010}}, 10, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := tc.samples[0]
			state := trafficCounterState{Sent: first.TotalSent, Recv: first.TotalRecv, Uptime: first.Uptime, SampleAt: first.CreatedAt}
			var sent, recv int64
			for i := range tc.samples[1:] {
				ds, dr := state.consume(&tc.samples[i+1])
				sent += ds
				recv += dr
			}
			if sent != tc.sent || recv != tc.recv {
				t.Fatalf("got %d/%d, want %d/%d", sent, recv, tc.sent, tc.recv)
			}
		})
	}
}

func TestTrafficRegressionWatermarkSurvivesReopen(t *testing.T) {
	now := time.Date(2026, 10, 18, 4, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 6)
	saveTrafficSample(t, s, now, 1000, 2000, 100)
	saveTrafficSample(t, s, now.Add(30*time.Second), 1100, 2200, 130)
	saveTrafficSample(t, s, now.Add(60*time.Second), 900, 1900, 160)
	path := s.path
	s.Close()
	s, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	saveTrafficSample(t, s, now.Add(90*time.Second), 1110, 2210, 190)
	assertUsage(t, s, id, 110, 210)
	saveTrafficSample(t, s, now.Add(120*time.Second), 10, 20, 10)
	assertUsage(t, s, id, 120, 230)
}

func TestTrafficFallbackAndInitialSeedDoNotChargeRegressedCounters(t *testing.T) {
	now := time.Date(2026, 10, 18, 4, 0, 0, 0, time.UTC)
	s, id := newTrafficTestStore(t, now, 6)
	boundary := monthlyBoundary(6, now)
	insertTrafficMetric(t, s, id, boundary, 5000, 6000)
	if err := s.EnsureTrafficBaseline(id, boundary.Unix()); err != nil {
		t.Fatal(err)
	}
	insertTrafficMetric(t, s, id, now.Add(-time.Minute), 4000, 4500)
	assertUsage(t, s, id, 0, 0)
	saveTrafficSample(t, s, now, 4000, 4500, 100)
	assertUsage(t, s, id, 0, 0)
	saveTrafficSample(t, s, now.Add(30*time.Second), 5010, 6010, 130)
	assertUsage(t, s, id, 10, 10)
}
