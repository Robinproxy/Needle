package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"needle/internal/agent/collector"
	"needle/internal/billing"
)

type ReportData struct {
	CPUModel string `json:"cpu_model,omitempty"`
	OSName   string `json:"os_name,omitempty"`
	Arch     string `json:"arch,omitempty"`

	BootID string `json:"boot_id,omitempty"`

	ReportID string `json:"report_id,omitempty"`

	TCPingCreatedAt     int64                    `json:"tcpping_created_at,omitempty"`
	Interval            int                      `json:"interval,omitempty"`
	RealtimeInterval    int                      `json:"realtime_interval,omitempty"`
	BillingCost         *billing.Cost            `json:"billing_cost,omitempty"`
	CreatedAt           int64                    `json:"created_at,omitempty"`
	MonthlyTrafficLimit int64                    `json:"monthly_traffic_limit"`
	Hostname            string                   `json:"hostname"`
	Region              string                   `json:"region"`
	ExpiresAt           *int64                   `json:"expires_at,omitempty"`
	BillingPeriod       string                   `json:"billing_period,omitempty"`
	CPU                 *collector.CPUStats      `json:"cpu"`
	Memory              *collector.MemoryStats   `json:"memory"`
	Disk                *collector.DiskStats     `json:"disk"`
	Network             *collector.NetworkStats  `json:"network"`
	Load                *collector.LoadStats     `json:"load"`
	Uptime              uint64                   `json:"uptime"`
	TCPing              []collector.TCPingResult `json:"tcpping,omitempty"`
}

// RealtimeData deliberately cannot carry probes, billing or traffic policy.
type RealtimeData struct {
	Hostname  string                  `json:"hostname"`
	CreatedAt int64                   `json:"created_at"`
	CPU       *collector.CPUStats     `json:"cpu"`
	Memory    *collector.MemoryStats  `json:"memory"`
	Disk      *collector.DiskStats    `json:"disk"`
	Network   *collector.NetworkStats `json:"network"`
	Load      *collector.LoadStats    `json:"load"`
	Uptime    uint64                  `json:"uptime"`
}

func RealtimeFromReport(r *ReportData) *RealtimeData {
	return &RealtimeData{Hostname: r.Hostname, CreatedAt: r.CreatedAt, CPU: r.CPU, Memory: r.Memory, Disk: r.Disk, Network: r.Network, Load: r.Load, Uptime: r.Uptime}
}

type HTTPStatusError struct {
	Status     int
	RetryAfter time.Duration
}

// Upstream bodies can echo credentials, configuration, or proxy HTML. Keep
// them out of runtime errors entirely; status and operation identify failures.
func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("server %d: %s", e.Status, http.StatusText(e.Status))
}

type Reporter struct {
	serverURL string
	token     string
	client    *http.Client
}

const maxResponseBytes = 4096

// Keep a failed/misconfigured upstream from filling router memory or logs.
// Draining small success bodies also permits HTTP connection reuse.
func readReportResponse(resp *http.Response) error {
	defer resp.Body.Close()
	_, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusOK {
		return &HTTPStatusError{Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	if err != nil {
		return fmt.Errorf("read server response: %w", err)
	}
	return nil
}

func NewReporter(serverURL, token string, tlsSkipVerify bool) *Reporter {
	// Clone keeps the default proxy support and connection-pool settings.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: tlsSkipVerify}
	return &Reporter{
		serverURL: serverURL,
		token:     token,
		client:    &http.Client{Transport: tr, Timeout: 15 * time.Second},
	}
}

func (r *Reporter) SendRealtime(ctx context.Context, data *RealtimeData) error {
	return r.send(ctx, "/api/realtime", data)
}

func (r *Reporter) send(ctx context.Context, path string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.serverURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.token)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return readReportResponse(resp)
}

func (r *Reporter) Unregister(ctx context.Context, hostname string) error {
	// An older Server's unregister endpoint deleted monitoring history. Require
	// explicit safe semantics before a new Agent calls it; old Agents remain
	// compatible with the new Server endpoint.
	reqCaps, err := http.NewRequestWithContext(ctx, http.MethodGet, r.serverURL+"/api/report-capabilities", nil)
	if err != nil {
		return err
	}
	reqCaps.Header.Set("Authorization", "Bearer "+r.token)
	capsResp, err := r.client.Do(reqCaps)
	if err != nil {
		return err
	}
	var capability struct {
		RetainsHistory bool `json:"unregister_retains_history"`
	}
	decoded := json.NewDecoder(io.LimitReader(capsResp.Body, maxResponseBytes)).Decode(&capability)
	capsResp.Body.Close()
	if capsResp.StatusCode != http.StatusOK || decoded != nil || !capability.RetainsHistory {
		return fmt.Errorf("Server has not confirmed history-preserving unregister; upgrade Server first, or uninstall locally without unregister")
	}
	payload := struct {
		Hostname string `json:"hostname"`
	}{Hostname: hostname}
	return r.send(ctx, "/api/unregister", payload)
}
