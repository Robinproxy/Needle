package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
)

func (c Config) validateProbesAndInterval() error {
	if c.HistoryQueueCapacity < 0 || c.HistoryQueueCapacity > 1024 {
		return fmt.Errorf("history_queue_capacity must be 1–1024 (omitted/0 = 64)")
	}
	if c.HistoryRetryMaxAgeSeconds < 0 || c.HistoryRetryMaxAgeSeconds > 86400 {
		return fmt.Errorf("history_retry_max_age_seconds must be 1–86400 (omitted/0 = 1800)")
	}
	switch c.BillingPeriod {
	case "", "1m", "3m", "6m", "12m", "24m", "36m", "forever":
	default:
		return fmt.Errorf("unsupported billing_period")
	}
	if c.Interval > 86400 {
		return fmt.Errorf("interval must not exceed 86400 seconds")
	}
	interval := c.Interval
	if interval <= 0 {
		interval = 30
	}
	if c.RealtimeInterval < 0 || c.RealtimeInterval > interval {
		return fmt.Errorf("realtime_interval must be 0 (disabled) or 1–interval seconds")
	}
	if len(c.TCPing) > 32 {
		return fmt.Errorf("at most 32 TCP Ping targets are supported")
	}
	names := make(map[string]bool, len(c.TCPing))
	for _, p := range c.TCPing {
		if strings.TrimSpace(p.Name) == "" || len(p.Name) > 64 || strings.ContainsAny(p.Name, "\r\n\x00") || names[p.Name] {
			return fmt.Errorf("TCP Ping names must be unique and contain 1–64 characters")
		}
		names[p.Name] = true
		host, port, err := net.SplitHostPort(p.Target)
		if err != nil || host == "" || len(p.Target) > 256 {
			return fmt.Errorf("invalid TCP Ping target %q", p.Target)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("TCP Ping port must be 1–65535")
		}
	}
	return nil
}

func (c Config) queueCapacity() int {
	if c.HistoryQueueCapacity == 0 {
		return 64
	}
	return c.HistoryQueueCapacity
}
func (c Config) retryMaxAge() int {
	if c.HistoryRetryMaxAgeSeconds == 0 {
		return 1800
	}
	return c.HistoryRetryMaxAgeSeconds
}

func (c Config) validateToken() error {
	if len(c.Token) < 1 || len(c.Token) > 256 {
		return fmt.Errorf("token must contain 1–256 non-whitespace characters")
	}
	for _, ch := range c.Token {
		if unicode.IsSpace(ch) || unicode.IsControl(ch) {
			return fmt.Errorf("token cannot contain whitespace/control characters")
		}
	}
	return nil
}
