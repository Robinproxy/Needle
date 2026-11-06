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
		{"invalid port", Config{TCPing: []collector.TCPingTarget{{Name: "test", Target: "host:0"}}}, true},
		{"valid IPv6", Config{TCPing: []collector.TCPingTarget{{Name: "test", Target: "[2001:db8::1]:443"}}}, false},
		{"duplicate", Config{TCPing: []collector.TCPingTarget{{Name: "test", Target: "host:80"}, {Name: "test", Target: "host:443"}}}, true},
		{"ASCII padding duplicate", Config{TCPing: []collector.TCPingTarget{{Name: "CTv4", Target: "host:80"}, {Name: " CTv4 ", Target: "host:443"}}}, true},
		{"Unicode padding duplicate", Config{TCPing: []collector.TCPingTarget{{Name: "CTv4", Target: "host:80"}, {Name: "\u00a0CTv4\u3000", Target: "host:443"}}}, true},
		{"case sensitive", Config{TCPing: []collector.TCPingTarget{{Name: "CTv4", Target: "host:80"}, {Name: "ctv4", Target: "host:443"}}}, false},
		{"unique padded name", Config{TCPing: []collector.TCPingTarget{{Name: " CTv4 ", Target: "host:80"}}}, false},
		{"64 bytes", Config{TCPing: []collector.TCPingTarget{{Name: strings.Repeat("a", 64), Target: "host:80"}}}, false},
		{"65 bytes", Config{TCPing: []collector.TCPingTarget{{Name: strings.Repeat("a", 65), Target: "host:80"}}}, true},
		{"UTF-8 within byte limit", Config{TCPing: []collector.TCPingTarget{{Name: strings.Repeat("线", 21), Target: "host:80"}}}, false},
		{"UTF-8 above byte limit", Config{TCPing: []collector.TCPingTarget{{Name: strings.Repeat("线", 22), Target: "host:80"}}}, true},
		{"invalid UTF-8", Config{TCPing: []collector.TCPingTarget{{Name: "bad\xffname", Target: "host:80"}}}, true},
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
	for _, name := range []string{" ", "\u00a0\u3000", "C\tTv4", "CT\x00v4", "CTv4\n", "CT\x1bv4", "CT\x7fv4", "CT\u0085v4"} {
		if err := (Config{TCPing: []collector.TCPingTarget{{Name: name, Target: "host:80"}}}).validateProbesAndInterval(); err == nil {
			t.Fatalf("blank/control name accepted: %q", name)
		}
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
		{"valid", "server: https://does-not-exist.invalid\ntoken: test\ninterval: 30\n", false},
		{"bad URL", "server: wrong\ntoken: test\n", true},
		{"bad YAML", "server: [\n", true},
		{"missing token", "server: https://does-not-exist.invalid\n", true},
		{"removed expiry", "server: https://does-not-exist.invalid\ntoken: test\nexpires_at: 2027-01-01\n", true},
		{"managed", "server: https://does-not-exist.invalid\ntoken: test\nserver_managed: true\n", false},
		{"removed insecure", "server: https://does-not-exist.invalid\ntoken: test\ninsecure: false\n", true},
		{"normalized probe duplicate", "server: https://does-not-exist.invalid\ntoken: test\ntcpping:\n  - {name: CTv4, target: 'example.com:80'}\n  - {name: ' CTv4 ', target: 'other.example:443'}\n", true},
		{"Unicode probe duplicate", "server: https://does-not-exist.invalid\ntoken: test\ntcpping:\n  - {name: CTv4, target: 'example.com:80'}\n  - {name: '\u00a0CTv4\u3000', target: 'other.example:443'}\n", true},
		{"probe control", "server: https://does-not-exist.invalid\ntoken: test\ntcpping:\n  - {name: \"C\\tTv4\", target: 'example.com:80'}\n", true},
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
