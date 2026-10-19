package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"needle/internal/agent/collector"
)

func TestProbeConfigurationBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  Config
		invalid bool
	}{
		{"defaults", Config{}, false},
		{"oversized interval", Config{Interval: 86401}, true},
		{"invalid period", Config{BillingPeriod: "always"}, true},
		{"invalid port", Config{TCPing: []collector.TCPingTarget{{Name: "test", Target: "host:0"}}}, true},
		{"valid IPv6", Config{TCPing: []collector.TCPingTarget{{Name: "test", Target: "[2001:db8::1]:443"}}}, false},
		{"duplicate", Config{TCPing: []collector.TCPingTarget{{Name: "test", Target: "host:80"}, {Name: "test", Target: "host:443"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.config.validateProbesAndInterval(); (err != nil) != tc.invalid {
				t.Fatalf("invalid=%v err=%v", tc.invalid, err)
			}
		})
	}
	c := Config{}
	for i := 0; i < 33; i++ {
		c.TCPing = append(c.TCPing, collector.TCPingTarget{Name: fmt.Sprintf("target-%d", i), Target: "host:443"})
	}
	if c.validateProbesAndInterval() == nil {
		t.Fatal("unbounded probe concurrency")
	}
	c.TCPing = c.TCPing[:32]
	if err := c.validateProbesAndInterval(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckConfigHelper(t *testing.T) {
	if os.Getenv("NEEDLE_CONFIG_CHECK_HELPER") != "1" {
		return
	}
	path := os.Args[len(os.Args)-1]
	flag.CommandLine = flag.NewFlagSet("needle-agent", flag.ExitOnError)
	os.Args = []string{"needle-agent", "-check-config", path}
	main()
}

func TestCheckConfigExitsWithoutCollectingOrContactingServer(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		invalid      bool
	}{
		{"valid", "server: https://does-not-exist.invalid\ntoken: test\ninterval: 30\nbilling_period: forever\n", false},
		{"bad URL", "server: wrong\ntoken: test\n", true},
		{"bad YAML", "server: [\n", true},
		{"missing token", "server: https://does-not-exist.invalid\n", true},
		{"bad date", "server: https://does-not-exist.invalid\ntoken: test\nexpires_at: invalid\nbilling_period: 1m\n", true},
		{"two year paid", "server: https://does-not-exist.invalid\ntoken: test\nbilling_period: 24m\nbilling_currency: CAD\nbilling_amount: '90.10'\nexpires_at: '2028-10-15'\nmonthly_traffic_limit_gb: 0\n", false},
		{"bad amount", "server: https://does-not-exist.invalid\ntoken: test\nbilling_period: 1m\nbilling_amount: -5\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCheckConfigHelper$", "--", path)
			cmd.Env = append(os.Environ(), "NEEDLE_CONFIG_CHECK_HELPER=1")
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || (err != nil) != tc.invalid {
				t.Fatalf("err=%v timeout=%v output=%s", err, ctx.Err(), out)
			}
			if !tc.invalid && !strings.Contains(string(out), "Configuration is valid.") {
				t.Fatalf("missing validation result: %s", out)
			}
		})
	}
}
