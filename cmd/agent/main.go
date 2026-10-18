package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	agentpkg "needle/internal/agent"
	"needle/internal/agent/collector"

	"gopkg.in/yaml.v3"
)

type Config struct {
	MonthlyTrafficLimitGB float64                  `yaml:"monthly_traffic_limit_gb"`
	Hostname              string                   `yaml:"hostname"`
	Server                string                   `yaml:"server"`
	Token                 string                   `yaml:"token"`
	Region                string                   `yaml:"region"`
	ExpiresAt             string                   `yaml:"expires_at"`
	BillingPeriod         string                   `yaml:"billing_period"`
	Interval              int                      `yaml:"interval"`
	TLSSkipVerify         bool                     `yaml:"tls_skip_verify"`
	AllowPlainHTTP        bool                     `yaml:"allow_plain_http"`
	Insecure              *bool                    `yaml:"insecure"` // Deprecated: use tls_skip_verify.
	Interfaces            []string                 `yaml:"interfaces"`
	TCPing                []collector.TCPingTarget `yaml:"tcpping"`
}

func (c Config) effectiveTLSSkipVerify() bool {
	return c.TLSSkipVerify || (c.Insecure != nil && *c.Insecure)
}

// Quotas use decimal GB, matching providers' traffic allowances. Zero is unset.
func (c Config) monthlyTrafficLimitBytes() (int64, error) {
	v := c.MonthlyTrafficLimitGB
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 9_000_000 {
		return 0, fmt.Errorf("monthly_traffic_limit_gb must be between 0 and 9000000")
	}
	return int64(math.Round(v * 1_000_000_000)), nil
}

func (c Config) expiryUnix() (*int64, error) {
	if c.ExpiresAt == "" || c.BillingPeriod == "forever" {
		return nil, nil
	}
	date, err := time.Parse("2006-01-02", c.ExpiresAt)
	if err != nil {
		return nil, err
	}
	// Preserve the existing wire representation of configured dates.
	unix := date.Add(12 * time.Hour).Unix()
	return &unix, nil
}

func main() {
	unregister := flag.Bool("unregister", false, "unregister this agent from server and exit")
	checkConfig := flag.Bool("check-config", false, "validate configuration without collecting metrics or contacting the server")
	flag.Parse()
	if *unregister && *checkConfig {
		log.Fatal("choose either -unregister or -check-config")
	}

	cfgPath := "agent.yaml"
	if args := flag.Args(); len(args) > 0 {
		cfgPath = args[0]
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("read config %s: %v", cfgPath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("parse config: %v", err)
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 30
	}

	if cfg.Insecure != nil {
		log.Println("WARNING: config field 'insecure' is deprecated; use 'tls_skip_verify' instead.")
	}
	serverURL, err := agentpkg.ValidateServerURL(cfg.Server, cfg.AllowPlainHTTP)
	if err != nil {
		log.Fatalf("invalid server configuration: %v", err)
	}
	if *checkConfig {
		if strings.TrimSpace(cfg.Token) == "" {
			log.Fatal("Agent token is required")
		}
		if err := cfg.validateProbesAndInterval(); err != nil {
			log.Fatal(err)
		}
		if _, err := cfg.monthlyTrafficLimitBytes(); err != nil {
			log.Fatal(err)
		}
		if _, err := cfg.expiryUnix(); err != nil {
			log.Fatalf("invalid expiry date: %v", err)
		}
		fmt.Println("Configuration is valid.")
		return
	}
	if cfg.AllowPlainHTTP && strings.HasPrefix(serverURL, "http://") {
		log.Printf("SECURITY WARNING: allow_plain_http is enabled; the Agent token may be transmitted without encryption to %s", serverURL)
	}

	reporter := agentpkg.NewReporter(serverURL, cfg.Token, cfg.effectiveTLSSkipVerify())
	hostname := cfg.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}

	if *unregister {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := reporter.Unregister(ctx, hostname); err != nil {
			log.Fatalf("unregister failed: %v", err)
		}
		log.Println("unregistered successfully")
		return
	}
	if err := cfg.validateProbesAndInterval(); err != nil {
		log.Fatal(err)
	}

	log.Printf("Needle Agent - server: %s, interval: %ds", serverURL, cfg.Interval)

	expiresAtUnix, err := cfg.expiryUnix()
	if err != nil {
		log.Printf("WARNING: invalid expires_at %q (expected YYYY-MM-DD); due date will not be reported", cfg.ExpiresAt)
	}

	monthlyLimit, err := cfg.monthlyTrafficLimitBytes()
	if err != nil {
		log.Fatalf("invalid traffic configuration: %v", err)
	}
	netCollector := collector.NewNetworkCollector(cfg.Interfaces)
	collectors := reportCollectors{
		cpu: collector.CollectCPU, memory: collector.CollectMemory,
		disk: collector.CollectDisk, network: netCollector.Collect,
		load: collector.CollectLoad, uptime: collector.CollectUptime,
		tcpping: collector.TCPing,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	collector.CollectCPU()

	// protect reportFn with a mutex so we wait for an in-flight report
	var reportMu sync.Mutex

	reportFn := func() {
		reportMu.Lock()
		defer reportMu.Unlock()

		// after receiving shutdown signal, skip new reports
		if ctx.Err() != nil {
			return
		}

		data := collectReport(collectors, cfg.TCPing, log.Printf)
		data.Hostname = hostname
		data.Region = cfg.Region
		data.ExpiresAt = expiresAtUnix
		data.BillingPeriod = cfg.BillingPeriod
		data.MonthlyTrafficLimit = monthlyLimit

		if err := reporter.Send(ctx, data); err != nil {
			log.Printf("report failed: %v", err)
		}
	}

	reportFn()

	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			reportFn()
		case <-sigCh:
			log.Println("shutting down...waiting for in-flight report...")
			reportMu.Lock()
			cancel()
			reportMu.Unlock()
			return
		}
	}
}
