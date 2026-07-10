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
)

type ReportData struct {
	Hostname      string                   `json:"hostname"`
	Region        string                   `json:"region"`
	ExpiresAt     *int64                   `json:"expires_at,omitempty"`
	BillingPeriod string                   `json:"billing_period,omitempty"`
	CPU           *collector.CPUStats      `json:"cpu"`
	Memory        *collector.MemoryStats   `json:"memory"`
	Disk          *collector.DiskStats     `json:"disk"`
	Network       *collector.NetworkStats  `json:"network"`
	Load          *collector.LoadStats     `json:"load"`
	Uptime        uint64                   `json:"uptime"`
	TCPing        []collector.TCPingResult `json:"tcpping,omitempty"`
}

type Reporter struct {
	serverURL string
	token     string
	client    *http.Client
}

func NewReporter(serverURL, token string, insecure bool) *Reporter {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
	}
	return &Reporter{
		serverURL: serverURL,
		token:     token,
		client:    &http.Client{Transport: tr, Timeout: 15 * time.Second},
	}
}

func (r *Reporter) Send(ctx context.Context, data *ReportData) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.serverURL+"/api/report", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.token)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (r *Reporter) Unregister(ctx context.Context, hostname string) error {
	payload := struct {
		Token    string `json:"token"`
		Hostname string `json:"hostname"`
	}{
		Token:    r.token,
		Hostname: hostname,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.serverURL+"/api/unregister", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.token)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("unregister: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
