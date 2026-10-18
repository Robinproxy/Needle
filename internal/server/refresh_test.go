package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentRefreshDoesNotHideStorageErrors(t *testing.T) {
	for _, broken := range []string{"metrics", "agent_tcpping_current"} {
		t.Run(broken, func(t *testing.T) {
			h, store := newTestHandler(t)
			if _, err := store.UpsertAgent("node", "", nil, ""); err != nil {
				t.Fatal(err)
			}
			// With no samples this normally remains a valid, empty node card.
			w := httptest.NewRecorder()
			h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("empty node response = %d: %s", w.Code, w.Body.String())
			}
			if _, err := store.db.Exec("DROP TABLE " + broken); err != nil {
				t.Fatal(err)
			}
			w = httptest.NewRecorder()
			h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("broken %s returned %d, want 500 instead of apparently valid missing data", broken, w.Code)
			}
		})
	}
}
