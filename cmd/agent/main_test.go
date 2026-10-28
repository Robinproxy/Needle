package main

import (
	"encoding/json"
	"strings"
	"testing"

	agentpkg "needle/internal/agent"

	"gopkg.in/yaml.v3"
)

func TestCurrentAgentConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		wantTLS      bool
	}{
		{"defaults", "server: https://example.com\n", false},
		{"TLS skip verify", "tls_skip_verify: true\n", true},
		{"explicit server management", "server_managed: true\n", false},
		{"merged server management", "defaults: &defaults {server_managed: true, realtime_interval: 5}\n<<: *defaults\n", false},
		{"boolean alias", "enabled: &enabled true\nserver_managed: *enabled\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(tc.config), &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.TLSSkipVerify != tc.wantTLS || !cfg.ServerManaged {
				t.Fatalf("unexpected configuration: %+v", cfg)
			}
		})
	}
}

func TestRemovedAgentSettingsAreRejected(t *testing.T) {
	for _, key := range []string{"region", "billing_currency", "billing_amount", "billing_period", "expires_at", "monthly_traffic_limit_gb", "insecure"} {
		for _, config := range []string{
			key + ": null\n",
			"defaults: &defaults {" + key + ": null}\n<<: *defaults\n",
			"defaults: &defaults {" + key + ": null}\n<<: [*defaults]\n",
		} {
			var cfg Config
			err := yaml.Unmarshal([]byte(config), &cfg)
			want := "/admin"
			if key == "insecure" {
				want = "tls_skip_verify"
			}
			if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), want) {
				t.Fatalf("removed setting must give migration guidance: config=%q err=%v", config, err)
			}
		}
	}
}

func TestConfigurationManagementAndScalarBounds(t *testing.T) {
	for _, config := range []string{
		"server_managed: false\n",
		"server_managed: 'true'\n",
		"server_managed: null\n",
		"realtime_interval: '5'\n",
		"history_queue_capacity: 1.5\n",
		"history_retry_max_age_seconds: null\n",
		"defaults: &defaults {server_managed: false}\n<<: *defaults\n",
		"defaults: &defaults {realtime_interval: '5'}\n<<: *defaults\n",
		"defaults: &defaults {nested: *defaults}\n<<: *defaults\n",
	} {
		var cfg Config
		if err := yaml.Unmarshal([]byte(config), &cfg); err == nil {
			t.Fatalf("invalid configuration accepted: %q", config)
		}
	}
}

func TestReportsDoNotCarryServerManagedStaticSettings(t *testing.T) {
	report := &agentpkg.ReportData{Hostname: "node", ServerManaged: true, Interval: 30, RealtimeInterval: 5}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"region", "billing_currency", "billing_amount", "billing_cost", "billing_period", "expires_at", "monthly_traffic_limit", "monthly_traffic_limit_gb"} {
		if _, exists := wire[key]; exists {
			t.Fatalf("Agent still sends static setting %q: %s", key, encoded)
		}
	}
	if string(wire["server_managed"]) != "true" || string(wire["interval"]) != "30" {
		t.Fatalf("management initialization or runtime intervals lost: %s", encoded)
	}
	encoded, err = json.Marshal(agentpkg.RealtimeFromReport(report))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "server_managed") {
		t.Fatalf("realtime report must not modify profile ownership: %s", encoded)
	}
}
