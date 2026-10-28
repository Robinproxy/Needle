package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	agentpkg "needle/internal/agent"
	"needle/internal/agent/collector"
	"needle/internal/logging"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ServerManaged             bool `yaml:"server_managed"`
	HistoryQueueCapacity      int  `yaml:"history_queue_capacity"`
	HistoryRetryMaxAgeSeconds int  `yaml:"history_retry_max_age_seconds"`

	Hostname         string                   `yaml:"hostname"`
	Server           string                   `yaml:"server"`
	Token            string                   `yaml:"token"`
	Interval         int                      `yaml:"interval"`
	RealtimeInterval int                      `yaml:"realtime_interval"`
	TLSSkipVerify    bool                     `yaml:"tls_skip_verify"`
	AllowPlainHTTP   bool                     `yaml:"allow_plain_http"`
	Interfaces       []string                 `yaml:"interfaces"`
	TCPing           []collector.TCPingTarget `yaml:"tcpping"`
}

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	// Decode the effective mapping first so aliases and YAML merge keys cannot
	// silently retain settings that now belong to the Server. yaml.v3 bounds
	// alias expansion and rejects self-referential aliases.
	var fields map[string]any
	if err := node.Decode(&fields); err != nil {
		return err
	}
	for _, name := range []string{"region", "billing_currency", "billing_amount", "billing_period", "expires_at", "monthly_traffic_limit_gb"} {
		if _, exists := fields[name]; exists {
			return fmt.Errorf("%s is no longer an Agent setting; configure static node settings in Server /admin and remove this key from agent.yaml", name)
		}
	}
	if _, exists := fields["insecure"]; exists {
		return fmt.Errorf("insecure was removed; replace it with tls_skip_verify in agent.yaml")
	}
	if value, exists := fields["server_managed"]; exists {
		managed, ok := value.(bool)
		if !ok {
			return fmt.Errorf("server_managed must be a boolean")
		}
		if !managed {
			return fmt.Errorf("server_managed: false is no longer supported; configure static node settings in Server /admin and remove this key or set it to true")
		}
	}
	for _, name := range []string{"realtime_interval", "history_queue_capacity", "history_retry_max_age_seconds"} {
		if value, exists := fields[name]; exists {
			if _, ok := value.(int); !ok {
				return fmt.Errorf("%s must be an integer", name)
			}
		}
	}
	type plain Config
	decoded := plain{ServerManaged: true}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*c = Config(decoded)
	return nil
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
		fmt.Println("needle-install-v2 persistent-report-v1 realtime-v1 server-profiles-v1")
		return
	}
	if *unregister && *checkConfig {
		logging.Fatal("agent", "arguments_invalid", fmt.Errorf("choose either -unregister or -check-config"))
	}

	cfgPath := "agent.yaml"
	if args := flag.Args(); len(args) > 0 {
		cfgPath = args[0]
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		logging.Fatal("agent", "config_read_failed", fmt.Errorf("read config %s: %w", cfgPath, err))
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		logging.Fatal("agent", "config_parse_failed", err)
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 30
	}

	if err := cfg.validateToken(); err != nil {
		logging.Fatal("agent", "config_invalid", err)
	}
	serverURL, err := agentpkg.ValidateServerURL(cfg.Server, cfg.AllowPlainHTTP)
	if err != nil {
		logging.Fatal("agent", "server_url_invalid", err)
	}
	if *checkConfig {
		if err := cfg.validateProbesAndInterval(); err != nil {
			logging.Fatal("agent", "config_invalid", err)
		}
		fmt.Println("Configuration is valid.")
		return
	}
	if cfg.AllowPlainHTTP && strings.HasPrefix(serverURL, "http://") {
		logging.Print("WARN", "agent", "plaintext_http", "action", "Token is transmitted without encryption; use HTTPS")
	}

	reporter := agentpkg.NewReporter(serverURL, cfg.Token, cfg.TLSSkipVerify)
	hostname := cfg.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}

	if *unregister {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := reporter.Unregister(ctx, hostname); err != nil {
			logging.Fatal("agent", "unregister_failed", err)
		}
		logging.Print("INFO", "agent", "unregistered")
		return
	}
	if err := cfg.validateProbesAndInterval(); err != nil {
		logging.Fatal("agent", "config_invalid", err)
	}

	logging.Print("INFO", "agent", "started", "version", Version, "commit", BuildCommit,
		"hostname", hostname, "history_interval_seconds", cfg.Interval, "realtime_interval_seconds", cfg.RealtimeInterval)

	systemInfo, systemErr := collector.CollectSystemInfo()
	if systemErr != nil {
		logging.Print("WARN", "agent", "system_info_incomplete", "error", systemErr)
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
		logging.Print("WARN", "traffic", "boot_identity_unavailable", "error", bootErr)
	}
	decorate := func(data *agentpkg.ReportData) {
		data.ServerManaged = true
		data.CPUModel, data.OSName, data.Arch = systemInfo.CPUModel, systemInfo.OSName, systemInfo.Arch
		data.BootID = bootID
		data.Hostname = hostname
		data.Interval, data.RealtimeInterval = cfg.Interval, cfg.RealtimeInterval
	}
	sample := func(channel string) func() *agentpkg.ReportData {
		c := newWindowCollectors(cfg.Interfaces)
		logs := logging.NewTracker("collect."+channel, log.Printf)
		return func() *agentpkg.ReportData {
			data := collectReport(c, logs)
			decorate(data)
			return data
		}
	}
	historySender := agentpkg.NewHistorySender(reporter, time.Duration(cfg.retryMaxAge())*time.Second, log.Printf)
	runSamplingWithPolicy(historyPolicy{Capacity: cfg.queueCapacity()}, ctx, historyTicker.C, realtimeTicks, sample("history"), sample("realtime"),
		func(ctx context.Context, data *agentpkg.ReportData) error {
			return historySender.Send(ctx, data)
		}, func(ctx context.Context, data *agentpkg.ReportData) error {
			return reporter.SendRealtime(ctx, agentpkg.RealtimeFromReport(data))
		}, log.Printf, newProbeSampler(cfg.TCPing))
}
