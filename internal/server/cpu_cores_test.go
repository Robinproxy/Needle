package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentpkg "needle/internal/agent"
	"needle/internal/agent/collector"
)

func TestCPUCoreCountReportAndDashboard(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("core-test"); err != nil {
		t.Fatal(err)
	}
	// Use the Agent's real wire struct, so a renamed or missing field fails here.
	data := agentpkg.ReportData{Hostname: "four-core", CPU: &collector.CPUStats{Percent: 12, Cores: 4}, Load: &collector.LoadStats{Load1: .14}}
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if w := sendReport(t, h, "Bearer core-test", string(payload)); w.Code != http.StatusOK {
		t.Fatalf("report %d: %s", w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var nodes []struct {
		Agent  AgentRow  `json:"agent"`
		Metric MetricRow `json:"latest_metric"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Agent.CPUCores != 4 || nodes[0].Metric.Load1 != .14 {
		t.Fatalf("dashboard: %s", w.Body.String())
	}
	// A rejected report must not partially update CPU metadata.
	if _, err := store.db.Exec(`CREATE TRIGGER fail_core_report BEFORE INSERT ON metrics BEGIN SELECT RAISE(ABORT,'forced failure'); END`); err != nil {
		t.Fatal(err)
	}
	data.CPU.Cores = 8
	payload, _ = json.Marshal(data)
	if w := sendReport(t, h, "Bearer core-test", string(payload)); w.Code != http.StatusInternalServerError {
		t.Fatalf("expected rejected report, got %d", w.Code)
	}
	rows, err := store.GetAgents()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].CPUCores != 4 {
		t.Fatal("failed report changed CPU metadata")
	}
}
