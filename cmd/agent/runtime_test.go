package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	agentpkg "needle/internal/agent"
)

func TestAgentRuntimeHelper(t *testing.T) {
	if os.Getenv("NEEDLE_RUNTIME_HELPER") != "1" {
		return
	}
	os.Args = []string{"needle-agent", os.Getenv("NEEDLE_RUNTIME_CONFIG")}
	flag.CommandLine = flag.NewFlagSet("needle-agent", flag.ExitOnError)
	main()
}

func TestNonRootAgentStartsReportsAndRestarts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("run the Agent lifecycle as a non-root user")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var loopback string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			loopback = iface.Name
			break
		}
	}
	if loopback == "" {
		t.Fatal("loopback interface unavailable")
	}
	reports := make(chan agentpkg.ReportData, 20)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/report-capabilities" {
			fmt.Fprint(w, `{"protocol":"persistent-report-v1"}`)
			return
		}
		if r.URL.Path != "/api/report" || r.Header.Get("Authorization") != "Bearer isolated-runtime-token" {
			http.Error(w, "invalid report identity", http.StatusUnauthorized)
			return
		}
		var report agentpkg.ReportData
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reports <- report
		json.NewEncoder(w).Encode(map[string]string{"protocol": "persistent-report-v1", "report_id": report.ReportID})
	}))
	defer receiver.Close()
	config := filepath.Join(t.TempDir(), "agent.yaml")
	contents := fmt.Sprintf("hostname: runtime-test\nserver: %s\ntoken: isolated-runtime-token\ninterval: 1\nbilling_period: forever\ninterfaces: [%q]\ntcpping:\n  - name: loopback\n    target: %q\n", receiver.URL, loopback, strings.TrimPrefix(receiver.URL, "http://"))
	if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	for start := 0; start < 2; start++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentRuntimeHelper$")
		cmd.Env = append(os.Environ(), "NEEDLE_RUNTIME_HELPER=1", "NEEDLE_RUNTIME_CONFIG="+config)
		var logs bytes.Buffer
		cmd.Stdout, cmd.Stderr = &logs, &logs
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		var report agentpkg.ReportData
	warm:
		for {
			select {
			case report = <-reports:
				if report.CPU != nil && report.Network != nil {
					break warm
				}
			case <-ctx.Done():
				cmd.Wait()
				cancel()
				t.Fatalf("Agent did not report: %s", logs.String())
			}
		}
		cmd.Process.Signal(syscall.SIGTERM)
		err = cmd.Wait()
		cancel()
		if err != nil {
			t.Fatalf("Agent failed graceful shutdown: %v\n%s", err, logs.String())
		}
		if report.CPU == nil || report.CPU.Cores < 1 || report.Memory == nil || report.Memory.Total == 0 ||
			report.Disk == nil || report.Disk.Total == 0 || report.Network == nil || report.Load == nil || report.Uptime == 0 {
			t.Fatalf("missing basic metrics: %+v\n%s", report, logs.String())
		}
		if len(report.TCPing) != 1 || !report.TCPing[0].Success {
			t.Fatalf("unprivileged TCP Ping failed: %+v", report.TCPing)
		}
		if strings.Contains(logs.String(), "collect ") || strings.Contains(logs.String(), "report failed:") {
			t.Fatalf("Agent reported collection/transport errors: %s", logs.String())
		}
		if actual, err := os.ReadFile(config); err != nil || string(actual) != contents {
			t.Fatalf("Agent modified configuration: %v", err)
		}
	}
}
