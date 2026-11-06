package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"needle/internal/logging"
)

type fxTransport func(*http.Request) (*http.Response, error)

func (f fxTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func ecbFixture(date string) string {
	quotes := ""
	for _, code := range []string{"USD", "CNY", "GBP", "CAD", "HKD", "JPY", "AUD"} {
		quotes += fmt.Sprintf(`<Cube currency='%s' rate='2.5000'/>`, code)
	}
	return `<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref"><Cube><Cube time='` + date + `'>` + quotes + `</Cube></Cube></gesmes:Envelope>`
}

func fxClient(t *testing.T, code int, body string) *http.Client {
	t.Helper()
	return &http.Client{Transport: fxTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != ecbURL || r.Method != "GET" || r.Header.Get("Authorization") != "" || r.Body != nil {
			t.Fatal("unsafe exchange request", r)
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
}

func TestECBDocumentValidation(t *testing.T) {
	now := time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC)
	valid := ecbFixture("2026-11-05")
	date, rates, err := parseECBRates([]byte(valid), now)
	if err != nil || date != "2026-11-05" || len(rates) != 8 || rates["EUR"] != "1" {
		t.Fatal(date, rates, err)
	}
	for _, body := range []string{
		`<html>unavailable</html>`, valid + `<extra/>`, valid + `garbage`,
		strings.Replace(valid, "2026-11-05", "2026-11-06", 1),
		strings.Replace(valid, "2026-11-05", "2026-02-30", 1),
		strings.Replace(valid, `currency='USD'`, `currency='EUR'`, 1),
		strings.Replace(valid, `currency='USD'`, `currency='CNY'`, 1),
		strings.Replace(valid, `currency='USD'`, `currency='ABC'`, 1),
		strings.Replace(valid, `rate='2.5000'`, `rate='0.000'`, 1),
		strings.Replace(valid, `rate='2.5000'`, `rate='NaN'`, 1),
		strings.Replace(valid, `rate='2.5000'`, `rate='-1'`, 1),
	} {
		if _, _, err := parseECBRates([]byte(body), now); err == nil {
			t.Fatalf("accepted malformed document: %.100s", body)
		}
	}
}

func TestExchangeCacheRetainsGoodDataAcrossFailureRestartAndAge(t *testing.T) {
	_, s := newTestHandler(t)
	now := time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC)
	if err := s.updateExchangeRates(context.Background(), fxClient(t, 200, ecbFixture("2026-11-04")), now); err != nil {
		t.Fatal(err)
	}
	if err := s.setCostDisplay("USD", nil); err != nil {
		t.Fatal(err)
	}
	before, _ := s.exchangeStatus(now)
	if before.Stale || before.LastSuccess != now.Unix() || before.Currency != "USD" {
		t.Fatal(before)
	}
	for _, test := range []struct {
		code int
		body string
	}{{503, "offline"}, {200, "invalid"}, {200, ecbFixture("2026-11-03")}, {200, strings.Repeat("x", (256<<10)+1)}} {
		if err := s.updateExchangeRates(context.Background(), fxClient(t, test.code, test.body), now.Add(time.Hour)); err == nil {
			t.Fatal("failed response accepted")
		}
		after, _ := s.exchangeStatus(now.Add(time.Hour))
		if after.LastError == "" || after.Date != before.Date || after.LastSuccess != before.LastSuccess || !reflect.DeepEqual(after.Rates, before.Rates) {
			t.Fatal("cache replaced", after)
		}
	}
	if err := s.updateExchangeRates(context.Background(), fxClient(t, 200, ecbFixture("2026-11-05")), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A second handle models a process restart without any outbound requests.
	reopened, err := NewStoreCLI(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, _ := reopened.exchangeStatus(now.Add(8 * 24 * time.Hour))
	if !after.Stale || after.Date != "2026-11-05" || after.LastError != "" || after.Currency != "USD" || len(after.Rates) != 8 {
		t.Fatal(after)
	}
	boundary, _ := s.exchangeStatus(time.Date(2026, 11, 12, 23, 0, 0, 0, time.UTC))
	if boundary.Stale {
		t.Fatal("weekends/7-day boundary treated as expired")
	}
	if err := s.setCostDisplay("XYZ", nil); err == nil {
		t.Fatal("unknown display currency accepted")
	}
	if err := s.migrateExchangeRates(); err != nil {
		t.Fatal(err)
	}
	still, _ := s.exchangeStatus(now)
	if still.Currency != "USD" || still.Date != "2026-11-05" {
		t.Fatal("migration reset settings")
	}
}

func TestExchangeSchedulingAndAuthenticatedSettings(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		status exchangeStatus
		due    bool
	}{
		{exchangeStatus{}, true},
		{exchangeStatus{LastAttempt: now.Add(-23 * time.Hour).Unix()}, false},
		{exchangeStatus{LastAttempt: now.Add(-24 * time.Hour).Unix()}, true},
		{exchangeStatus{LastAttempt: now.Add(-59 * time.Minute).Unix(), LastError: "failed"}, false},
		{exchangeStatus{LastAttempt: now.Add(-time.Hour).Unix(), LastError: "failed"}, true},
	} {
		if exchangeUpdateDue(test.status, now) != test.due {
			t.Fatal(test)
		}
	}
	h, s := newAdminTest(t)
	if w := adminCall(h, "PATCH", "cost-display", `{"currency":"USD"}`, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := adminLogin(t, h, s)
	if w := adminCall(h, "PATCH", "cost-display", `{"currency":"USD"}`, cookie, "wrong"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := adminCall(h, "PATCH", "cost-display", `{"currency":"USD","url":"http://localhost"}`, cookie, csrf); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := adminCall(h, "PATCH", "cost-display", `{"currency":"XYZ"}`, cookie, csrf); w.Code != 422 {
		t.Fatal(w.Code)
	}
	if w := adminCall(h, "PATCH", "cost-display", `{"currency":"USD"}`, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	before, _ := s.exchangeStatus(h.now())
	if w := adminCall(h, "PATCH", "cost-display", `{"hidden":"true"}`, cookie, csrf); w.Code != 400 {
		t.Fatal("invalid visibility accepted", w.Code)
	}
	if w := adminCall(h, "PATCH", "cost-display", `{"hidden":true}`, cookie, "wrong"); w.Code != 403 {
		t.Fatal("visibility changed without CSRF", w.Code)
	}
	if w := adminCall(h, "PATCH", "cost-display", `{"hidden":true}`, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := s.exchangeStatus(h.now())
	if !after.Hidden || before.Currency != after.Currency || !reflect.DeepEqual(before.Rates, after.Rates) || before.Date != after.Date {
		t.Fatal("visibility change erased settings", before, after)
	}
	reopened, err := NewStoreCLI(s.path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := reopened.exchangeStatus(h.now())
	reopened.Close()
	if !persisted.Hidden || persisted.Currency != "USD" {
		t.Fatal("visibility not persisted", persisted)
	}
	w := httptest.NewRecorder()
	h.handleInfo(w, httptest.NewRequest("GET", "/api/info", nil))
	var info struct {
		Display costDisplay `json:"cost_display"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.Display.Currency != "USD" || !info.Display.Hidden {
		t.Fatal(w.Body.String(), err)
	}
	if strings.Contains(w.Body.String(), "last_error") || strings.Contains(w.Body.String(), "last_attempt") {
		t.Fatal("public API exposed diagnostic details")
	}
	if w := adminCall(h, "PATCH", "cost-display", `{"currency":"CNY","hidden":false}`, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	restored, _ := s.exchangeStatus(h.now())
	if restored.Hidden || restored.Currency != "CNY" {
		t.Fatal("could not restore cost display", restored)
	}
	if _, err := s.db.Exec(`DROP TABLE cost_display`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.handleInfo(w, httptest.NewRequest("GET", "/api/info", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version"`) {
		t.Fatal("auxiliary storage failure broke info")
	}
	// CLI construction remains offline and keeps custom database paths.
	other, err := NewStoreCLI(filepath.Join(t.TempDir(), "offline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	status, _ := other.exchangeStatus(now)
	if status.LastAttempt != 0 {
		t.Fatal("CLI unexpectedly fetched rates")
	}
}

func TestCostDisplayVisibilityMigrationFromV0710(t *testing.T) {
	_, s := newTestHandler(t)
	if _, err := s.db.Exec(`ALTER TABLE cost_display DROP COLUMN hidden; UPDATE cost_display SET currency='EUR',rate_date='2026-11-05',rates_json='{"EUR":"1","CNY":"8"}',last_success=123`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateExchangeRates(); err != nil {
		t.Fatal(err)
	}
	status, err := s.exchangeStatus(time.Now())
	if err != nil || status.Hidden || status.Currency != "EUR" || status.Date != "2026-11-05" || status.Rates["CNY"] != "8" || status.LastSuccess != 123 {
		t.Fatal("migration changed existing cost settings", status, err)
	}
	hidden := true
	if err := s.setCostDisplay("", &hidden); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateExchangeRates(); err != nil {
		t.Fatal(err)
	}
	status, _ = s.exchangeStatus(time.Now())
	if !status.Hidden || status.Currency != "EUR" {
		t.Fatal("migration reset saved visibility", status)
	}
}

func exchangeRecoveryCount(lines []string, issue string) int {
	count := 0
	for _, line := range lines {
		if strings.Contains(line, "event=recovered") && strings.Contains(line, fmt.Sprintf("issue=%q", issue)) {
			count++
		}
	}
	return count
}

func TestExchangeCooldownDoesNotLogRecoveryOrLoseCachedRates(t *testing.T) {
	_, s := newTestHandler(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := s.updateExchangeRates(context.Background(), fxClient(t, 200, ecbFixture("2026-11-05")), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	before, err := s.exchangeStatus(now)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	s.logs = logging.NewTracker("server.maintenance", func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	calls := 0
	upstream := fxClient(t, 503, "unavailable").Transport
	client := &http.Client{Transport: fxTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return upstream.RoundTrip(req)
	})}
	s.updateExchangeRatesIfDue(context.Background(), client, now)
	for minute := 1; minute < 60; minute++ {
		s.updateExchangeRatesIfDue(context.Background(), client, now.Add(time.Duration(minute)*time.Minute))
	}
	status, err := s.exchangeStatus(now.Add(59 * time.Minute))
	if err != nil || calls != 1 || len(lines) != 1 || exchangeRecoveryCount(lines, "exchange_rates") != 0 ||
		status.LastError == "" || status.LastSuccess != before.LastSuccess || !reflect.DeepEqual(status.Rates, before.Rates) {
		t.Fatalf("cooldown changed sync state: calls=%d logs=%v status=%+v err=%v", calls, lines, status, err)
	}
	s.updateExchangeRatesIfDue(context.Background(), client, now.Add(time.Hour))
	if calls != 2 || exchangeRecoveryCount(lines, "exchange_rates") != 0 {
		t.Fatal("a failed retry reported recovery", calls, lines)
	}
	upstream = fxClient(t, 200, ecbFixture("2026-11-06")).Transport
	s.updateExchangeRatesIfDue(context.Background(), client, now.Add(2*time.Hour))
	status, err = s.exchangeStatus(now.Add(2 * time.Hour))
	if err != nil || calls != 3 || status.LastError != "" || status.LastSuccess != now.Add(2*time.Hour).Unix() ||
		exchangeRecoveryCount(lines, "exchange_rates") != 1 || !strings.Contains(lines[len(lines)-1], "count=2") {
		t.Fatalf("real success did not recover failed attempts: calls=%d logs=%v status=%+v err=%v", calls, lines, status, err)
	}
	logCount := len(lines)
	s.updateExchangeRatesIfDue(context.Background(), client, now.Add(2*time.Hour+time.Minute))
	if calls != 3 || len(lines) != logCount {
		t.Fatal("successful cooldown emitted more fetches or recovery logs", calls, lines)
	}
}

func TestExchangeCacheRecoveryDoesNotRecoverRemoteSync(t *testing.T) {
	_, s := newTestHandler(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	var lines []string
	s.logs = logging.NewTracker("server.maintenance", func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	client := fxClient(t, 503, "unavailable")
	s.updateExchangeRatesIfDue(context.Background(), client, now)
	if _, err := s.db.Exec("ALTER TABLE cost_display RENAME TO unavailable_cost_display"); err != nil {
		t.Fatal(err)
	}
	s.updateExchangeRatesIfDue(context.Background(), client, now.Add(time.Minute))
	if _, err := s.db.Exec("ALTER TABLE unavailable_cost_display RENAME TO cost_display"); err != nil {
		t.Fatal(err)
	}
	s.updateExchangeRatesIfDue(context.Background(), client, now.Add(2*time.Minute))
	if exchangeRecoveryCount(lines, "exchange_rates") != 0 || exchangeRecoveryCount(lines, "exchange_rates_cache") != 1 {
		t.Fatal("cache recovery cleared the remote failure", lines)
	}
	s.updateExchangeRatesIfDue(context.Background(), fxClient(t, 200, ecbFixture("2026-11-06")), now.Add(time.Hour))
	if exchangeRecoveryCount(lines, "exchange_rates") != 1 {
		t.Fatal("real remote success did not log recovery", lines)
	}
}

func TestExchangeCanceledAttemptDoesNotLogRecovery(t *testing.T) {
	_, s := newTestHandler(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	var lines []string
	s.logs = logging.NewTracker("server.maintenance", func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	s.updateExchangeRatesIfDue(context.Background(), fxClient(t, 503, "unavailable"), now)
	before := len(lines)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := &http.Client{Transport: fxTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, ctx.Err()
	})}
	s.updateExchangeRatesIfDue(ctx, client, now.Add(time.Hour))
	s.updateExchangeRatesIfDue(ctx, client, now.Add(2*time.Hour))
	status, err := s.exchangeStatus(now.Add(2 * time.Hour))
	if err != nil || calls != 1 || len(lines) != before || status.LastError == "" || status.LastSuccess != 0 {
		t.Fatalf("cancellation changed remote success state: calls=%d logs=%v status=%+v err=%v", calls, lines, status, err)
	}
}
