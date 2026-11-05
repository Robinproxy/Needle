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
	if err := s.setCostCurrency("USD"); err != nil {
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
	if err := s.setCostCurrency("XYZ"); err == nil {
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
	w := httptest.NewRecorder()
	h.handleInfo(w, httptest.NewRequest("GET", "/api/info", nil))
	var info struct {
		Display costDisplay `json:"cost_display"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.Display.Currency != "USD" {
		t.Fatal(w.Body.String(), err)
	}
	if strings.Contains(w.Body.String(), "last_error") || strings.Contains(w.Body.String(), "last_attempt") {
		t.Fatal("public API exposed diagnostic details")
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
