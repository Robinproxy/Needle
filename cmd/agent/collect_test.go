package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"needle/internal/agent/collector"
	"needle/internal/logging"
	"needle/internal/server"
)

func TestCollectionFailuresKeepIndependentMetricsAndProbes(t *testing.T) {
	receiver := httptest.NewServer(http.NotFoundHandler())
	defer receiver.Close()
	targets := []collector.TCPingTarget{{Name: "CMv4", Target: receiver.Listener.Addr().String()}}
	probe := newProbeSampler(targets)
	for _, failed := range []string{"", "cpu", "memory", "disk", "network", "load", "uptime", "all"} {
		t.Run("failed_"+failed, func(t *testing.T) {
			var calls []string
			failure := func(name string) error {
				calls = append(calls, name)
				if failed == name || failed == "all" {
					return errors.New("injected failure")
				}
				return nil
			}
			var logs []string
			logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
			c := reportCollectors{
				cpu: func() (*collector.CPUStats, error) {
					return &collector.CPUStats{Percent: 25, Cores: 4}, failure("cpu")
				},
				memory: func() (*collector.MemoryStats, error) {
					return &collector.MemoryStats{Total: 100, Used: 50}, failure("memory")
				},
				disk: func() (*collector.DiskStats, error) {
					return &collector.DiskStats{Total: 200, Used: 70}, failure("disk")
				},
				network: func() (*collector.NetworkStats, error) {
					return &collector.NetworkStats{Up: 10, Down: 20, TotalSent: 1000, TotalRecv: 2000}, failure("network")
				},
				load: func() (*collector.LoadStats, error) {
					return &collector.LoadStats{Load1: 1}, failure("load")
				},
				uptime: func() (uint64, error) { return 100, failure("uptime") },
			}
			report := collectReport(c, logging.NewTracker("collect", logf))
			if report.CreatedAt <= 0 || report.CreatedAt > time.Now().Unix() {
				t.Fatalf("sample timestamp unavailable: %d", report.CreatedAt)
			}
			if strings.Join(calls, ",") != "cpu,memory,disk,network,load,uptime" {
				t.Fatalf("one collector failure skipped another collector: %v", calls)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ready, err := probe(ctx, report)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("asynchronous probes did not complete")
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"cpu", "memory", "disk", "network", "load"} {
				wantUnavailable := failed == name || failed == "all"
				if got := string(wire[name]) == "null"; got != wantUnavailable {
					t.Fatalf("%s unavailable=%v, want %v: %s", name, got, wantUnavailable, encoded)
				}
			}
			if (report.Uptime == 0) != (failed == "uptime" || failed == "all") {
				t.Fatalf("failed uptime must be unknown, got %d", report.Uptime)
			}
			if failed != "network" && failed != "all" && (report.Network.TotalSent != 1000 || report.Network.TotalRecv != 2000) {
				t.Fatalf("independent failure changed network counters: %+v", report.Network)
			}
			if len(report.TCPing) != 1 || report.TCPing[0].Name != targets[0].Name || report.TCPing[0].Target != targets[0].Target || !report.TCPing[0].Success || report.TCPing[0].LatencyMs < 0 {
				t.Fatalf("metrics failure lost probes: %+v", report.TCPing)
			}
			wantLogs := 0
			if failed != "" {
				wantLogs = 1
			}
			if failed == "all" {
				wantLogs = 6
			}
			if len(logs) != wantLogs {
				t.Fatalf("failure logs=%v, want %d entries", logs, wantLogs)
			}
		})
	}
}

func TestCollectorLoggingRetainsFailuresUntilAValidSample(t *testing.T) {
	var lines []string
	logs := logging.NewTracker("collect.history", func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	for i := 0; i < 20; i++ {
		got := collectValue("cpu", func() (int, error) { return 42, errors.New("permission denied") }, logs)
		if got != 0 {
			t.Fatal("failed collector returned a healthy value")
		}
	}
	collectValue("cpu", func() (int, error) { return 0, collector.ErrBaseline }, logs)
	if len(lines) != 1 {
		t.Fatalf("failures flooded logs or a baseline falsely recovered: %v", lines)
	}
	if got := collectValue("cpu", func() (int, error) { return 42, nil }, logs); got != 42 {
		t.Fatal("valid sample changed")
	}
	collectValue("cpu", func() (int, error) { return 42, nil }, logs)
	if len(lines) != 2 || !strings.Contains(lines[1], "event=recovered") || !strings.Contains(lines[1], "count=20 suppressed=19") {
		t.Fatalf("missing recovery: %v", lines)
	}
}

func TestPartialCollectionWireReportKeepsTrafficAccounting(t *testing.T) {
	store, err := server.NewStoreCLI(filepath.Join(t.TempDir(), "needle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AllowToken("partial-wire"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server.NewHandler(store).Register(mux)
	sent, recv := int64(1000), int64(2000)
	c := reportCollectors{
		cpu:    func() (*collector.CPUStats, error) { return nil, errors.New("cpu unavailable") },
		memory: func() (*collector.MemoryStats, error) { return &collector.MemoryStats{Total: 100, Used: 50}, nil },
		disk:   func() (*collector.DiskStats, error) { return &collector.DiskStats{Total: 200, Used: 100}, nil },
		network: func() (*collector.NetworkStats, error) {
			return &collector.NetworkStats{TotalSent: sent, TotalRecv: recv}, nil
		},
		load:   func() (*collector.LoadStats, error) { return &collector.LoadStats{Load1: .1}, nil },
		uptime: func() (uint64, error) { return uint64(sent), nil },
	}
	for i := 0; i < 2; i++ {
		report := collectReport(c, logging.NewTracker("collect", func(string, ...any) {}))
		report.Hostname = "partial-wire"
		report.BillingPeriod = "forever"
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/report", bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer partial-wire")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("report failed: %d %s", w.Code, w.Body.String())
		}
		sent += 50
		recv += 70
	}
	agents, err := store.GetAgents()
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents %+v %v", agents, err)
	}
	usage, err := store.GetTrafficUsage(agents[0].ID)
	if err != nil || !usage.HasData || usage.Sent != 50 || usage.Recv != 70 {
		t.Fatalf("partial collection lost traffic: %+v %v", usage, err)
	}
	latest, err := store.GetLatestMetric(agents[0].ID)
	if err != nil || latest.MissingMetrics&1 == 0 {
		t.Fatalf("missing CPU became a healthy zero: %+v %v", latest, err)
	}
}
