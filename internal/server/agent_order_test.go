package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

func assertAgentOrder(t *testing.T, s *Store, names ...string) {
	t.Helper()
	agents, err := s.GetAgents()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(agents))
	for i, a := range agents {
		got[i] = a.Hostname
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("order = %v, want %v", got, names)
	}
}

func TestMoveAgentPreservesDataAndAppendsNewNodes(t *testing.T) {
	s := newTransactionTestStore(t)
	seed := seedAgentForDelete(t, s)
	if _, err := s.db.Exec(`INSERT INTO traffic_baselines(agent_id,boundary,total_sent,total_recv,usage_sent,usage_recv,created_at) VALUES(?,1,100,200,300,400,1)`, seed.id); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node-2", "node-3"} {
		if _, err := s.UpsertAgent(name, "US", nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.GetAgents()
	metric, _ := s.GetLatestMetric(seed.id)
	tokens, _ := s.ListTokens()
	if err := s.MoveAgent("node-3", 1); err != nil {
		t.Fatal(err)
	}
	assertAgentOrder(t, s, "node-3", "node-1", "node-2")
	if err := s.MoveAgent(strconv.FormatInt(seed.id, 10), 3); err != nil {
		t.Fatal(err)
	}
	assertAgentOrder(t, s, "node-3", "node-2", "node-1")
	if err := s.MoveAgent("node-1", 3); err != nil {
		t.Fatal(err)
	}
	assertAgentDataCounts(t, s, seed.id, 1, 1, 1, 1)
	after, _ := s.GetAgents()
	for _, a := range after {
		old := before[a.ID-1]
		a.DisplayOrder, old.DisplayOrder = 0, 0
		if !reflect.DeepEqual(a, old) {
			t.Fatalf("move changed node metadata: %+v / %+v", a, old)
		}
	}
	newMetric, _ := s.GetLatestMetric(seed.id)
	newTokens, _ := s.ListTokens()
	if !reflect.DeepEqual(metric, newMetric) || !reflect.DeepEqual(tokens, newTokens) {
		t.Fatal("move changed metrics or tokens")
	}
	var sent, recv int64
	if err := s.db.QueryRow(`SELECT usage_sent,usage_recv FROM traffic_baselines WHERE agent_id=?`, seed.id).Scan(&sent, &recv); err != nil || sent != 300 || recv != 400 {
		t.Fatalf("move changed traffic usage: %d/%d (%v)", sent, recv, err)
	}
	// Agent reports must not reset a manually selected position.
	if _, err := s.SaveReport(&ReportWrite{Hostname: "node-3", Region: "US"}); err != nil {
		t.Fatal(err)
	}
	assertAgentOrder(t, s, "node-3", "node-2", "node-1")
	// Deleting the highest ID allows SQLite to reuse it; the new node must
	// still append after the remaining nodes, independently of that ID.
	if err := s.DeleteAgent(3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveReport(&ReportWrite{Hostname: "new-node"}); err != nil {
		t.Fatal(err)
	}
	assertAgentOrder(t, s, "node-2", "node-1", "new-node")
	// A separate CLI connection observes persistent order immediately.
	other, err := NewStoreCLI(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	assertAgentOrder(t, other, "node-2", "node-1", "new-node")
	if err := other.MoveAgent("new-node", 1); err != nil {
		t.Fatal(err)
	}
	assertAgentOrder(t, s, "new-node", "node-2", "node-1")
}

func TestMoveAgentValidationAndRollback(t *testing.T) {
	s := newTransactionTestStore(t)
	for _, name := range []string{"a", "b", "c"} {
		if _, err := s.UpsertAgent(name, "", nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		target string
		pos    int
	}{{"missing", 1}, {"999", 1}, {"a", 0}, {"a", -1}, {"a", 4}} {
		if err := s.MoveAgent(c.target, c.pos); err == nil {
			t.Fatalf("accepted invalid move: %+v", c)
		}
		assertAgentOrder(t, s, "a", "b", "c")
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_order BEFORE UPDATE OF display_order ON agents WHEN OLD.hostname='a' BEGIN SELECT RAISE(ABORT,'forced failure'); END`); err != nil {
		t.Fatal(err)
	}
	// c is updated first; failure on a must undo that earlier write too.
	if err := s.MoveAgent("c", 1); err == nil {
		t.Fatal("move succeeded despite failing trigger")
	}
	assertAgentOrder(t, s, "a", "b", "c")
}

func TestAgentOrderMigrationAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	// Model an older database with sparse IDs and no display order column.
	_, err = s.db.Exec(`ALTER TABLE agents DROP COLUMN display_order;
		INSERT INTO agents(id,hostname) VALUES(30,'third'),(10,'first'),(20,'second')`)
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentOrder(t, s, "first", "second", "third")
	if err := s.MoveAgent("third", 1); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertAgentOrder(t, s, "third", "first", "second")
	if _, err := s.UpsertAgent("fourth", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	assertAgentOrder(t, s, "third", "first", "second", "fourth")
	w := httptest.NewRecorder()
	NewHandler(s).handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var nodes []struct {
		Agent AgentRow `json:"agent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(nodes) != 4 || nodes[0].Agent.ID != 30 || nodes[0].Agent.DisplayOrder != 1 {
		t.Fatalf("API did not expose persistent order: %d %s", w.Code, w.Body.String())
	}
}

func TestMoveAgentConcurrentReports(t *testing.T) {
	s := newTransactionTestStore(t)
	if _, err := s.UpsertAgent("first", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	cli, err := NewStoreCLI(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	errors := make(chan error, 40)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, err := s.SaveReport(&ReportWrite{Hostname: fmt.Sprintf("node-%d", i)})
			errors <- err
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			errors <- cli.MoveAgent("first", 1)
		}
	}()
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	agents, err := s.GetAgents()
	if err != nil || len(agents) != 21 || agents[0].Hostname != "first" {
		t.Fatalf("concurrent move/report lost nodes: %v / %v", agents, err)
	}
	for i, a := range agents {
		if a.DisplayOrder != int64(i+1) {
			t.Fatalf("concurrent move/report produced duplicate or missing position: %+v", agents)
		}
	}
}
