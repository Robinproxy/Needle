package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRealtimeAdmissionRejectsBeforeReadingBody(t *testing.T) {
	for _, tc := range []struct {
		name, authorization string
		status              int
		prepare             func(*testing.T, *Handler, *Store)
	}{
		{"missing bearer", "", 401, nil},
		{"malformed bearer", "live-secret", 401, nil},
		{"unknown token", "Bearer unknown", 401, nil},
		{"unbound token", "Bearer not-bound", 401, func(t *testing.T, h *Handler, s *Store) {
			if err := s.AllowToken("not-bound"); err != nil {
				t.Fatal(err)
			}
		}},
		{"revoked token", "Bearer live-secret", 401, func(t *testing.T, h *Handler, s *Store) {
			if err := s.RevokeToken("live-secret"); err != nil {
				t.Fatal(err)
			}
		}},
		{"credential storage failure", "Bearer live-secret", 500, func(t *testing.T, h *Handler, s *Store) {
			if _, err := s.db.Exec("ALTER TABLE agent_tokens RENAME TO unavailable_tokens"); err != nil {
				t.Fatal(err)
			}
		}},
		{"token rate limit", "Bearer live-secret", 429, func(t *testing.T, h *Handler, s *Store) {
			row, err := s.LookupToken("live-secret")
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < perTokenReportLimit; i++ {
				h.realtimeLimiter.Allow(row.ID, h.now())
			}
		}},
		{"global rate limit", "Bearer live-secret", 429, func(t *testing.T, h *Handler, s *Store) {
			for i := 0; i < globalReportLimit; i++ {
				h.realtimeGlobal.Allow(h.now())
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store, now, _ := realtimeFixture(t)
			if tc.prepare != nil {
				tc.prepare(t, h, store)
			}
			body := &reportAdmissionBody{Reader: strings.NewReader(fmt.Sprintf(`{"hostname":"live","created_at":%d}`, now.Unix()))}
			req := httptest.NewRequest(http.MethodPost, "/api/realtime", body)
			req.Header.Set("Authorization", tc.authorization)
			w := httptest.NewRecorder()
			h.handleRealtime(w, req)
			if w.Code != tc.status || body.reads != 0 || len(h.realtimeSlots) != 0 || w.Header().Get("Connection") != "close" {
				t.Fatalf("status=%d reads=%d slots=%d headers=%v", w.Code, body.reads, len(h.realtimeSlots), w.Header())
			}
			if tc.status == 429 && w.Header().Get("Retry-After") != "1" {
				t.Fatal("missing retry hint")
			}
		})
	}
}

func TestUnauthenticatedRealtimeBodiesCannotBlockReports(t *testing.T) {
	h, _, now, _ := realtimeFixture(t)
	sample := fmt.Sprintf(`{"hostname":"live","created_at":%d,"cpu":{"percent":10}}`, now.Unix())
	release := make(chan struct{})
	var workers sync.WaitGroup
	t.Cleanup(func() { close(release); workers.Wait() })
	type result struct{ status, reads int }
	results := make(chan result, cap(h.realtimeSlots))
	for i := 0; i < cap(h.realtimeSlots); i++ {
		body := &reportAdmissionBody{
			Reader:     strings.NewReader(sample),
			beforeRead: func() { <-release },
		}
		req := httptest.NewRequest(http.MethodPost, "/api/realtime", body)
		if i%2 != 0 {
			req.Header.Set("Authorization", "Bearer unknown")
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			w := httptest.NewRecorder()
			h.handleRealtime(w, req)
			results <- result{w.Code, body.reads}
		}()
	}
	for i := 0; i < cap(h.realtimeSlots); i++ {
		select {
		case got := <-results:
			if got.status != 401 || got.reads != 0 {
				t.Fatalf("unauthenticated request: %+v", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("unauthenticated request waited for its body")
		}
	}
	if w := sendRealtime(t, h, "live-secret", sample); w.Code != 200 {
		t.Fatalf("anonymous bodies blocked realtime: %d %s", w.Code, w.Body.String())
	}
	if w := sendReport(t, h, "Bearer live-secret", sample); w.Code != 200 {
		t.Fatalf("anonymous bodies blocked history: %d %s", w.Code, w.Body.String())
	}
}

func TestRealtimeAdmissionCountsInvalidBodiesAndRechecksRevocation(t *testing.T) {
	t.Run("invalid bodies consume the token limit", func(t *testing.T) {
		h, _, now, _ := realtimeFixture(t)
		for i := 0; i < perTokenReportLimit; i++ {
			if w := sendRealtime(t, h, "live-secret", `{`); w.Code != 400 || len(h.realtimeSlots) != 0 {
				t.Fatalf("invalid body: status=%d slots=%d", w.Code, len(h.realtimeSlots))
			}
		}
		body := &reportAdmissionBody{Reader: strings.NewReader(`{`)}
		req := httptest.NewRequest(http.MethodPost, "/api/realtime", body)
		req.Header.Set("Authorization", "Bearer live-secret")
		w := httptest.NewRecorder()
		h.handleRealtime(w, req)
		if w.Code != 429 || body.reads != 0 {
			t.Fatalf("invalid bodies bypassed the token limit: status=%d reads=%d", w.Code, body.reads)
		}
		*now = now.Add(time.Second)
		if w := sendRealtime(t, h, "live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d}`, now.Unix())); w.Code != 200 {
			t.Fatalf("admission did not recover: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("revocation while reading the body", func(t *testing.T) {
		h, store, now, id := realtimeFixture(t)
		body := &reportAdmissionBody{
			Reader: strings.NewReader(fmt.Sprintf(`{"hostname":"live","created_at":%d,"cpu":{"percent":10}}`, now.Unix())),
			beforeRead: func() {
				if err := store.RevokeToken("live-secret"); err != nil {
					t.Fatal(err)
				}
			},
		}
		req := httptest.NewRequest(http.MethodPost, "/api/realtime", body)
		req.Header.Set("Authorization", "Bearer live-secret")
		w := httptest.NewRecorder()
		h.handleRealtime(w, req)
		if w.Code != 401 || len(h.realtimeSlots) != 0 {
			t.Fatalf("stale authorization accepted: status=%d slots=%d", w.Code, len(h.realtimeSlots))
		}
		if _, found := store.live.nodes[id]; found {
			t.Fatal("revoked node reentered the live cache")
		}
	})
}
