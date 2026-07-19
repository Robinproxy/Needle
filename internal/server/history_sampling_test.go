package server

import (
	"math"
	"testing"
	"time"
)

func TestHistoryBucketSeconds(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     int64
	}{
		{time.Hour, 0},
		{24 * time.Hour, 0},
		{7 * 24 * time.Hour, 900},
		{30 * 24 * time.Hour, 3600},
	}
	for _, tt := range tests {
		if got := historyBucketSeconds(tt.duration); got != tt.want {
			t.Fatalf("historyBucketSeconds(%s) = %d, want %d", tt.duration, got, tt.want)
		}
	}
}

func TestGetMetricsSampledAggregatesTimeBuckets(t *testing.T) {
	_, store := newTestHandler(t)
	agentID, err := store.UpsertAgent("node-1", "token", "SG", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	base := int64(1_700_000_040) // divisible by 120
	for i, sample := range []struct {
		offset int64
		cpu    float64
	}{{0, 10}, {30, 20}, {120, 30}, {150, 40}} {
		err := store.InsertMetric(&MetricRow{AgentID: agentID, CPUUsage: sample.cpu, MemoryTotal: 100, MemoryUsed: int64(10 * (i + 1))}, base+sample.offset)
		if err != nil {
			t.Fatal(err)
		}
	}

	metrics, err := store.GetMetricsSampled(agentID, base, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 2 {
		t.Fatalf("len(metrics) = %d, want 2", len(metrics))
	}
	if math.Abs(metrics[0].CPUUsage-15) > 0.001 || math.Abs(metrics[1].CPUUsage-35) > 0.001 {
		t.Fatalf("CPU averages = %.2f, %.2f; want 15, 35", metrics[0].CPUUsage, metrics[1].CPUUsage)
	}
	if metrics[0].CreatedAt != base+30 || metrics[1].CreatedAt != base+150 {
		t.Fatalf("bucket timestamps = %d, %d", metrics[0].CreatedAt, metrics[1].CreatedAt)
	}
}

func TestGetTCPingResultsSampledPreservesLossCounts(t *testing.T) {
	_, store := newTestHandler(t)
	agentID, err := store.UpsertAgent("node-1", "token", "SG", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	base := int64(1_700_000_040)
	samples := []struct {
		offset  int64
		latency float64
		success bool
	}{{0, 10, true}, {30, 0, false}, {120, 0, false}, {150, 0, false}}
	for _, sample := range samples {
		err := store.InsertTCPing(&TCPingRow{AgentID: agentID, Name: "CMv4", Target: "example.com:80", LatencyMs: sample.latency, Success: sample.success}, base+sample.offset)
		if err != nil {
			t.Fatal(err)
		}
	}

	results, err := store.GetTCPingResultsSampled(agentID, base, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].SampleCount != 2 || results[0].SuccessCount != 1 || !results[0].Success || results[0].LatencyMs != 10 {
		t.Fatalf("first bucket = %+v", results[0])
	}
	if results[1].SampleCount != 2 || results[1].SuccessCount != 0 || results[1].Success || results[1].LatencyMs != 0 {
		t.Fatalf("second bucket = %+v", results[1])
	}
}
