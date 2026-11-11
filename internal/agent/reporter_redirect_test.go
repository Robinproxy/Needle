package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const redirectTestToken = "redirect-test-token"
const redirectTestReply = `{"protocol":"persistent-report-v1","report_id":"redirect-report"}`

type redirectOperation struct {
	name, path string
	send       func(*Reporter) error
}

func redirectOperations(t *testing.T) []redirectOperation {
	t.Helper()
	history := func(r *Reporter) error {
		s := NewHistorySender(r, time.Minute, func(string, ...any) {})
		s.wait = func(context.Context, time.Duration) error {
			t.Error("rejected redirect must not be retried")
			return errors.New("unexpected retry")
		}
		return s.Send(context.Background(), &ReportData{Hostname: "node", CreatedAt: time.Now().Unix(), ReportID: "redirect-report"})
	}
	return []redirectOperation{
		{"realtime", "/api/realtime", func(r *Reporter) error {
			return r.SendRealtime(context.Background(), &RealtimeData{Hostname: "node"})
		}},
		{"legacy report", "/api/report", func(r *Reporter) error {
			return r.send(context.Background(), "/api/report", &ReportData{Hostname: "node"})
		}},
		{"history capabilities", "/api/report-capabilities", history},
		{"history report", "/api/report", history},
	}
}

func TestReporterRejectsUnsafeRedirectsAcrossAllOperations(t *testing.T) {
	for _, tc := range []struct{ name, server, location string }{
		{"downgrade on same port", "https://needle.example", "http://needle.example:443/escaped"},
		{"another host", "https://needle.example", "https://other.example/escaped"},
		{"child domain", "https://needle.example", "https://child.needle.example/escaped"},
		{"another port", "https://needle.example", "https://needle.example:8443/escaped"},
		{"embedded credentials", "https://needle.example", "https://user:redirect-secret@needle.example/escaped"},
		{"explicit HTTP still restricts origin", "http://needle.example:8080", "http://needle.example:9090/escaped"},
	} {
		for _, status := range []int{307, 308} {
			for _, op := range redirectOperations(t) {
				t.Run(fmt.Sprintf("%s/%d/%s", tc.name, status, op.name), func(t *testing.T) {
					server, err := ValidateServerURL(tc.server, true)
					if err != nil {
						t.Fatal(err)
					}
					r := NewReporter(server, redirectTestToken, false)
					escaped := 0
					r.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
						if req.URL.Path == "/escaped" {
							escaped++
							return response(200, redirectTestReply), nil
						}
						if req.URL.Path == op.path {
							resp := response(status, "untrusted redirect body: "+redirectTestToken)
							resp.Header.Set("Location", tc.location+"?secret="+redirectTestToken)
							return resp, nil
						}
						return response(200, redirectTestReply), nil
					})
					err = op.send(r)
					if err == nil || escaped != 0 || retryable(err) {
						t.Fatalf("redirect accepted or retried: err=%v escaped=%d", err, escaped)
					}
					for _, secret := range []string{redirectTestToken, "redirect-secret", tc.location, "untrusted redirect body"} {
						if strings.Contains(err.Error(), secret) {
							t.Fatalf("redirect details leaked into error: %v", err)
						}
					}
				})
			}
		}
	}
}

