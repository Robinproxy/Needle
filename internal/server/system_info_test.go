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

	agentpkg "needle/internal/agent"
)

func TestSystemInformationWireStorageAndCompatibility(t *testing.T) {
	h, store := newTestHandler(t)
	now := h.now()
	h.now = func() time.Time { return now }
	store.now = h.now
	if err := store.AllowToken("system-test"); err != nil {
		t.Fatal(err)
	}
	data := agentpkg.ReportData{Hostname: "router", CPUModel: "Intel N150", OSName: "ImmortalWrt 24.10.4", Arch: "x86_64", VirtualizationSystem: "kvm", VirtualizationRole: "host", CreatedAt: h.now().Unix()}
	// The first CPU measurement may still be waiting for its baseline. Static
	// metadata must not depend on that utilization sample being available.
	payload, _ := json.Marshal(data)
	if w := sendReport(t, h, "Bearer system-test", string(payload)); w.Code != http.StatusOK {
		t.Fatalf("report rejected: %d %s", w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var nodes []struct {
		Agent AgentRow `json:"agent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &nodes); err != nil || len(nodes) != 1 {
		t.Fatalf("dashboard missing node: %s / %v", w.Body.String(), err)
	}
	id := nodes[0].Agent.ID
	assertInfo := func() {
		t.Helper()
		row, err := store.GetAgent(id)
		if err != nil || row.CPUModel != data.CPUModel || row.OSName != data.OSName || row.Arch != data.Arch || row.VirtualizationSystem != data.VirtualizationSystem || row.VirtualizationRole != data.VirtualizationRole {
			t.Fatalf("metadata lost or rolled back: %+v / %v", row, err)
		}
	}
	assertInfo()
	if nodes[0].Agent.CPUModel != data.CPUModel || nodes[0].Agent.OSName != data.OSName || nodes[0].Agent.Arch != data.Arch || nodes[0].Agent.VirtualizationSystem != data.VirtualizationSystem || nodes[0].Agent.VirtualizationRole != data.VirtualizationRole {
		t.Fatalf("dashboard lost system metadata: %s", w.Body.String())
	}
	for _, body := range []string{
		`{"hostname":"router"}`,
		`{"hostname":"router","cpu_model":"","os_name":"","arch":"","virtualization_system":"","virtualization_role":""}`,
		fmt.Sprintf(`{"hostname":"router","created_at":%d,"cpu_model":"stale","os_name":"stale","arch":"stale","virtualization_system":"docker","virtualization_role":"guest"}`, h.now().Add(-time.Minute).Unix()),
	} {
		now = now.Add(time.Second)
		if w := sendReport(t, h, "Bearer system-test", body); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		assertInfo()
	}
	// The metadata and business rows must still commit or fail together.
	if _, err := store.db.Exec(`CREATE TRIGGER fail_system_report BEFORE INSERT ON metrics BEGIN SELECT RAISE(ABORT,'forced failure'); END`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if w := sendReport(t, h, "Bearer system-test", `{"hostname":"router","cpu_model":"changed","os_name":"changed","arch":"aarch64","virtualization_system":"docker","virtualization_role":"guest","cpu":{"percent":10}}`); w.Code != 500 {
		t.Fatal(w.Code)
	}
	assertInfo()
}

func TestSystemInformationValidationAndRealtimeIsolation(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.AllowToken("system-test"); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"cpu_model": strings.Repeat("x", 257), "os_name": "Linux\nforged", "arch": strings.Repeat("x", 33), "virtualization_system": strings.Repeat("x", 65), "virtualization_role": "other"} {
		payload, _ := json.Marshal(map[string]any{"hostname": "node", key: value})
		if w := sendReport(t, h, "Bearer system-test", string(payload)); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid %s accepted: %d", key, w.Code)
		}
	}
	for _, body := range []string{
		`{"hostname":"node","virtualization_system":"KVM\nforged"}`,
		`{"hostname":"node","virtualization_system":"kvm","virtualization_role":"other"}`,
		`{"hostname":"node","virtualization_role":"host"}`,
	} {
		if w := sendReport(t, h, "Bearer system-test", body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid virtualization accepted: %d / %s", w.Code, body)
		}
	}
	rows, err := store.GetAgents()
	if err != nil || len(rows) != 0 {
		t.Fatalf("rejected metadata created a node: %+v / %v", rows, err)
	}
	data := agentpkg.ReportData{Hostname: "node", ServerManaged: true, CPUModel: "private model", OSName: "private OS", Arch: "x86_64", VirtualizationSystem: "kvm", VirtualizationRole: "guest"}
	encoded, _ := json.Marshal(agentpkg.RealtimeFromReport(&data))
	for _, field := range []string{"cpu_model", "os_name", "arch", "server_managed", "virtualization_system", "virtualization_role"} {
		if strings.Contains(string(encoded), field) {
			t.Fatalf("realtime includes static metadata: %s", encoded)
		}
	}
	// Omitted metadata must leave the legacy canonical report (and its stored
	// deduplication digest) unchanged across a Server upgrade.
	legacy, _ := json.Marshal(reportRequest{Hostname: "legacy"})
	for _, field := range []string{"cpu_model", "os_name", "arch", "server_managed", "virtualization_system", "virtualization_role"} {
		if strings.Contains(string(legacy), field) {
			t.Fatalf("new default fields change legacy report digests: %s", legacy)
		}
	}
}

func TestSystemInformationMigrationPreservesExistingNodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "needle.db")
	store, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.SaveReport(&ReportWrite{Hostname: "existing", Region: "US", CPUCores: 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"cpu_model", "os_name", "arch", "virtualization_system", "virtualization_role"} {
		if _, err := store.db.Exec("ALTER TABLE agents DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	for i := 0; i < 2; i++ {
		store, err = NewStoreCLI(path)
		if err != nil {
			t.Fatal(err)
		}
		row, err := store.GetAgent(id)
		if err != nil || row.Hostname != "existing" || row.Region != "US" || row.CPUCores != 4 || row.CPUModel != "" || row.OSName != "" || row.Arch != "" || row.VirtualizationSystem != "" || row.VirtualizationRole != "" {
			t.Fatalf("migration changed an existing node: %+v / %v", row, err)
		}
		store.Close()
	}
}

func TestVirtualizationChangesReplaceRoleTogether(t *testing.T) {
	h, store := newTestHandler(t)
	now := h.now()
	h.now = func() time.Time { return now }
	store.now = h.now
	if err := store.AllowToken("virtualization-test"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ system, role string }{{"kvm", "host"}, {"docker", "guest"}, {"linux-vserver", ""}} {
		now = now.Add(time.Second)
		body := fmt.Sprintf(`{"hostname":"node","virtualization_system":%q,"virtualization_role":%q}`, tc.system, tc.role)
		if w := sendReport(t, h, "Bearer virtualization-test", body); w.Code != http.StatusOK {
			t.Fatalf("report rejected: %d %s", w.Code, w.Body.String())
		}
		rows, err := store.GetAgents()
		if err != nil || len(rows) != 1 || rows[0].VirtualizationSystem != tc.system || rows[0].VirtualizationRole != tc.role {
			t.Fatalf("virtualization system and role diverged: %+v / %v", rows, err)
		}
	}
}

func TestVirtualizationMigrationPreservesSystemMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "needle.db")
	store, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.SaveReport(&ReportWrite{Hostname: "existing", CPUModel: "Intel N150", OSName: "ImmortalWrt 24.10.4", Arch: "x86_64"})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"virtualization_system", "virtualization_role"} {
		if _, err := store.db.Exec("ALTER TABLE agents DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	store, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row, err := store.GetAgent(id)
	if err != nil || row.CPUModel != "Intel N150" || row.OSName != "ImmortalWrt 24.10.4" || row.Arch != "x86_64" || row.VirtualizationSystem != "" || row.VirtualizationRole != "" {
		t.Fatalf("virtualization migration changed existing metadata: %+v / %v", row, err)
	}
}
