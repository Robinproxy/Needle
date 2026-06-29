package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
	agentpkg "needle/internal/agent"
	"needle/internal/agent/collector"
)

type Config struct {
	Hostname      string                    `yaml:"hostname"`
	Server        string                    `yaml:"server"`
	Token         string                    `yaml:"token"`
	Region        string                    `yaml:"region"`
	ExpiresAt     string                    `yaml:"expires_at"`
	BillingPeriod string                    `yaml:"billing_period"`
	Interval      int                       `yaml:"interval"`
	Insecure      bool                      `yaml:"insecure"`
	TCPing        []collector.TCPingTarget  `yaml:"tcpping"`
}

func main() {
	unregister := flag.Bool("unregister", false, "unregister this agent from server and exit")
	flag.Parse()

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

	reporter := agentpkg.NewReporter(cfg.Server, cfg.Token, cfg.Insecure)
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

	log.Printf("Needle Agent - server: %s, interval: %ds", cfg.Server, cfg.Interval)

	netCollector := collector.NewNetworkCollector()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	collector.CollectCPU()

	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer ticker.Stop()

	report := func() {
		cpu, err := collector.CollectCPU()
		if err != nil {
			log.Printf("collect cpu: %v", err)
			return
		}
		mem, err := collector.CollectMemory()
		if err != nil {
			log.Printf("collect memory: %v", err)
			return
		}
		diskStat, err := collector.CollectDisk()
		if err != nil {
			log.Printf("collect disk: %v", err)
			return
		}
		netStat, err := netCollector.Collect()
		if err != nil {
			log.Printf("collect network: %v", err)
			return
		}
		load, err := collector.CollectLoad()
		if err != nil {
			log.Printf("collect load: %v", err)
			return
		}
		uptime, err := collector.CollectUptime()
		if err != nil {
			log.Printf("collect uptime: %v", err)
			return
		}
		tcpping := collector.TCPing(cfg.TCPing)

		var expiresAtUnix *int64
		if cfg.ExpiresAt != "" {
			t, err := time.Parse("2006-01-02", cfg.ExpiresAt)
			if err == nil {
				unix := t.Unix()
				expiresAtUnix = &unix
			}
		}

		data := &agentpkg.ReportData{
			Hostname:      hostname,
			Region:        cfg.Region,
			ExpiresAt:     expiresAtUnix,
			BillingPeriod: cfg.BillingPeriod,
			CPU:      cpu,
			Memory:   mem,
			Disk:     diskStat,
			Network:  netStat,
			Load:     load,
			Uptime:   uptime,
			TCPing:   tcpping,
		}

		if err := reporter.Send(ctx, data); err != nil {
			log.Printf("report failed: %v", err)
		}
	}

	report()

	for {
		select {
		case <-ticker.C:
			report()
		case <-sigCh:
			log.Println("shutting down...")
			cancel()
			return
		}
	}
}
