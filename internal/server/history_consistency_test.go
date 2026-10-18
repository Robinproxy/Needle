package server

import (
	"testing"
	"time"
)

func TestTCPingAggregatesAgreeAcrossSourcesAndBatches(t *testing.T) {
	type sample struct {
		latency float64
		success bool
	}
	type summary struct {
		mean, peak, loss   float64
		samples, successes int64
	}
	tests := []struct {
		name    string
		batches [][]sample
		want    []summary
	}{
		{
			name: "successful latency excludes failed attempts",
			batches: [][]sample{
				{{10, true}, {30, true}, {5000, false}},
				{{80, true}, {6000, false}},
			},
			want: []summary{{20, 30, 1.0 / 3, 3, 2}, {40, 80, 0.4, 5, 3}},
		},
		{
			name:    "all failed attempts have no latency",
			batches: [][]sample{{{5000, false}}, {{1000, false}}},
			want:    []summary{{0, 0, 1, 1, 0}, {0, 0, 1, 2, 0}},
		},
		{
			name: "zero latency is a successful sample",
			batches: [][]sample{
				{{5000, false}},
				{{0, true}, {6000, false}},
				{{30, true}, {60, true}},
			},
			want: []summary{{0, 0, 1, 1, 0}, {0, 0, 2.0 / 3, 3, 1}, {30, 60, 0.4, 5, 3}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			store, agentID := newTrafficTestStore(t, now, 6)
			hourStart := now.Add(-time.Hour)
			at := hourStart
			check := func(want summary) {
				t.Helper()
				raw, err := store.GetTCPingResultsWindowSampled(agentID, hourStart.Unix(), 0, 3600)
				if err != nil {
					t.Fatal(err)
				}
				hourly, err := store.GetTCPingHourly(agentID, hourStart.Unix(), 0)
				if err != nil {
					t.Fatal(err)
				}
				for source, rows := range map[string][]TCPingRow{"raw": raw, "hourly": hourly} {
					if len(rows) != 1 {
						t.Fatalf("%s rows = %+v, want one summary", source, rows)
					}
					row := rows[0]
					loss := float64(row.SampleCount-row.SuccessCount) / float64(row.SampleCount)
					if row.LatencyMs != want.mean || row.LatencyPeak != want.peak ||
						row.SampleCount != want.samples || row.SuccessCount != want.successes ||
						row.Success != (want.successes > 0) || loss != want.loss {
						t.Fatalf("%s summary = %+v (loss %g), want %+v", source, row, loss, want)
					}
				}
			}
			for batch, samples := range tt.batches {
				for _, sample := range samples {
					insertPingRaw(t, store, agentID, at, "CMv4", sample.latency, sample.success)
					at = at.Add(30 * time.Second)
				}
				mustRollUpHourly(t, store)
				check(tt.want[batch])
				mustRollUpHourly(t, store)
				check(tt.want[batch])
			}
		})
	}
}
