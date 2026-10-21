package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const ReportProtocol = "persistent-report-v1"

var ErrHistoryExpired = errors.New("historical sample exceeded retry age")

type HistorySender struct {
	reporter          *Reporter
	maxAge            time.Duration
	logf              func(string, ...any)
	now               func() time.Time
	wait              func(context.Context, time.Duration) error
	confirmed, legacy bool
	lastLog           time.Time
	authCooldown      time.Time
}

func NewHistorySender(r *Reporter, age time.Duration, logf func(string, ...any)) *HistorySender {
	return &HistorySender{reporter: r, maxAge: age, logf: logf, now: time.Now, wait: func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
}
func parseRetryAfter(value string, now time.Time) time.Duration {
	if n, err := strconv.Atoi(value); err == nil {
		return time.Duration(min(max(0, n), 30)) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		return min(max(time.Duration(0), t.Sub(now)), 30*time.Second)
	}
	return 0
}
func retryable(err error) bool {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Status {
		case 408, 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		return false
	}
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &cert) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var address net.InvalidAddrError
	if errors.As(err, &address) {
		return false
	}
	var network *url.Error
	if errors.As(err, &network) {
		var transport net.Error
		return errors.As(network.Err, &transport)
	}
	var transport net.Error
	return errors.As(err, &transport)
}
func (s *HistorySender) negotiate(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", s.reporter.serverURL+"/api/report-capabilities", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.reporter.token)
	resp, err := s.reporter.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return &HTTPStatusError{Status: resp.StatusCode}
	}
	var capability struct {
		Protocol string `json:"protocol"`
	}
	if resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&capability) == nil && capability.Protocol == ReportProtocol {
		s.confirmed = true
		return nil
	}
	if resp.StatusCode == 404 || resp.StatusCode == 405 || resp.StatusCode == 501 || resp.StatusCode == 200 {
		s.legacy = true
		s.logf("Server lacks persistent report deduplication; history retries disabled until Agent restart. Upgrade Server first.")
		return nil
	}
	return &HTTPStatusError{Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), s.now())}
}
func (s *HistorySender) sendFrozen(ctx context.Context, body []byte, id string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", s.reporter.serverURL+"/api/report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.reporter.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.reporter.client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return s.reporter.redact(readReportResponse(resp))
	}
	defer resp.Body.Close()
	var ack struct{ Protocol, ReportID string }
	// Explicit tags are required for snake_case wire names.
	var payload struct {
		Protocol string `json:"protocol"`
		ReportID string `json:"report_id"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return fmt.Errorf("invalid committed report acknowledgement: %w", err)
	}
	ack.Protocol, ack.ReportID = payload.Protocol, payload.ReportID
	if ack.Protocol != ReportProtocol || ack.ReportID != id {
		return fmt.Errorf("committed report acknowledgement mismatch; refusing unsafe retry")
	}
	return nil
}
func (s *HistorySender) Send(ctx context.Context, data *ReportData) error {
	if s.now().Before(s.authCooldown) {
		return fmt.Errorf("history authentication cooldown; sample discarded")
	}
	// Freeze once, after the asynchronous probe batch completed.
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	expires := time.Unix(data.CreatedAt, 0).Add(s.maxAge)
	retries := 0
	for {
		remaining := expires.Sub(s.now())
		if remaining <= 0 {
			s.notice("history sample expired; retries=%d", retries)
			return ErrHistoryExpired
		}
		attempt, cancel := context.WithTimeout(ctx, min(15*time.Second, remaining))
		if !s.confirmed && !s.legacy {
			err = s.negotiate(attempt)
		} else {
			err = nil
		}
		if err == nil {
			if s.legacy {
				err = s.reporter.send(attempt, "/api/report", json.RawMessage(body))
			} else {
				err = s.sendFrozen(attempt, body, data.ReportID)
			}
		}
		cancel()
		if err == nil {
			if retries > 0 {
				s.logf("history recovered: backfilled=1 retries=%d sample_age=%s", retries, s.now().Sub(time.Unix(data.CreatedAt, 0)).Round(time.Second))
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Legacy report requests are never retried, including ambiguous responses.
		if s.legacy || !retryable(err) {
			var status *HTTPStatusError
			if errors.As(err, &status) && (status.Status == 401 || status.Status == 403) {
				s.authCooldown = s.now().Add(time.Minute)
			}
			s.notice("history permanent error; sample discarded: %v", err)
			return err
		}
		// Retrying read-only capability negotiation is safe before confirmation;
		// no report is sent until persistent deduplication is explicitly confirmed.
		retries++
		delay := min(time.Second*time.Duration(1<<min(retries-1, 5)), 30*time.Second)
		delay = time.Duration(float64(delay) * (.75 + rand.Float64()*.5))
		var status *HTTPStatusError
		if errors.As(err, &status) {
			delay = max(delay, status.RetryAfter)
		}
		delay = min(delay, 30*time.Second, expires.Sub(s.now()))
		s.notice("history retry pending: retries=%d oldest_age=%s next=%s", retries, s.now().Sub(time.Unix(data.CreatedAt, 0)).Round(time.Second), delay.Round(time.Millisecond))
		if err = s.wait(ctx, max(time.Duration(0), delay)); err != nil {
			return err
		}
	}
}
func (s *HistorySender) notice(format string, args ...any) {
	if s.now().Sub(s.lastLog) >= time.Minute {
		s.logf(format, args...)
		s.lastLog = s.now()
	}
}
