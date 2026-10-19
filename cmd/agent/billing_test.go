package main

import (
	"encoding/json"
	"testing"
	"time"

	agentpkg "needle/internal/agent"

	"gopkg.in/yaml.v3"
)

func TestBillingConfigAndWire(t *testing.T) {
	for _, tc := range []struct {
		name, config, amount, currency string
		absent, invalid                bool
	}{
		{name: "legacy omitted", config: "billing_period: 1m\n", absent: true},
		{name: "free", config: "billing_amount: 0\nbilling_period: forever\n", amount: "0", currency: "USD"},
		{name: "unset", config: "billing_amount: ''\nbilling_currency: CAD\n", currency: "CAD"},
		{name: "exact number", config: "billing_amount: 0.123456\nbilling_period: 24m\nbilling_currency: eur\n", amount: "0.123456", currency: "EUR"},
		{name: "three years", config: "billing_amount: '120.00'\nbilling_period: 36m\nbilling_currency: GBP\n", amount: "120", currency: "GBP"},
		{name: "negative", config: "billing_amount: -1\nbilling_period: 1m\n", invalid: true},
		{name: "bad currency", config: "billing_currency: dollars\n", invalid: true},
		{name: "missing period", config: "billing_amount: '5'\n", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(tc.config), &cfg); err != nil {
				t.Fatal(err)
			}
			cost, err := cfg.billingCost()
			if (err != nil) != tc.invalid {
				t.Fatalf("cost=%+v err=%v", cost, err)
			}
			if tc.invalid {
				return
			}
			if err := cfg.validateProbesAndInterval(); err != nil {
				t.Fatal(err)
			}
			if (cost == nil) != tc.absent || cost != nil && (cost.Amount != tc.amount || cost.Currency != tc.currency) {
				t.Fatalf("unexpected cost %+v", cost)
			}
			encoded, err := json.Marshal(agentpkg.ReportData{BillingCost: cost})
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			_, present := wire["billing_cost"]
			if present == tc.absent {
				t.Fatalf("omission contract lost: %s", encoded)
			}
		})
	}
}

func TestTransferredNodeKeepsActualRenewalDate(t *testing.T) {
	for _, period := range []string{"1m", "3m", "6m", "12m", "24m", "36m"} {
		cfg := Config{BillingPeriod: period, ExpiresAt: "2028-02-29"}
		if err := cfg.validateProbesAndInterval(); err != nil {
			t.Fatal(err)
		}
		date, err := cfg.expiryUnix()
		if err != nil || date == nil || time.Unix(*date, 0).UTC().Format("2006-01-02") != cfg.ExpiresAt {
			t.Fatalf("%s changed the entered renewal date: %v, %v", period, date, err)
		}
	}
}
