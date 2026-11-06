package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentpkg "needle/internal/agent"
	"needle/internal/agent/collector"
	"needle/internal/probe"
	"needle/internal/server"
)

func TestProbeConfigurationMatchesServerNameKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		valid bool
	}{
		{"standard", []string{"CTv4", "CTv6"}, true},
		{"unique padding and internal spaces", []string{" CTv4 ", "My VPS's route"}, true},
		{"Unicode padding", []string{"\u00a0路由\u3000", "CTv6"}, true},
		{"UTF-8 byte boundary", []string{strings.Repeat("线", 21)}, true},
		{"ASCII collision", []string{"CTv4", " CTv4 "}, false},
		{"Unicode collision", []string{"CTv4", "\u00a0CTv4\u3000"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{}
			data := &agentpkg.ReportData{
				Hostname: "name-contract", CreatedAt: time.Now().Unix(),
				ReportID: "0123456789abcdef0123456789abcdef:1",
				CPU:      &collector.CPUStats{Percent: 10},
			}
			for _, name := range tc.names {
				cfg.TCPing = append(cfg.TCPing, collector.TCPingTarget{Name: name, Target: "example.com:80"})
				data.TCPing = append(data.TCPing, collector.TCPingResult{Name: name, Target: "example.com:80", Success: true, LatencyMs: 10})
			}
			if err := cfg.validateProbesAndInterval(); (err == nil) != tc.valid {
				t.Fatalf("Agent validation: valid=%t err=%v", tc.valid, err)
			}
			for i, target := range cfg.TCPing {
				if target.Name != tc.names[i] {
					t.Fatal("validation rewrote a wire name")
				}
			}
			store, err := server.NewStoreCLI(filepath.Join(t.TempDir(), "needle.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AllowToken("name-contract-token"); err != nil {
				t.Fatal(err)
			}
			h := server.NewHandler(store)
			defer h.Close()
			mux := http.NewServeMux()
			h.Register(mux)
			body, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			send := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/api/report", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer name-contract-token")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				return w
			}
			w := send()
			if !tc.valid {
				if w.Code != 400 {
					t.Fatalf("Server failed to reject collision: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("Agent accepted names rejected by Server: %d %s", w.Code, w.Body.String())
			}
			if retry := send(); retry.Code != 200 || !strings.Contains(retry.Body.String(), `"duplicate":true`) {
				t.Fatalf("frozen report retry failed: %d %s", retry.Code, retry.Body.String())
			}
			id, found, err := store.AgentIDByHostname(data.Hostname)
			if err != nil || !found {
				t.Fatalf("missing node: %v", err)
			}
			metrics, err := store.GetMetricsWindowSampled(context.Background(), id, 0, 0, 0)
			if err != nil || len(metrics) != 1 || metrics[0].CPUUsage != 10 {
				t.Fatalf("probe names broke history/deduplication: %+v / %v", metrics, err)
			}
			latest, err := store.GetLatestTCPing(id)
			if err != nil || len(latest) != len(tc.names) {
				t.Fatalf("probe names lost results: %+v / %v", latest, err)
			}
			want := make(map[string]bool)
			for _, name := range tc.names {
				want[probe.NormalizeName(name)] = true
			}
			for _, result := range latest {
				if !want[result.Name] {
					t.Fatalf("unexpected stored name: %q", result.Name)
				}
			}
		})
	}
}
