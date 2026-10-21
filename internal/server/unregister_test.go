package server

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestUnregisterOnlyRevokesCredential(t *testing.T) {
	s := newTransactionTestStore(t)
	seed := seedAgentForDelete(t, s)
	h := NewHandler(s)
	for index, host := range []string{"someone-else", "node-1", "node-1"} {
		req := httptest.NewRequest("POST", "/api/unregister", bytes.NewBufferString(`{"hostname":"`+host+`"}`))
		req.Header.Set("Authorization", "Bearer "+seed.token)
		w := httptest.NewRecorder()
		h.handleUnregister(w, req)
		want := 401
		if index == 1 {
			want = 200
		}
		if host == "node-1" {
			if _, err := s.LookupToken(seed.token); !errors.Is(err, ErrTokenNotFound) {
				t.Fatal(err)
			}
		}

		if w.Code != want {
			t.Fatalf("%s: %d %s", host, w.Code, w.Body.String())
		}
	}
	assertAgentDataCounts(t, s, seed.id, 1, 1, 1, 0)
	if _, err := s.SaveReport(&ReportWrite{Token: seed.token, Hostname: "node-1", Metric: &MetricRow{}}); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("revoked report: %v", err)
	}
	assertAgentDataCounts(t, s, seed.id, 1, 1, 1, 0)
}
func TestUnregisterBoundTokenWithMissingNode(t *testing.T) {
	s := newTransactionTestStore(t)
	s.AllowToken("orphan")
	s.BindToken("orphan", "gone")
	h := NewHandler(s)
	req := httptest.NewRequest("POST", "/api/unregister", bytes.NewBufferString(`{"hostname":"gone"}`))
	req.Header.Set("Authorization", "Bearer orphan")
	w := httptest.NewRecorder()
	h.handleUnregister(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := s.LookupToken("orphan"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatal(err)
	}
}

func TestConcurrentUnregisterAndReportCannotDeleteOrResurrect(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		s := newTransactionTestStore(t)
		seed := seedAgentForDelete(t, s)
		h := NewHandler(s)
		done := make(chan int, 2)
		go func() {
			req := httptest.NewRequest("POST", "/api/unregister", bytes.NewBufferString(`{"hostname":"node-1"}`))
			req.Header.Set("Authorization", "Bearer "+seed.token)
			w := httptest.NewRecorder()
			h.handleUnregister(w, req)
			done <- w.Code
		}()
		go func() {
			req := httptest.NewRequest("POST", "/api/report", bytes.NewBufferString(`{"hostname":"node-1","uptime":100}`))
			req.Header.Set("Authorization", "Bearer "+seed.token)
			w := httptest.NewRecorder()
			h.handleReport(w, req)
			done <- w.Code
		}()
		for range 2 {
			code := <-done
			if code != 200 && code != 401 {
				t.Fatal(code)
			}
		}
		if _, err := s.LookupToken(seed.token); !errors.Is(err, ErrTokenNotFound) {
			t.Fatal(err)
		}
		var agents, pings int
		s.db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&agents)
		s.db.QueryRow(`SELECT COUNT(*) FROM tcpping_results`).Scan(&pings)
		if agents != 1 || pings != 1 {
			t.Fatal("unregistration deleted stored data", agents, pings)
		}
	}
}
