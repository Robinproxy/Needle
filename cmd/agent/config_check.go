package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

func (c Config) validateProbesAndInterval() error {
	switch c.BillingPeriod {
	case "", "1m", "3m", "6m", "12m", "forever":
	default:
		return fmt.Errorf("unsupported billing_period")
	}
	if c.Interval > 86400 {
		return fmt.Errorf("interval must not exceed 86400 seconds")
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
