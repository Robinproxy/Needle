package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type reportAdmissionBody struct {
	io.Reader
	beforeRead func()
	reads      int
}

func (b *reportAdmissionBody) Read(p []byte) (int, error) {
	b.reads++
	if b.beforeRead != nil {
		beforeRead := b.beforeRead
		b.beforeRead = nil
		beforeRead()
	}
	return b.Reader.Read(p)
}

func (b *reportAdmissionBody) Close() error { return nil }

func TestReportAdmissionRejectsBeforeReadingBody(t *testing.T) {
	for _, tc := range []struct {
		name, authorization string
		status              int
		prepare             func(*testing.T, *Handler, *Store)
	}{
		{"missing bearer", "", 401, nil},
		{"malformed bearer", "test-token", 401, nil},
		{"unknown token", "Bearer unknown", 401, nil},
		{"revoked token", "Bearer test-token", 401, func(t *testing.T, h *Handler, s *Store) {
			if err := s.RevokeToken("test-token"); err != nil {
				t.Fatal(err)
			}
		}},
		{"credential storage failure", "Bearer test-token", 500, func(t *testing.T, h *Handler, s *Store) {
			if _, err := s.db.Exec("ALTER TABLE agent_tokens RENAME TO unavailable_tokens"); err != nil {
				t.Fatal(err)
			}
		}},
		{"token rate limit", "Bearer test-token", 429, func(t *testing.T, h *Handler, s *Store) {
			row, err := s.LookupToken("test-token")
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < perTokenReportLimit; i++ {
				h.tokenLimiter.Allow(row.ID, h.now())
			}
		}},
		{"global rate limit", "Bearer test-token", 429, func(t *testing.T, h *Handler, s *Store) {
			for i := 0; i < globalReportLimit; i++ {
				h.globalLimiter.Allow(h.now())
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newTestHandler(t)
			if err := store.AllowToken("test-token"); err != nil {
				t.Fatal(err)
			}
			if tc.prepare != nil {
				tc.prepare(t, h, store)
			}
			body := &reportAdmissionBody{Reader: strings.NewReader(`{"hostname":"node-1"}`)}
			req := httptest.NewRequest(http.MethodPost, "/api/report", body)
			req.Header.Set("Authorization", tc.authorization)
			w := httptest.NewRecorder()
			h.handleReport(w, req)
			if w.Code != tc.status || body.reads != 0 || len(h.reportSlots) != 0 || w.Header().Get("Connection") != "close" {
				t.Fatalf("status=%d reads=%d slots=%d headers=%v", w.Code, body.reads, len(h.reportSlots), w.Header())
			}
			if tc.status == 429 && w.Header().Get("Retry-After") != "1" {
				t.Fatal("missing retry hint")
			}
			if nodes, err := store.GetAgents(); err != nil || len(nodes) != 0 {
				t.Fatalf("rejected report wrote nodes: %v / %v", nodes, err)
			}
		})
	}
}

func TestReportAdmissionBoundsConcurrentBodiesAndRecovers(t *testing.T) {
	h, store := newTestHandler(t)
	store.now = h.now
	started := make(chan struct{}, cap(h.reportSlots))
	release := make(chan struct{})
	var wg sync.WaitGroup
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }); wg.Wait() }
	t.Cleanup(finish)
	results := make(chan int, cap(h.reportSlots))
	for i := 0; i < cap(h.reportSlots); i++ {
		token := fmt.Sprintf("slow-token-%d", i)
		if err := store.AllowToken(token); err != nil {
			t.Fatal(err)
		}
		body := &reportAdmissionBody{
			Reader: strings.NewReader(fmt.Sprintf(`{"hostname":"slow-node-%d"}`, i)),
			beforeRead: func() {
				started <- struct{}{}
				<-release
			},
		}
		req := httptest.NewRequest(http.MethodPost, "/api/report", body)
		req.Header.Set("Authorization", "Bearer "+token)
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.handleReport(w, req)
			results <- w.Code
		}()
	}
	for i := 0; i < cap(h.reportSlots); i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("admitted requests did not reach their body readers")
		}
	}
	if err := store.AllowToken("overflow-token"); err != nil {
		t.Fatal(err)
	}
	report := fmt.Sprintf(`{"hostname":"overflow-node","report_id":"0123456789abcdef0123456789abcdef:1","created_at":%d,"cpu":{"percent":10}}`, h.now().Unix())
	body := &reportAdmissionBody{Reader: strings.NewReader(report)}
	req := httptest.NewRequest(http.MethodPost, "/api/report", body)
	req.Header.Set("Authorization", "Bearer overflow-token")
	w := httptest.NewRecorder()
	h.handleReport(w, req)
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || w.Header().Get("Connection") != "close" || body.reads != 0 {
		t.Fatalf("overload: status=%d headers=%v reads=%d", w.Code, w.Header(), body.reads)
	}
	if row, err := store.LookupToken("overflow-token"); err != nil || row.Hostname != "" {
		t.Fatalf("rejected request changed token binding: %v / %v", row, err)
	}
	if nodes, err := store.GetAgents(); err != nil || len(nodes) != 0 {
		t.Fatalf("blocked requests wrote nodes: %v / %v", nodes, err)
	}
	finish()
	for i := 0; i < cap(h.reportSlots); i++ {
		if status := <-results; status != 200 {
			t.Fatalf("admitted request failed: %d", status)
		}
	}
	if len(h.reportSlots) != 0 {
		t.Fatal("completed requests leaked slots")
	}
	if retry := sendReport(t, h, "Bearer overflow-token", report); retry.Code != 200 {
		t.Fatalf("retry failed: %d %s", retry.Code, retry.Body.String())
	}
	if duplicate := sendReport(t, h, "Bearer overflow-token", report); duplicate.Code != 200 || !strings.Contains(duplicate.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate retry failed: %d %s", duplicate.Code, duplicate.Body.String())
	}
	for _, table := range []string{"metrics", "report_receipts"} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("overload retry changed %s: count=%d err=%v", table, count, err)
		}
	}
}

