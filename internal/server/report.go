package server

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

type reportRequest struct {
	Hostname      string `json:"hostname"`
	Region        string `json:"region"`
	ExpiresAt     *int64 `json:"expires_at"`
	BillingPeriod string `json:"billing_period"`
	CPU           *struct {
		Percent float64 `json:"percent"`
	} `json:"cpu"`
	Memory *struct {
		Total uint64 `json:"total"`
		Used  uint64 `json:"used"`
	} `json:"memory"`
	Disk *struct {
		Total uint64 `json:"total"`
		Used  uint64 `json:"used"`
	} `json:"disk"`
	Network *struct {
		Up        int64 `json:"up"`
		Down      int64 `json:"down"`
		TotalSent int64 `json:"total_sent"`
		TotalRecv int64 `json:"total_recv"`
	} `json:"network"`
	Load *struct {
		Load1  float64 `json:"load1"`
		Load5  float64 `json:"load5"`
		Load15 float64 `json:"load15"`
	} `json:"load"`
	Uptime    uint64 `json:"uptime"`
	CreatedAt *int64 `json:"created_at"`
	TCPing    []struct {
		Name      string  `json:"name"`
		Target    string  `json:"target"`
		LatencyMs float64 `json:"latency_ms"`
		Success   bool    `json:"success"`
	} `json:"tcpping"`
}

func validateReport(req *reportRequest, now time.Time) error {
	req.Hostname = strings.TrimSpace(req.Hostname)
	if req.Hostname == "" {
		return fmt.Errorf("hostname is required")
	}
	if utf8.RuneCountInString(req.Hostname) > 253 || hasControl(req.Hostname) {
		return fmt.Errorf("invalid hostname")
	}

	switch req.BillingPeriod {
	case "", "1m", "3m", "6m", "12m":
	default:
		return fmt.Errorf("invalid billing period")
	}
	if req.ExpiresAt != nil {
		minTime := now.AddDate(-20, 0, 0).Unix()
		maxTime := now.AddDate(20, 0, 0).Unix()
		if *req.ExpiresAt < minTime || *req.ExpiresAt > maxTime {
			return fmt.Errorf("invalid expiry time")
		}
	}

	if req.CPU != nil && (!finite(req.CPU.Percent) || req.CPU.Percent < 0 || req.CPU.Percent > 100) {
		return fmt.Errorf("invalid CPU percent")
	}
	if req.Memory != nil && (req.Memory.Total > math.MaxInt64 || req.Memory.Used > math.MaxInt64 || req.Memory.Used > req.Memory.Total) {
		return fmt.Errorf("invalid memory values")
	}
	if req.Disk != nil && (req.Disk.Total > math.MaxInt64 || req.Disk.Used > math.MaxInt64 || req.Disk.Used > req.Disk.Total) {
		return fmt.Errorf("invalid disk values")
	}
	if req.Network != nil {
		if req.Network.Up < 0 || req.Network.Down < 0 || req.Network.Up > 1_000_000_000_000 || req.Network.Down > 1_000_000_000_000 || req.Network.TotalSent < 0 || req.Network.TotalRecv < 0 {
			return fmt.Errorf("invalid network values")
		}
	}
	if req.Load != nil {
		for _, value := range []float64{req.Load.Load1, req.Load.Load5, req.Load.Load15} {
			if !finite(value) || value < 0 || value > 1_000_000 {
				return fmt.Errorf("invalid load values")
			}
		}
	}
	if req.Uptime > math.MaxInt64 {
		return fmt.Errorf("invalid uptime")
	}
	if len(req.TCPing) > maxTCPingPerReport {
		return fmt.Errorf("too many TCPing results")
	}
	for _, result := range req.TCPing {
		if sanitizeTCPingName(result.Name) == "" || sanitizeTCPingTarget(result.Target) == "" {
			return fmt.Errorf("invalid TCPing name or target")
		}
		if !finite(result.LatencyMs) || result.LatencyMs < 0 || result.LatencyMs > 60_000 {
			return fmt.Errorf("invalid TCPing latency")
		}
	}
	return nil
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}
