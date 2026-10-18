package main

import (
	"math"
	"testing"

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
