package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func deleteConfirmation(t *testing.T, s *Store, id int64) map[string]any {
	t.Helper()
	nodes, _, err := s.NodeProfiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.ID == id {
			return map[string]any{"hostname": node.Hostname, "version": node.Version, "order_version": node.OrderVersion}
		}
	}
	t.Fatal("missing test node")
	return nil
}

func deleteBody(t *testing.T, input map[string]any) string {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestAdminDeleteNodeCommitsDataTokenAndLiveRemoval(t *testing.T) {
	h, s := newAdminTest(t)
	cookie, csrf := adminLogin(t, h, s)
	seed := seedAgentForDelete(t, s)
	other, err := s.UpsertAgent("retained", "US", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO traffic_baselines(agent_id,boundary,total_sent,total_recv,created_at) VALUES(?,1,100,200,1)`, seed.id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.snapshots(); err != nil {
		t.Fatal(err)
	}
	client, _ := s.live.subscribe()
	defer s.live.unsubscribe(client)
	body := deleteBody(t, deleteConfirmation(t, s, seed.id))
	w := adminCall(h, "DELETE", fmt.Sprintf("nodes/%d", seed.id), body, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertAgentDataCounts(t, s, seed.id, 0, 0, 0, 0)
	var remaining int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM traffic_baselines WHERE agent_id=?", seed.id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("traffic records retained", remaining, err)
	}
	nodes, _, err := s.NodeProfiles()
	if err != nil || len(nodes) != 1 || nodes[0].ID != other || nodes[0].Position != 1 {
		t.Fatal("other node or position changed", nodes, err)
	}
	h.committedMu.Lock()
	_, cached := h.committed[seed.id]
	h.committedMu.Unlock()
	if cached {
		t.Fatal("deleted node remained in committed cache")
	}
	select {
	case event := <-client:
		if event.Kind != "remove" || event.ID != seed.id {
			t.Fatal("wrong public update", event)
		}
	default:
		t.Fatal("no public removal event")
	}
	r := httptest.NewRequest("POST", "/api/report", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+seed.token)
	w = httptest.NewRecorder()
	h.handleReport(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("deleted Token still accepted", w.Code)
	}
	if w := adminCall(h, "DELETE", fmt.Sprintf("nodes/%d", seed.id), body, cookie, csrf); w.Code != 404 {
		t.Fatal("repeated deletion not rejected", w.Code)
	}
}

func TestAdminDeleteRejectsStaleIdentityVersionsAndInvalidBodies(t *testing.T) {
	h, s := newAdminTest(t)
	cookie, csrf := adminLogin(t, h, s)
	seed := seedAgentForDelete(t, s)
	confirmation := deleteConfirmation(t, s, seed.id)
	for _, key := range []string{"hostname", "version", "order_version"} {
		input := deleteConfirmation(t, s, seed.id)
		if key == "hostname" {
			input[key] = "another-node"
		} else {
			input[key] = input[key].(int64) + 1
		}
		if w := adminCall(h, "DELETE", fmt.Sprintf("nodes/%d", seed.id), deleteBody(t, input), cookie, csrf); w.Code != 409 {
			t.Fatal("stale confirmation accepted", key, w.Code)
		}
	}
	for _, body := range []string{`{}`, `null`, `{"hostname":"node-1","version":0}`, `{"hostname":"node-1","version":-1,"order_version":0}`, `{} {}`, strings.Repeat("x", 5000)} {
		if w := adminCall(h, "DELETE", fmt.Sprintf("nodes/%d", seed.id), body, cookie, csrf); w.Code != 400 {
			t.Fatal("invalid delete body accepted", w.Code)
		}
	}
	for _, path := range []string{"nodes/0", "nodes/-1", "nodes/1/extra"} {
		if w := adminCall(h, "DELETE", path, deleteBody(t, confirmation), cookie, csrf); w.Code != 400 {
			t.Fatal("invalid node path accepted", path, w.Code)
		}
	}
	assertAgentDataCounts(t, s, seed.id, 1, 1, 1, 1)
}

func TestAdminDeleteRequiresSessionOriginAndCSRF(t *testing.T) {
	h, s := newAdminTest(t)
	cookie, csrf := adminLogin(t, h, s)
	seed := seedAgentForDelete(t, s)
	path := fmt.Sprintf("nodes/%d", seed.id)
	body := deleteBody(t, deleteConfirmation(t, s, seed.id))
	if w := adminCall(h, "DELETE", path, body, nil, ""); w.Code != 401 {
		t.Fatal("anonymous deletion accepted", w.Code)
	}
	if w := adminCall(h, "DELETE", path, body, cookie, "wrong"); w.Code != 403 {
		t.Fatal("wrong CSRF accepted", w.Code)
	}
	r := httptest.NewRequest("DELETE", "/api/admin/"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+seed.token)
	w := httptest.NewRecorder()
	h.handleAdmin(w, r)
	if w.Code != 401 {
		t.Fatal("Agent Token accepted for deletion", w.Code)
	}
	w = httptest.NewRecorder()
	h.handleAgentDetail(w, httptest.NewRequest("DELETE", fmt.Sprintf("/api/agents/%d", seed.id), nil))
	if w.Code != 405 {
		t.Fatal("public node endpoint accepts deletion", w.Code)
	}
	for _, header := range []string{"Origin", "Content-Type", "X-Needle-Admin"} {
		r := httptest.NewRequest("DELETE", "/api/admin/"+path, strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("Origin", "https://needle.test")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Needle-Admin", "1")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set(header, "invalid")
		w := httptest.NewRecorder()
		h.handleAdmin(w, r)
		if w.Code != 403 {
			t.Fatal("unsafe delete request accepted", header, w.Code)
		}
	}
	assertAgentDataCounts(t, s, seed.id, 1, 1, 1, 1)
}

func TestAdminDeleteRollsBackWithoutPublishingRemoval(t *testing.T) {
	h, s := newAdminTest(t)
	cookie, csrf := adminLogin(t, h, s)
	seed := seedAgentForDelete(t, s)
	confirmation := deleteConfirmation(t, s, seed.id)
	client, _ := s.live.subscribe()
	defer s.live.unsubscribe(client)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON agent_tokens BEGIN SELECT RAISE(ABORT,'forced failure'); END`); err != nil {
		t.Fatal(err)
	}
	if w := adminCall(h, "DELETE", fmt.Sprintf("nodes/%d", seed.id), deleteBody(t, confirmation), cookie, csrf); w.Code != 500 {
		t.Fatal("delete failure not reported", w.Code)
	}
	assertAgentDataCounts(t, s, seed.id, 1, 1, 1, 1)
	if got := deleteConfirmation(t, s, seed.id); got["order_version"] != confirmation["order_version"] {
		t.Fatal("failed transaction advanced revision")
	}
	select {
	case <-client:
		t.Fatal("failed deletion published removal")
	default:
	}
}

func TestAdminDeleteOldPageCannotDeleteReusedID(t *testing.T) {
	for _, hostname := range []string{"replacement", "node-1"} {
		t.Run(hostname, func(t *testing.T) {
			h, s := newAdminTest(t)
			cookie, csrf := adminLogin(t, h, s)
			seed := seedAgentForDelete(t, s)
			body := deleteBody(t, deleteConfirmation(t, s, seed.id))
			if err := s.DeleteAgent(seed.id); err != nil {
				t.Fatal(err)
			}
			id, err := s.UpsertAgent(hostname, "US", nil, "")
			if err != nil || id != seed.id {
				t.Fatal("fixture did not reuse ID", id, err)
			}
			if w := adminCall(h, "DELETE", fmt.Sprintf("nodes/%d", id), body, cookie, csrf); w.Code != 409 {
				t.Fatal("stale page could delete replacement", w.Code)
			}
			if _, err := s.GetAgent(id); err != nil {
				t.Fatal("replacement deleted", err)
			}
		})
	}
}
