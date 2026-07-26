package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleAgentsIncludesTrafficAvailability(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("test-token"); err != nil {
		t.Fatal(err)
	}
	if w := sendReport(t, h, "Bearer test-token", `{"hostname":"node-1","network":{"total_sent":30,"total_recv":40}}`); w.Code != http.StatusOK {
		t.Fatalf("report status = %d, body=%s", w.Code, w.Body.String())
	}

	w := httptest.NewRecorder()
	h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got []struct {
		Agent   AgentRow     `json:"agent"`
		Traffic TrafficUsage `json:"traffic"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Traffic.Available || got[0].Traffic.HasData || got[0].Traffic.Reason != "billing_not_configured" {
		t.Fatalf("traffic = %+v, want billing_not_configured", got)
	}
	if got[0].Agent.LastSeen == nil {
		t.Fatal("agent.last_seen missing after a report")
	}
}

func newTestHandler(t *testing.T) (*Handler, *Store) {
	t.Helper()
	store, err := NewStoreCLI(filepath.Join(t.TempDir(), "needle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := NewHandler(store)
	h.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	return h, store
}

func sendReport(t *testing.T, h *Handler, authorization, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/report", strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	h.handleReport(w, req)
	return w
}

func TestHandleReportRequiresStrictBearerScheme(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("test-token"); err != nil {
		t.Fatal(err)
	}
	w := sendReport(t, h, "test-token", `{"hostname":"node-1"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleReportRejectsTrailingJSON(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("test-token"); err != nil {
		t.Fatal(err)
	}
	w := sendReport(t, h, "Bearer test-token", `{"hostname":"node-1"}{"hostname":"node-2"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleReportRateLimitDoesNotWrite(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("test-token"); err != nil {
		t.Fatal(err)
	}
	body := `{"hostname":"node-1","cpu":{"percent":10}}`
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		w := sendReport(t, h, "Bearer test-token", body)
		if w.Code != want {
			t.Fatalf("request %d status = %d, want %d; body=%s", i+1, w.Code, want, w.Body.String())
		}
	}

	agents, err := store.GetAgents()
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents = %d, err = %v", len(agents), err)
	}
	metrics, err := store.GetMetrics(agents[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 2 {
		t.Fatalf("metric rows = %d, want 2", len(metrics))
	}
}

func TestHandleReportCreatesTrafficBaseline(t *testing.T) {
	h, store := newTestHandler(t)
	store.now = h.now
	now := h.now()
	if err := store.AllowToken("test-token"); err != nil {
		t.Fatal(err)
	}

	expiresAt := now.AddDate(0, 1, 0).Unix()
	report := func(sent, recv int64) string {
		return fmt.Sprintf(
			`{"hostname":"node-1","expires_at":%d,"billing_period":"1m","cpu":{"percent":10},"network":{"total_sent":%d,"total_recv":%d}}`,
			expiresAt, sent, recv)
	}
	if w := sendReport(t, h, "Bearer test-token", report(100, 200)); w.Code != http.StatusOK {
		t.Fatalf("report status = %d, body=%s", w.Code, w.Body.String())
	}

	boundary := monthlyBoundary(time.Unix(expiresAt, 0).Day(), now).Unix()
	var baseSent, baseRecv int64
	if err := store.db.QueryRow(
		`SELECT total_sent, total_recv FROM traffic_baselines WHERE boundary = ?`, boundary,
	).Scan(&baseSent, &baseRecv); err != nil {
		t.Fatalf("baseline row: %v", err)
	}
	if baseSent != 100 || baseRecv != 200 {
		t.Fatalf("baseline = %d/%d, want 100/200", baseSent, baseRecv)
	}

	if w := sendReport(t, h, "Bearer test-token", report(600, 900)); w.Code != http.StatusOK {
		t.Fatalf("second report status = %d, body=%s", w.Code, w.Body.String())
	}
	agents, err := store.GetAgents()
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents = %d, err = %v", len(agents), err)
	}
	usage, err := store.GetTrafficUsage(agents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !usage.HasData || usage.Sent != 500 || usage.Recv != 700 {
		t.Fatalf("usage = %+v, want sent=500 recv=700", usage)
	}
}

func TestStaticETagRevalidation(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	etag := w.Header().Get("ETag")
	if etag == "" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("missing cache headers: etag=%q cache-control=%q", etag, w.Header().Get("Cache-Control"))
	}

	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want %d", w.Code, http.StatusNotModified)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("304 response carried a %d-byte body", w.Body.Len())
	}

	// The index route ("/") must revalidate too.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || w.Header().Get("ETag") == "" {
		t.Fatalf("index: status = %d, etag = %q", w.Code, w.Header().Get("ETag"))
	}
}

func TestHandleReportIsAtomic(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("test-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`CREATE TRIGGER fail_ping BEFORE INSERT ON tcpping_results BEGIN SELECT RAISE(ABORT, 'forced failure'); END`,
	); err != nil {
		t.Fatal(err)
	}

	body := `{"hostname":"node-1","cpu":{"percent":10},"tcpping":[{"name":"CMv4","target":"example.com:80","latency_ms":5,"success":true}]}`
	if w := sendReport(t, h, "Bearer test-token", body); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}

	for _, table := range []string{"agents", "metrics", "tcpping_results"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows after failed report = %d, want 0 (partial write)", table, count)
		}
	}
}

func TestHandleReportRejectsInvalidValuesBeforeBinding(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("test-token"); err != nil {
		t.Fatal(err)
	}
	w := sendReport(t, h, "Bearer test-token", `{"hostname":"node-1","cpu":{"percent":101}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	token, err := store.LookupToken("test-token")
	if err != nil {
		t.Fatal(err)
	}
	if token.Hostname != "" {
		t.Fatalf("invalid report bound token to %q", token.Hostname)
	}
}
