package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBillingReportsPreserveCostUsageAndIdentity(t *testing.T) {
	h, s := newTestHandler(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	s.now = h.now
	if err := s.AllowToken("billing-test"); err != nil {
		t.Fatal(err)
	}
	expiry := time.Date(2028, 10, 15, 12, 0, 0, 0, time.UTC).Unix()
	send := func(extra string, sent int64) {
		t.Helper()
		body := fmt.Sprintf(`{"hostname":"router","billing_period":"24m","expires_at":%d,"network":{"total_sent":%d,"total_recv":%d},"uptime":1000%s}`, expiry, sent, sent, extra)
		w := sendReport(t, h, "Bearer billing-test", body)
		if w.Code != http.StatusOK {
			t.Fatalf("report: %d %s", w.Code, w.Body.String())
		}
		now = now.Add(time.Minute)
	}
	send(`,"billing_cost":{"currency":"cad","amount":"90.100000"}`, 100)
	send(`,"billing_cost":{"currency":"CAD","amount":"100.25"}`, 300)
	agents, _ := s.GetAgents()
	id := agents[0].ID
	assertUsage(t, s, id, 200, 200)
	// An older Agent omits cost. It must not erase saved billing settings.
	send("", 400)
	agents, _ = s.GetAgents()
	if len(agents) != 1 || agents[0].ID != id || agents[0].BillingCost.Amount != "100.25" || agents[0].BillingCost.Currency != "CAD" || *agents[0].ExpiresAt != expiry || agents[0].MonthlyTrafficLimit != 0 {
		t.Fatalf("billing report changed identity/date/quota: %+v", agents)
	}
	assertUsage(t, s, id, 300, 300)
	// A delayed report cannot replace current cost, just like other metadata.
	old := fmt.Sprintf(`{"hostname":"router","billing_period":"36m","created_at":%d,"billing_cost":{"currency":"USD","amount":"1"}}`, now.Add(-time.Hour).Unix())
	if w := sendReport(t, h, "Bearer billing-test", old); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	agents, _ = s.GetAgents()
	if agents[0].BillingCost.Amount != "100.25" || agents[0].BillingPeriod != "24m" {
		t.Fatal("historical report overwrote current billing")
	}
	// Explicit free and unconfigured are different and persist on reopen.
	for _, amount := range []string{"0", ""} {
		send(fmt.Sprintf(`,"billing_cost":{"currency":"CNY","amount":%q}`, amount), 500)
		other, err := NewStoreCLI(s.path)
		if err != nil {
			t.Fatal(err)
		}
		a, err := other.GetAgents()
		other.Close()
		if err != nil || a[0].BillingCost.Amount != amount {
			t.Fatalf("cost did not persist: %+v, %v", a, err)
		}
		w := httptest.NewRecorder()
		h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
		var api []struct {
			Agent AgentRow `json:"agent"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &api); err != nil || api[0].Agent.BillingCost.Amount != amount {
			t.Fatalf("API cost: %s, %v", w.Body.String(), err)
		}
	}
	// A two-year payment schedule still resets traffic monthly, and the
	// configured next renewal date is stored unchanged across that reset.
	now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	send("", 600)
	assertUsage(t, s, id, 0, 0)
	agents, _ = s.GetAgents()
	if *agents[0].ExpiresAt != expiry {
		t.Fatal("monthly traffic reset changed the configured renewal date")
	}
}

func TestInvalidBillingCostCannotMutateAgent(t *testing.T) {
	for _, cost := range []string{
		`{"currency":"USD","amount":"-1"}`, `{"currency":"$","amount":"5"}`,
		`{"currency":"USD","amount":"1e3"}`, `{"currency":"USD","amount":5}`,
		`{"currency":"USD","amount":"1000000000000"}`, `{"currency":"USD","amount":"1.0000001"}`,
	} {
		t.Run(cost, func(t *testing.T) {
			h, s := newTestHandler(t)
			if err := s.AllowToken("invalid-billing"); err != nil {
				t.Fatal(err)
			}
			w := sendReport(t, h, "Bearer invalid-billing", `{"hostname":"node","billing_period":"36m","billing_cost":`+cost+`}`)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("accepted invalid cost: %d %s", w.Code, w.Body.String())
			}
			a, _ := s.GetAgents()
			token, _ := s.LookupToken("invalid-billing")
			if len(a) != 0 || token.Hostname != "" {
				t.Fatal("invalid cost mutated node or bound token")
			}
		})
	}
}

func TestRenewalAPIProjectsScheduleWithoutChangingStoredAnchor(t *testing.T) {
	h, s := newTestHandler(t)
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, loc)
	h.now = func() time.Time { return now }
	s.now = h.now
	expiry := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC).Unix()
	if _, err := s.UpsertAgent("annual-transfer", "US", &expiry, "12m"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at   time.Time
		days int
		date string
	}{
		{time.Date(2026, 10, 18, 23, 59, 0, 0, loc), 1, "2026-10-19"},
		{time.Date(2026, 10, 19, 0, 0, 0, 0, loc), 0, "2026-10-19"},
		{time.Date(2026, 10, 19, 23, 59, 0, 0, loc), 0, "2026-10-19"},
		{time.Date(2026, 10, 20, 0, 0, 0, 0, loc), 364, "2027-10-19"},
		{time.Date(2026, 10, 22, 12, 0, 0, 0, loc), 362, "2027-10-19"},
	} {
		now = tc.at
		w := httptest.NewRecorder()
		h.handleAgents(w, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
		var rows []struct {
			Days int    `json:"expiry_days"`
			Date string `json:"expiry_date"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].Days != tc.days || rows[0].Date != tc.date {
			t.Fatalf("%s: wrong due state %s, err=%v", now, w.Body.String(), err)
		}
	}
	agents, err := s.GetAgents()
	if err != nil || agents[0].ExpiresAt == nil || *agents[0].ExpiresAt != expiry {
		t.Fatalf("display projection mutated stored expiry: %+v / %v", agents, err)
	}
	renewed := time.Date(2027, 10, 19, 12, 0, 0, 0, time.UTC).Unix()
	if _, err := s.SaveReport(&ReportWrite{Hostname: "annual-transfer", BillingPeriod: "12m", ExpiresAt: &renewed}); err != nil {
		t.Fatal(err)
	}
	agents, err = s.GetAgents()
	if err != nil || agents[0].ExpiresAt == nil || *agents[0].ExpiresAt != renewed {
		t.Fatalf("explicit renewal did not persist: %+v / %v", agents, err)
	}
}
