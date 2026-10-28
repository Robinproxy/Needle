package server

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"needle/internal/billing"
)

type reportRequest struct {
	CPUModel string `json:"cpu_model,omitempty"`
	OSName   string `json:"os_name,omitempty"`
	Arch     string `json:"arch,omitempty"`

	BootID string `json:"boot_id,omitempty"`

	ReportID string `json:"report_id,omitempty"`

	TCPingCreatedAt     *int64        `json:"tcpping_created_at"`
	Interval            int           `json:"interval"`
	RealtimeInterval    int           `json:"realtime_interval"`
	BillingCost         *billing.Cost `json:"billing_cost"`
	MonthlyTrafficLimit int64         `json:"monthly_traffic_limit"`
	Hostname            string        `json:"hostname"`
	Region              string        `json:"region"`
	ExpiresAt           *int64        `json:"expires_at"`
	BillingPeriod       string        `json:"billing_period"`
	CPU                 *struct {
		Percent float64 `json:"percent"`
		Cores   int     `json:"cores"`
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
		ScopeID   string `json:"network_scope_id,omitempty"`
		Up        int64  `json:"up"`
		Down      int64  `json:"down"`
		TotalSent int64  `json:"total_sent"`
		TotalRecv int64  `json:"total_recv"`
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
	for _, field := range []struct {
		value *string
		limit int
	}{{&req.CPUModel, 256}, {&req.OSName, 256}, {&req.Arch, 32}} {
		if !utf8.ValidString(*field.value) || hasControl(*field.value) || utf8.RuneCountInString(*field.value) > field.limit {
			return fmt.Errorf("invalid system information")
		}
		*field.value = strings.TrimSpace(*field.value)
	}
	if req.ReportID != "" && (!reportIDPattern.MatchString(req.ReportID) || req.CreatedAt == nil || *req.CreatedAt <= 0) {
		return fmt.Errorf("invalid report identity/sample time")
	}
	if req.TCPingCreatedAt != nil && (*req.TCPingCreatedAt < now.Add(-30*24*time.Hour).Unix() || *req.TCPingCreatedAt > now.Add(5*time.Minute).Unix()) {
		return fmt.Errorf("invalid TCP Ping sample time")
	}
	if req.BootID != "" && (len(req.BootID) != 36 || !bootIDPattern.MatchString(req.BootID)) {
		return fmt.Errorf("invalid boot identity")
	}
	if req.Network != nil && req.Network.ScopeID != "" && !scopeIDPattern.MatchString(req.Network.ScopeID) {
		return fmt.Errorf("invalid network scope identity")
	}
	interval := req.Interval
	if interval == 0 {
		interval = 30
	}
	if interval < 1 || interval > 86400 || req.RealtimeInterval < 0 || req.RealtimeInterval > interval {
		return fmt.Errorf("invalid report/realtime interval")
	}
	req.Hostname = strings.TrimSpace(req.Hostname)
	if req.Hostname == "" {
		return fmt.Errorf("hostname is required")
	}
	if utf8.RuneCountInString(req.Hostname) > 253 || hasControl(req.Hostname) {
		return fmt.Errorf("invalid hostname")
	}

	if req.MonthlyTrafficLimit < 0 || req.MonthlyTrafficLimit > 9_000_000_000_000_000 {
		return fmt.Errorf("invalid monthly traffic limit")
	}
	switch req.BillingPeriod {
	case "", "1m", "3m", "6m", "12m", "24m", "36m", "forever":
	default:
		return fmt.Errorf("invalid billing period")
	}
	if req.BillingCost != nil {
		cost, err := req.BillingCost.Normalize()
		if err != nil {
			return err
		}
		if cost.Amount != "" && req.BillingPeriod == "" {
			return fmt.Errorf("billing period is required when billing amount is configured")
		}
		req.BillingCost = &cost
	}
	if req.BillingPeriod == "forever" {
		req.ExpiresAt = nil // Permanent nodes have no renewal date.
	}
	if req.ExpiresAt != nil {
		minTime := now.AddDate(-20, 0, 0).Unix()
		maxTime := now.AddDate(20, 0, 0).Unix()
		if *req.ExpiresAt < minTime || *req.ExpiresAt > maxTime {
			return fmt.Errorf("invalid expiry time")
		}
	}

	if req.CPU != nil && (req.CPU.Cores < 0 || req.CPU.Cores > 65536) {
		return fmt.Errorf("invalid CPU core count")
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
	names := make(map[string]bool, len(req.TCPing))
	for _, result := range req.TCPing {
		if sanitizeTCPingName(result.Name) == "" || sanitizeTCPingTarget(result.Target) == "" {
			return fmt.Errorf("invalid TCPing name or target")
		}
		name := sanitizeTCPingName(result.Name)
		if names[name] {
			return fmt.Errorf("duplicate TCPing name")
		}
		names[name] = true
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
