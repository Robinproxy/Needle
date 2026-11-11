package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"needle/internal/logging"
)

func TestRemovedUnregisterEndpointCannotChangeCredentials(t *testing.T) {
	s := newTransactionTestStore(t)
	seed := seedAgentForDelete(t, s)
	h := NewHandler(s)
	mux := http.NewServeMux()
	h.Register(mux)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, auth := range []string{"", "Bearer unknown", "Bearer " + seed.token} {
			body := &reportAdmissionBody{Reader: strings.NewReader(`{"hostname":"node-1"}`)}
			req := httptest.NewRequest(method, "/api/unregister", body)
			req.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound || body.reads != 0 {
				t.Fatalf("%s with %q: status=%d reads=%d", method, auth, w.Code, body.reads)
			}
		}
	}
	if _, err := s.LookupToken(seed.token); err != nil {
		t.Fatal("removed endpoint changed credential", err)
	}
	assertAgentDataCounts(t, s, seed.id, 1, 1, 1, 1)
}

func TestReportCapabilitiesOnlyAdvertiseReporting(t *testing.T) {
	h, s := newTestHandler(t)
	if err := s.AllowToken("report-only-token"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	for _, auth := range []string{"", "Bearer unknown", "Bearer report-only-token"} {
		req := httptest.NewRequest(http.MethodGet, "/api/report-capabilities", nil)
		req.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if auth != "Bearer report-only-token" {
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("capabilities without valid credentials: %d", w.Code)
			}
			continue
		}
		var capability map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &capability); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || len(capability) != 2 || capability["protocol"] != reportProtocol || capability["max_report_age_seconds"] != reportAcceptAge.Seconds() {
			t.Fatalf("report negotiation changed: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestConcurrentAdministratorRevocationAndReportCannotResurrectToken(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		s := newTransactionTestStore(t)
		seed := seedAgentForDelete(t, s)
		h := NewHandler(s)
		var reportLog string
		h.logs = logging.NewTracker("server.api", func(format string, args ...any) {
			reportLog = fmt.Sprintf(format, args...)
		})
		done := make(chan error, 1)
		go func() { done <- s.RevokeToken(seed.token) }()
		req := httptest.NewRequest(http.MethodPost, "/api/report", strings.NewReader(`{"hostname":"node-1","uptime":100}`))
		req.Header.Set("Authorization", "Bearer "+seed.token)
		w := httptest.NewRecorder()
		h.handleReport(w, req)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		// Administrator CLI writes do not share the HTTP handler lock. SQLite
		// may reject an overlapping report; the existing sender retries 500s.
		// Only that known transient error is allowed alongside success/401.
		transientLock := w.Code == http.StatusInternalServerError && strings.Contains(reportLog, "database is locked")
		if w.Code != http.StatusOK && w.Code != http.StatusUnauthorized && !transientLock {
			t.Fatal(w.Code, w.Body.String())
		}
		if _, err := s.LookupToken(seed.token); !errors.Is(err, ErrTokenNotFound) {
			t.Fatal("report restored revoked credential", err)
		}
		if after := sendReport(t, h, "Bearer "+seed.token, `{"hostname":"node-1"}`); after.Code != http.StatusUnauthorized {
			t.Fatalf("report accepted after administrator revocation: %d", after.Code)
		}
		var agents, pings int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&agents); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM tcpping_results`).Scan(&pings); err != nil {
			t.Fatal(err)
		}
		if agents != 1 || pings != 1 {
			t.Fatal("administrator revocation deleted stored data", agents, pings)
		}
	}
}