func TestReporterRejectsPostMethodChangesAndRedirectLoops(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := NewReporter("https://needle.example", redirectTestToken, false)
			calls := 0
			r.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				resp := response(status, "")
				resp.Header.Set("Location", "/again?secret="+redirectTestToken)
				return resp, nil
			})
			err := r.SendRealtime(context.Background(), &RealtimeData{Hostname: "node"})
			want := 1
			if status == 307 {
				want = 10
			}
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.Status != status || calls != want || retryable(err) || strings.Contains(err.Error(), redirectTestToken) {
				t.Fatalf("unsafe redirect chain: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestReportOriginUsesEffectivePortsAndExactZones(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		want     bool
	}{
		{"https://NEEDLE.example", "https://needle.example:443/path", true},
		{"http://needle.example", "http://needle.example:80/path", true},
		{"https://[::1]", "https://[::1]:443/path", true},
		{"https://needle.example", "https://needle.example:444/path", false},
		{"http://needle.example:443", "https://needle.example:443/path", false},
		{"https://[fe80::1%25eth0]", "https://[fe80::1%25ETH0]/path", false},
	} {
		a, err := url.Parse(tc.from)
		if err != nil {
			t.Fatal(err)
		}
		b, err := url.Parse(tc.to)
		if err != nil {
			t.Fatal(err)
		}
		if got := sameReportOrigin(a, b); got != tc.want {
			t.Fatalf("%s -> %s: %t, want %t", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestReporterAllowsSameOriginCapabilityRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := NewReporter("https://NEEDLE.example:443", redirectTestToken, false)
			calls := 0
			r.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer "+redirectTestToken {
					t.Fatalf("capability redirect changed method or authentication: %s %v", req.Method, req.Header)
				}
				if req.URL.Path == "/api/report-capabilities" {
					resp := response(status, "")
					resp.Header.Set("Location", "https://needle.example/capabilities")
					return resp, nil
				}
				return response(200, redirectTestReply), nil
			})
			sender := NewHistorySender(r, time.Minute, func(string, ...any) {})
			if err := sender.negotiate(context.Background()); err != nil || !sender.confirmed || calls != 2 {
				t.Fatalf("normal capability redirect failed: err=%v confirmed=%t calls=%d", err, sender.confirmed, calls)
			}
		})
	}
}

func TestReporterSameOriginTLSRedirectsPreserveRequests(t *testing.T) {
	for _, status := range []int{307, 308} {
		for _, op := range redirectOperations(t) {
			t.Run(fmt.Sprintf("%d/%s", status, op.name), func(t *testing.T) {
				type observed struct{ method, auth, body string }
				var mu sync.Mutex
				initial, final := map[string]observed{}, map[string]observed{}
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Error(err)
					}
					mu.Lock()
					defer mu.Unlock()
					observation := observed{req.Method, req.Header.Get("Authorization"), string(body)}
					if strings.HasPrefix(req.URL.Path, "/forward/") {
						final[strings.TrimPrefix(req.URL.Path, "/forward")] = observation
						io.WriteString(w, redirectTestReply)
					} else {
						initial[req.URL.Path] = observation
						w.Header().Set("Location", "/forward"+req.URL.Path)
						w.WriteHeader(status)
					}
				}))
				defer server.Close()
				r := NewReporter(server.URL, redirectTestToken, false)
				// Trust only the fixture CA; certificate verification remains enabled.
				tr := r.client.Transport.(*http.Transport)
				tr.TLSClientConfig.RootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
				defer r.client.CloseIdleConnections()
				if err := op.send(r); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(initial) == 0 || len(initial) != len(final) {
					t.Fatalf("requests missing: initial=%v final=%v", initial, final)
				}
				for path, before := range initial {
					if after, ok := final[path]; !ok || before != after || after.auth != "Bearer "+redirectTestToken {
						t.Fatalf("redirect changed request %s: before=%+v after=%+v", path, before, after)
					}
				}
			})
		}
	}
}

func TestReporterTLSNeverForwardsToPlainHTTP(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		io.WriteString(w, redirectTestReply)
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Location", target.URL+"/escaped")
		w.WriteHeader(307)
	}))
	defer source.Close()
	r := NewReporter(source.URL, redirectTestToken, false)
	tr := r.client.Transport.(*http.Transport)
	tr.TLSClientConfig.RootCAs = source.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	defer r.client.CloseIdleConnections()
	if err := r.SendRealtime(context.Background(), &RealtimeData{Hostname: "node"}); err == nil {
		t.Fatal("plaintext redirect accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("plaintext endpoint received %d requests", hits)
	}
}
