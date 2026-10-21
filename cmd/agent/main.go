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
	"syscall"
	"time"

	agentpkg "needle/internal/agent"
	"needle/internal/agent/collector"
	"needle/internal/billing"

	"gopkg.in/yaml.v3"
)

type Config struct {
	HistoryQueueCapacity      int `yaml:"history_queue_capacity"`
	HistoryRetryMaxAgeSeconds int `yaml:"history_retry_max_age_seconds"`

	BillingCurrency       string                   `yaml:"billing_currency"`
	BillingAmount         *string                  `yaml:"billing_amount"`
	MonthlyTrafficLimitGB float64                  `yaml:"monthly_traffic_limit_gb"`
	Hostname              string                   `yaml:"hostname"`
	Server                string                   `yaml:"server"`
	Token                 string                   `yaml:"token"`
	Region                string                   `yaml:"region"`
	ExpiresAt             string                   `yaml:"expires_at"`
	BillingPeriod         string                   `yaml:"billing_period"`
	Interval              int                      `yaml:"interval"`
	RealtimeInterval      int                      `yaml:"realtime_interval"`
	TLSSkipVerify         bool                     `yaml:"tls_skip_verify"`
	AllowPlainHTTP        bool                     `yaml:"allow_plain_http"`
	Insecure              *bool                    `yaml:"insecure"` // Deprecated: use tls_skip_verify.
	Interfaces            []string                 `yaml:"interfaces"`
	TCPing                []collector.TCPingTarget `yaml:"tcpping"`
}

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	type plain Config
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if (node.Content[i].Value == "realtime_interval" || node.Content[i].Value == "history_queue_capacity" || node.Content[i].Value == "history_retry_max_age_seconds") && node.Content[i+1].Tag != "!!int" {
			return fmt.Errorf("%s must be an integer", node.Content[i].Value)
		}
	}
	*c = Config(decoded)
	return nil
}

func (c Config) billingCost() (*billing.Cost, error) {
	if c.BillingAmount == nil && c.BillingCurrency == "" {
		return nil, nil
	}
	cost := billing.Cost{Currency: c.BillingCurrency}
	if cost.Currency == "" {
		cost.Currency = "USD"
	}
	if c.BillingAmount != nil {
		cost.Amount = *c.BillingAmount
	}
	cost, err := cost.Normalize()
	if err != nil {
		return nil, err
	}
	if cost.Amount != "" && c.BillingPeriod == "" {
		return nil, fmt.Errorf("billing_period is required when billing_amount is configured")
	}
	return &cost, nil
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

var Version = "dev"
var BuildCommit = "unknown"

func main() {
	unregister := flag.Bool("unregister", false, "unregister this agent from server and exit")
	checkConfig := flag.Bool("check-config", false, "validate configuration without collecting metrics or contacting the server")
	showVersion := flag.Bool("version", false, "show binary version and source commit")
	installerProtocol := flag.Bool("installer-protocol", false, "show supported installer protocol")
	flag.Parse()
	if *showVersion {
		fmt.Printf("needle-agent %s commit=%s\n", Version, BuildCommit)
		return
	}
	if *installerProtocol {
		fmt.Println("needle-install-v2 persistent-report-v1 realtime-v1")
		return
	}
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

	if err := cfg.validateToken(); err != nil {
		log.Fatal(err)
	}
	if cfg.Insecure != nil {
		log.Println("WARNING: config field 'insecure' is deprecated; use 'tls_skip_verify' instead.")
	}
	serverURL, err := agentpkg.ValidateServerURL(cfg.Server, cfg.AllowPlainHTTP)
	if err != nil {
		log.Fatalf("invalid server configuration: %v", err)
	}
	if *checkConfig {
		if err := cfg.validateProbesAndInterval(); err != nil {
			log.Fatal(err)
		}
		if _, err := cfg.monthlyTrafficLimitBytes(); err != nil {
			log.Fatal(err)
		}
		if _, err := cfg.expiryUnix(); err != nil {
			log.Fatalf("invalid expiry date: %v", err)
		}
		if _, err := cfg.billingCost(); err != nil {
			log.Fatal(err)
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

	log.Printf("Needle Agent - server: %s, history interval: %ds, realtime interval: %ds (0=disabled)", serverURL, cfg.Interval, cfg.RealtimeInterval)

	expiresAtUnix, err := cfg.expiryUnix()
	if err != nil {
		log.Fatalf("invalid expires_at %q (expected YYYY-MM-DD): %v", cfg.ExpiresAt, err)
	}

	monthlyLimit, err := cfg.monthlyTrafficLimitBytes()
	if err != nil {
		log.Fatalf("invalid traffic configuration: %v", err)
	}
	billingCost, err := cfg.billingCost()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	historyTicker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer historyTicker.Stop()
	var realtimeTicks <-chan time.Time
	if cfg.RealtimeInterval > 0 {
		ticker := time.NewTicker(time.Duration(cfg.RealtimeInterval) * time.Second)
		defer ticker.Stop()
		realtimeTicks = ticker.C
	}
	bootID, bootErr := collector.BootID()
	if bootErr != nil {
		log.Printf("system boot identity unavailable; traffic continuity uses conservative compatibility: %v", bootErr)
	}
	decorate := func(data *agentpkg.ReportData) {
		data.BootID = bootID
		data.Hostname, data.Region = hostname, cfg.Region
		data.ExpiresAt, data.BillingPeriod = expiresAtUnix, cfg.BillingPeriod
		data.MonthlyTrafficLimit, data.BillingCost = monthlyLimit, billingCost
		data.Interval, data.RealtimeInterval = cfg.Interval, cfg.RealtimeInterval
	}
	sample := func() func() *agentpkg.ReportData {
		c := newWindowCollectors(cfg.Interfaces)
		return func() *agentpkg.ReportData {
			data := collectReport(c, nil, log.Printf)
			decorate(data)
			return data
		}
	}
	historySender := agentpkg.NewHistorySender(reporter, time.Duration(cfg.retryMaxAge())*time.Second, log.Printf)
	runSamplingWithPolicy(historyPolicy{Capacity: cfg.queueCapacity()}, ctx, historyTicker.C, realtimeTicks, sample(), sample(),
		func(ctx context.Context, data *agentpkg.ReportData) error {
			return historySender.Send(ctx, data)
		}, func(ctx context.Context, data *agentpkg.ReportData) error {
			return reporter.SendRealtime(ctx, agentpkg.RealtimeFromReport(data))
		}, log.Printf, newProbeSampler(cfg.TCPing))
}
