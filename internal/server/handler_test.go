package server

import (
	"encoding/json"
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
		Traffic TrafficUsage `json:"traffic"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Traffic.Available || got[0].Traffic.HasData || got[0].Traffic.Reason != "billing_not_configured" {
		t.Fatalf("traffic = %+v, want billing_not_configured", got)
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
