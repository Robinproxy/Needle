package main

import (
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
