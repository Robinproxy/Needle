package agent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func TestHistoryRetryFrozenPayloadAndLostAck(t *testing.T) {
	now := time.Unix(10000, 0)
	calls := 0
	var bodies []string
	r := NewReporter("http://localhost", "test", false)
	r.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/report-capabilities" {
			return response(200, `{"protocol":"persistent-report-v1"}`), nil
		}
		b, _ := io.ReadAll(req.Body)
		bodies = append(bodies, string(b))
		calls++
		if calls == 1 {
			return nil, io.ErrUnexpectedEOF
		} // committed but ACK lost
		var report ReportData
		json.Unmarshal(b, &report)
		return response(200, `{"protocol":"persistent-report-v1","report_id":"`+report.ReportID+`"}`), nil
	})
	sender := NewHistorySender(r, 30*time.Minute, func(string, ...any) {})
	sender.now = func() time.Time { return now }
	sender.wait = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	data := &ReportData{ReportID: "0123456789abcdef0123456789abcdef:1", CreatedAt: now.Unix(), Hostname: "test"}
	if err := sender.Send(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || bodies[0] != bodies[1] {
		t.Fatal("retry changed frozen report", calls)
	}
}
func TestHistoryRetryCompatibilityAndPermanentErrors(t *testing.T) {
	for _, code := range []int{400, 401, 403, 409, 422, 404} {
		calls := 0
		r := NewReporter("http://localhost", "test", false)
		r.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/api/report-capabilities" {
				if code == 404 {
					return response(404, ""), nil
				}
				return response(200, `{"protocol":"persistent-report-v1"}`), nil
			}
			calls++
			return response(code, "test error"), nil
		})
		sender := NewHistorySender(r, time.Minute, func(string, ...any) {})
		if err := sender.Send(context.Background(), &ReportData{CreatedAt: time.Now().Unix(), ReportID: "id"}); err == nil {
			t.Fatal(code)
		}
		if calls != 1 {
			t.Fatal("permanent error retried", code, calls)
		}
	}
}
func TestHistoryAgeMeasuredFromSampleAndRetryAfter(t *testing.T) {
	now := time.Unix(10000, 0)
	calls := 0
	r := NewReporter("http://localhost", "test", false)
	r.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/report-capabilities" {
			return response(200, `{"protocol":"persistent-report-v1"}`), nil
		}
		calls++
		resp := response(429, "")
		resp.Header.Set("Retry-After", "30")
		return resp, nil
	})
	sender := NewHistorySender(r, 60*time.Second, func(string, ...any) {})
	sender.now = func() time.Time { return now }
	sender.wait = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	if err := sender.Send(context.Background(), &ReportData{CreatedAt: now.Add(-50 * time.Second).Unix(), ReportID: "test"}); err != ErrHistoryExpired {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestCapabilityNegotiationCanRecoverBeforeAnyReportIsSent(t *testing.T) {
	now := time.Unix(10000, 0)
	caps, reports := 0, 0
	r := NewReporter("http://localhost", "test", false)
	r.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/report-capabilities" {
			caps++
			if caps == 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return response(200, `{"protocol":"persistent-report-v1"}`), nil
		}
		reports++
		return response(200, `{"protocol":"persistent-report-v1","report_id":"stable"}`), nil
	})
	sender := NewHistorySender(r, time.Minute, func(string, ...any) {})
	sender.now = func() time.Time { return now }
	sender.wait = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	if err := sender.Send(context.Background(), &ReportData{CreatedAt: now.Unix(), ReportID: "stable"}); err != nil {
		t.Fatal(err)
	}
	if caps != 2 || reports != 1 {
		t.Fatal(caps, reports)
	}
}
func TestTLSConfigurationErrorsDoNotRetry(t *testing.T) {
	if retryable(&url.Error{Op: "Post", URL: "https://localhost", Err: x509.UnknownAuthorityError{}}) {
		t.Fatal("TLS trust configuration treated as transient")
	}
	if parseRetryAfter("9223372036854775807", time.Now()) != 30*time.Second {
		t.Fatal("Retry-After overflow")
	}
}