func TestReportAdmissionRechecksCredentialsAtCommit(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoke=%t", revoke), func(t *testing.T) {
			h, store := newTestHandler(t)
			if err := store.AllowToken("test-token"); err != nil {
				t.Fatal(err)
			}
			body := &reportAdmissionBody{
				Reader: strings.NewReader(`{"hostname":"node-1","cpu":{"percent":10}}`),
				beforeRead: func() {
					var err error
					if revoke {
						err = store.RevokeToken("test-token")
					} else {
						err = store.BindToken("test-token", "another-node")
					}
					if err != nil {
						t.Fatal(err)
					}
				},
			}
			req := httptest.NewRequest(http.MethodPost, "/api/report", body)
			req.Header.Set("Authorization", "Bearer test-token")
			w := httptest.NewRecorder()
			h.handleReport(w, req)
			want := 409
			if revoke {
				want = 401
			}
			if w.Code != want || len(h.reportSlots) != 0 {
				t.Fatalf("status=%d, want=%d; slots=%d", w.Code, want, len(h.reportSlots))
			}
			if nodes, err := store.GetAgents(); err != nil || len(nodes) != 0 {
				t.Fatalf("stale authorization wrote nodes: %v / %v", nodes, err)
			}
		})
	}
}

func TestReportAdmissionCountsInvalidBodiesAndReleasesSlots(t *testing.T) {
	h, store := newTestHandler(t)
	now := h.now()
	h.now = func() time.Time { return now }
	if err := store.AllowToken("test-token"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{`, `{"hostname":"node-1","cpu":{"percent":101}}`} {
		if w := sendReport(t, h, "Bearer test-token", body); w.Code != 400 || len(h.reportSlots) != 0 {
			t.Fatalf("invalid body: status=%d slots=%d", w.Code, len(h.reportSlots))
		}
	}
	if w := sendReport(t, h, "Bearer test-token", `{"hostname":"node-1"}`); w.Code != 429 {
		t.Fatalf("invalid bodies bypassed the token limit: %d", w.Code)
	}
	if row, err := store.LookupToken("test-token"); err != nil || row.Hostname != "" {
		t.Fatalf("invalid bodies bound the token: %v / %v", row, err)
	}
	now = now.Add(time.Second)
	if w := sendReport(t, h, "Bearer test-token", `{"hostname":"node-1"}`); w.Code != 200 || len(h.reportSlots) != 0 {
		t.Fatalf("admission did not recover: status=%d slots=%d", w.Code, len(h.reportSlots))
	}
}

func TestReportAdmissionRejectsSlowHTTPBodyImmediately(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/report", h.handleReport)
	mux.HandleFunc("/api/realtime", h.handleRealtime)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ReadTimeout = 5 * time.Second
	srv.Start()
	defer srv.Close()
	for _, path := range []string{"/api/report", "/api/realtime"} {
		t.Run(path, func(t *testing.T) {
			for _, authorization := range []string{"", "Bearer unknown"} {
				conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), 2*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				func() {
					defer conn.Close()
					if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
						t.Fatal(err)
					}
					// Deliberately send no body, below net/http's automatic drain threshold.
					if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Length: 1024\r\nAuthorization: %s\r\n\r\n", path, authorization); err != nil {
						t.Fatal(err)
					}
					resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
					if err != nil {
						t.Fatalf("rejection waited for an unauthenticated body: %v", err)
					}
					defer resp.Body.Close()
					if resp.StatusCode != 401 || !resp.Close {
						t.Fatalf("status=%d close=%t", resp.StatusCode, resp.Close)
					}
				}()
			}
		})
	}
}
