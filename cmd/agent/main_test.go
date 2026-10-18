package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	agentpkg "needle/internal/agent"

	"gopkg.in/yaml.v3"
)

func TestEffectiveTLSSkipVerify(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{name: "defaults false", yaml: "server: https://example.com\n", want: false},
		{name: "new field", yaml: "tls_skip_verify: true\n", want: true},
		{name: "legacy true", yaml: "insecure: true\n", want: true},
		{name: "legacy false", yaml: "insecure: false\n", want: false},
		{name: "new field wins over legacy false", yaml: "tls_skip_verify: true\ninsecure: false\n", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(tt.yaml), &cfg); err != nil {
				t.Fatal(err)
			}
			if got := cfg.effectiveTLSSkipVerify(); got != tt.want {
				t.Fatalf("effectiveTLSSkipVerify() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExpiryDateWireContract(t *testing.T) {
	for _, tc := range []struct {
		name, config, wantDate string
		invalid                bool
	}{
		{"quoted date", "expires_at: '2026-11-06'\nbilling_period: 1m\n", "2026-11-06", false},
		{"unquoted date", "expires_at: 2026-11-06\nbilling_period: 3m\n", "2026-11-06", false},
		{"leap date", "expires_at: 2028-02-29\nbilling_period: 12m\n", "2028-02-29", false},
		{"empty", "expires_at: ''\nbilling_period: 1m\n", "", false},
		{"absent", "billing_period: 1m\n", "", false},
		{"invalid", "expires_at: 2026-02-29\nbilling_period: 1m\n", "", true},
		{"forever", "expires_at: 2026-11-06\nbilling_period: forever\n", "", false},
		{"forever ignores invalid", "expires_at: invalid\nbilling_period: forever\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(tc.config), &cfg); err != nil {
				t.Fatal(err)
			}
			expiry, err := cfg.expiryUnix()
			if (err != nil) != tc.invalid {
				t.Fatalf("expiryUnix()=%v,%v invalid=%v", expiry, err, tc.invalid)
			}
			encoded, err := json.Marshal(&agentpkg.ReportData{ExpiresAt: expiry, BillingPeriod: cfg.BillingPeriod})
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Expiry *int64 `json:"expires_at"`
				Period string `json:"billing_period"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Period != cfg.BillingPeriod {
				t.Fatalf("billing period changed: %s", encoded)
			}
			if tc.wantDate == "" {
				if wire.Expiry != nil {
					t.Fatalf("unconfigured date must be omitted: %s", encoded)
				}
				return
			}
			want, err := time.Parse(time.RFC3339, tc.wantDate+"T12:00:00Z")
			if err != nil {
				t.Fatal(err)
			}
			if wire.Expiry == nil || *wire.Expiry != want.Unix() {
				t.Fatalf("date wire timestamp=%s, want UTC noon %d", encoded, want.Unix())
			}
		})
	}
}

func TestMonthlyTrafficLimitBytes(t *testing.T) {
	for _, tc := range []struct {
		gb      float64
		bytes   int64
		invalid bool
	}{
		{0, 0, false}, {1000, 1_000_000_000_000, false}, {0.5, 500_000_000, false},
		{9_000_000, 9_000_000_000_000_000, false}, {-1, 0, true}, {9_000_001, 0, true},
		{math.Inf(1), 0, true}, {math.NaN(), 0, true},
	} {
		got, err := (Config{MonthlyTrafficLimitGB: tc.gb}).monthlyTrafficLimitBytes()
		if (err != nil) != tc.invalid || (!tc.invalid && got != tc.bytes) {
			t.Fatalf("%v GB: got %d, %v", tc.gb, got, err)
		}
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte("monthly_traffic_limit_gb: 1000\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if got, _ := cfg.monthlyTrafficLimitBytes(); got != 1_000_000_000_000 {
		t.Fatalf("YAML quota = %d", got)
	}
}
