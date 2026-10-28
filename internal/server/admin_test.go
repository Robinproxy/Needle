package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"needle/internal/billing"
)

const adminTestPassword = "test-admin-password-2026"

func newAdminTest(t *testing.T) (*Handler, *Store) {
	t.Helper()
	h, s := newTestHandler(t)
	h.now = func() time.Time { return time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC) }
	s.now = h.now
	return h, s
}

func adminCall(h *Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://needle.test/api/admin/"+path, strings.NewReader(body))
	r.Header.Set("Origin", "https://needle.test")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Needle-Admin", "1")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	h.handleAdmin(w, r)
	return w
}

func adminLogin(t *testing.T, h *Handler, s *Store) (*http.Cookie, string) {
	t.Helper()
	if err := s.SetAdmin("robin", adminTestPassword, "https://needle.test", false); err != nil {
		t.Fatal(err)
	}
	w := adminCall(h, "POST", "login", `{"username":"robin","password":"`+adminTestPassword+`"}`, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || len(data.CSRF) != 43 {
		t.Fatal("missing CSRF token", err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	return cookies[0], data.CSRF
}

func TestAdminAuthenticationAndPublicBoundaries(t *testing.T) {
	h, s := newAdminTest(t)
	w := adminCall(h, "GET", "status", "", nil, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"configured":true`) {
		t.Fatal(w.Body.String())
	}
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		for _, path := range []string{"nodes", "nodes/1", "init", "reset"} {
			if w := adminCall(h, method, path, `{}`, nil, ""); w.Code != 401 {
				t.Fatalf("anonymous %s %s: %d", method, path, w.Code)
			}
		}
	}
	cookie, csrf := adminLogin(t, h, s)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/admin/" || cookie.Domain != "" || cookie.MaxAge != 8*3600 {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	if w := adminCall(h, "GET", "nodes", "", cookie, ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	mux := http.NewServeMux()
	h.Register(mux)
	for _, path := range []string{"/", "/api/agents", "/api/info", "/api/health", "/admin"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("public %s: %d", path, w.Code)
		}
		if path == "/admin" && strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-inline") {
			t.Fatal("admin page allows inline scripts")
		}
	}
	r := httptest.NewRequest("PATCH", "/api/admin/nodes/1", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer an-agent-token")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("Agent token accepted as admin")
	}
	r = httptest.NewRequest("POST", "/api/report", strings.NewReader(`{"hostname":"node"}`))
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("admin cookie accepted as Agent credential")
	}
	if w := adminCall(h, "POST", "logout", `{}`, cookie, csrf); w.Code != 200 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout failed", w.Code)
	}
	if w := adminCall(h, "GET", "nodes", "", cookie, ""); w.Code != 401 {
		t.Fatal("logged-out session accepted")
	}
}

func TestAdminCSRFOriginBodyAndRateLimits(t *testing.T) {
	h, s := newAdminTest(t)
	cookie, csrf := adminLogin(t, h, s)
	for _, origin := range []string{"", "null", "https://evil.test", "http://needle.test", "https://needle.test.evil.test", "https://needle.test/path"} {
		r := httptest.NewRequest("POST", "/api/admin/logout", strings.NewReader(`{}`))
		r.AddCookie(cookie)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Needle-Admin", "1")
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		h.handleAdmin(w, r)
		if w.Code != 403 {
			t.Fatalf("origin %q accepted: %d", origin, w.Code)
		}
	}
	if w := adminCall(h, "POST", "logout", `{}`, cookie, "wrong"); w.Code != 403 {
		t.Fatal("missing/wrong CSRF accepted")
	}
	for _, body := range []string{`{"unknown":true}`, `{} {}`, strings.Repeat("x", 5000)} {
		if w := adminCall(h, "PATCH", "nodes/1", body, cookie, csrf); w.Code != 400 {
			t.Fatal("unsafe body accepted", w.Code)
		}
	}
	for i := 0; i < 5; i++ {
		w := adminCall(h, "POST", "login", `{"username":"robin","password":"incorrect"}`, nil, "")
		want := 401
		if i == 4 { // The successful login also consumes one attempt.
			want = 429
		}
		if w.Code != want {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	state := newAdminState()
	for i := 0; i < 1000; i++ {
		state.allowLogin(fmt.Sprintf("192.0.2.%d:123", i), h.now())
	}
	if len(state.attempts) > 128 || state.global.Count > 20 {
		t.Fatal("login limiter is unbounded")
	}
}

func TestAdminPasswordLifecycleAndSessionBounds(t *testing.T) {
	h, s := newAdminTest(t)
	cookie, _ := adminLogin(t, h, s)
	a, _ := s.adminAccount()
	if strings.Contains(a.PasswordHash, adminTestPassword) || !verifyAdminPassword(a.PasswordHash, adminTestPassword) || verifyAdminPassword(a.PasswordHash, "wrong") {
		t.Fatal("password not safely stored/verified")
	}
	if err := s.SetAdmin("other", adminTestPassword, a.Origin, false); err == nil {
		t.Fatal("initialization overwrote existing credentials")
	}
	if err := s.SetAdmin("robin", "short", a.Origin, true); err == nil {
		t.Fatal("weak password accepted")
	}
	for _, origin := range []string{"http://example.com", "https://user:pass@example.com", "https://needle.test/a", "https://needle.test?x=1"} {
		if _, err := AdminOrigin(origin); err == nil {
			t.Fatal("unsafe origin", origin)
		}
	}
	for _, origin := range []string{"http://127.0.0.1:57982", "http://[::1]:8008", "https://needle.test/"} {
		if _, err := AdminOrigin(origin); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAdmin("robin", "replacement-password-2026", a.Origin, true); err != nil {
		t.Fatal(err)
	}
	if w := adminCall(h, "GET", "nodes", "", cookie, ""); w.Code != 401 {
		t.Fatal("password reset did not revoke sessions")
	}
	account, _ := s.adminAccount()
	token, _, _ := h.admin.createSession(account, h.now())
	if _, ok := h.admin.session(token, account, h.now().Add(adminSessionLifetime)); ok {
		t.Fatal("expired session accepted")
	}
	if _, ok := newAdminState().session(token, account, h.now()); ok {
		t.Fatal("sessions survived server restart")
	}
	state := newAdminState()
	for i := 0; i < 32; i++ {
		if _, _, err := state.createSession(account, h.now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := state.createSession(account, h.now()); err == nil || len(state.sessions) != 32 {
		t.Fatal("sessions are not bounded")
	}
	if _, _, err := state.createSession(account, h.now().Add(adminSessionLifetime)); err != nil {
		t.Fatal("expired sessions were not pruned")
	}
	for _, encoded := range []string{"", "$argon2id$v=19$m=999999999,t=2,p=1$a$b", a.PasswordHash + "$extra"} {
		if verifyAdminPassword(encoded, adminTestPassword) {
			t.Fatal("malformed hash accepted")
		}
	}
}

func TestNodeProfileTakeoverKeepsTrafficAndAgentIdentity(t *testing.T) {
	h, s := newAdminTest(t)
	now := h.now()
	h.now = func() time.Time { return now }
	s.now = h.now
	expiry := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC).Unix()
	metric := func(sent, recv int64) *MetricRow {
		return &MetricRow{TotalSent: sent, TotalRecv: recv, Uptime: sent + 1000, CreatedAt: now.Unix()}
	}
	id, err := s.SaveReport(&ReportWrite{Hostname: "machine", Region: "US", BillingPeriod: "1m", ExpiresAt: &expiry, MonthlyTrafficLimit: 1e12, BillingCost: &billing.Cost{Currency: "USD", Amount: "5"}, Metric: metric(100, 200), CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := s.SaveReport(&ReportWrite{Hostname: "machine", Region: "US", BillingPeriod: "1m", ExpiresAt: &expiry, MonthlyTrafficLimit: 1e12, Metric: metric(150, 300), CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetTrafficUsage(id)
	profiles, _, err := s.NodeProfiles()
	if err != nil || len(profiles) != 1 {
		t.Fatal(profiles, err)
	}
	p := profiles[0].NodeProfileInput
	p.DisplayName, p.Region = "My server", "CA"
	p.BillingCurrency, p.BillingAmount, p.BillingPeriod, p.ExpiresAt = "CAD", "39.50", "12m", "2027-12-17"
	p.MonthlyTrafficLimitGB = "2000"
	if err := s.UpdateNodeProfile(id, p, "robin"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetAgent(id)
	if a.Hostname != "machine" || a.DisplayName != "My server" || !a.ProfileManaged || *a.TrafficResetDay != 3 {
		t.Fatalf("identity/reset day changed: %+v", a)
	}
	usage, _ := s.GetTrafficUsage(id)
	if !reflect.DeepEqual(before, usage) || usage.Sent != 50 || usage.Recv != 100 {
		t.Fatalf("editing profile changed usage: before=%+v after=%+v", before, usage)
	}
	now = now.Add(time.Minute)
	if _, err := s.SaveReport(&ReportWrite{Hostname: "machine", Region: "CN", BillingPeriod: "forever", MonthlyTrafficLimit: 0, BillingCost: &billing.Cost{Currency: "CNY", Amount: "0"}, Metric: metric(200, 400), CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.GetAgent(id)
	if updated.Region != "CA" || updated.BillingCost.Amount != "39.5" || updated.BillingPeriod != "12m" || *updated.ExpiresAt != *a.ExpiresAt || updated.MonthlyTrafficLimit != 2e12 || *updated.TrafficResetDay != 3 {
		t.Fatalf("old Agent overwrote settings: %+v", updated)
	}
	usage, _ = s.GetTrafficUsage(id)
	if usage.Sent != 100 || usage.Recv != 200 {
		t.Fatalf("Agent stopped counting with managed metadata: %+v", usage)
	}
	now = time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	if _, err := s.SaveReport(&ReportWrite{Hostname: "machine", BillingPeriod: "forever", Metric: metric(300, 500), CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	usage, _ = s.GetTrafficUsage(id)
	if !usage.Available || !usage.HasData || usage.Sent != 0 || usage.Recv != 0 {
		t.Fatalf("monthly reset did not retain day 3: %+v", usage)
	}
}

func TestNewManagedAgentAndProfileConflictsRollback(t *testing.T) {
	h, s := newAdminTest(t)
	id, err := s.SaveReport(&ReportWrite{Hostname: "new", ServerManaged: true, CreatedAt: h.now().Unix(), Metric: &MetricRow{TotalSent: 10, TotalRecv: 20, CreatedAt: h.now().Unix()}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetAgent(id)
	usage, _ := s.GetTrafficUsage(id)
	if !a.ProfileManaged || *a.TrafficResetDay != 1 || a.BillingCost != nil || a.BillingPeriod != "" || !usage.Available {
		t.Fatalf("new Agent defaults: %+v %+v", a, usage)
	}
	s.SaveReport(&ReportWrite{Hostname: "other", CreatedAt: h.now().Unix()})
	profiles, _, _ := s.NodeProfiles()
	p := profiles[0].NodeProfileInput
	p.DisplayName, p.Position = "alias", 2
	if err := s.UpdateNodeProfile(id, p, "robin"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateNodeProfile(id, p, "robin"); !errors.Is(err, ErrProfileConflict) {
		t.Fatal("stale form accepted", err)
	}
	profiles, _, _ = s.NodeProfiles()
	p = profiles[1].NodeProfileInput
	p.DisplayName, p.Position = "should roll back", 1
	if _, err := s.db.Exec(`CREATE TRIGGER fail_profile_audit BEFORE INSERT ON node_profile_audit BEGIN SELECT RAISE(ABORT,'forced'); END`); err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.NodeProfiles()
	if err := s.UpdateNodeProfile(id, p, "robin"); err == nil {
		t.Fatal("forced failure committed")
	}
	after, _, _ := s.NodeProfiles()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed save changed profiles or order")
	}
}

func TestProfileEditingDoesNotChangeReportDeduplication(t *testing.T) {
	h, s := newAdminTest(t)
	if err := s.AllowToken("wire-test"); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"hostname":"wire","server_managed":true,"report_id":"%s:1","created_at":%d,"network":{"total_sent":100,"total_recv":200},"uptime":100}`, strings.Repeat("a", 32), h.now().Unix())
	if w := sendReport(t, h, "Bearer wire-test", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	profiles, _, _ := s.NodeProfiles()
	p := profiles[0].NodeProfileInput
	p.DisplayName, p.BillingAmount, p.BillingPeriod, p.ExpiresAt = "Wire alias", "12", "1m", "2026-11-13"
	if err := s.UpdateNodeProfile(profiles[0].ID, p, "robin"); err != nil {
		t.Fatal(err)
	}
	if w := sendReport(t, h, "Bearer wire-test", body); w.Code != 200 || !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatal("admin edit changed retry digest", w.Code, w.Body.String())
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM metrics").Scan(&count)
	if count != 1 {
		t.Fatal("retry duplicated history")
	}
}

func TestAdminNodeWriteAuthorizationAndResync(t *testing.T) {
	h, s := newAdminTest(t)
	cookie, csrf := adminLogin(t, h, s)
	id, _ := s.SaveReport(&ReportWrite{Hostname: "one", ServerManaged: true})
	profiles, _, _ := s.NodeProfiles()
	p := profiles[0].NodeProfileInput
	p.DisplayName = "Edited"
	encoded, _ := json.Marshal(p)
	client, ok := s.live.subscribe()
	if !ok {
		t.Fatal("could not subscribe")
	}
	defer s.live.unsubscribe(client)
	if w := adminCall(h, "PATCH", fmt.Sprintf("nodes/%d", id), string(encoded), cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case event := <-client:
		if event.Kind != "resync" {
			t.Fatal("admin edits must refresh all reordered nodes")
		}
	default:
		t.Fatal("admin save did not notify public dashboard")
	}
	if w := adminCall(h, "PATCH", fmt.Sprintf("nodes/%d", id), string(encoded), cookie, csrf); w.Code != 409 {
		t.Fatal("stale HTTP editor accepted", w.Code)
	}
	if w := adminCall(h, "PATCH", fmt.Sprintf("nodes/%d", id), `{"hostname":"identity-change","token":"evil"}`, cookie, csrf); w.Code != 400 {
		t.Fatal("non-profile fields accepted")
	}
}

func TestManagedProfileSurvivesMigrationAndDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "needle.db")
	s, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.SaveReport(&ReportWrite{Hostname: "original", ServerManaged: true})
	profiles, _, _ := s.NodeProfiles()
	p := profiles[0].NodeProfileInput
	p.DisplayName = "Persistent"
	if err := s.UpdateNodeProfile(id, p, "robin"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := s.GetAgent(id)
	if a.DisplayName != "Persistent" || !a.ProfileManaged || *a.TrafficResetDay != 1 {
		t.Fatal("migration erased profile")
	}
	if err := s.DeleteAgent(id); err != nil {
		t.Fatal(err)
	}
	newID, _ := s.SaveReport(&ReportWrite{Hostname: "replacement"})
	a, _ = s.GetAgent(newID)
	var audits int
	s.db.QueryRow("SELECT COUNT(*) FROM node_profile_audit WHERE agent_id=?", newID).Scan(&audits)
	if a.DisplayName != "" || a.ProfileManaged || a.TrafficResetDay != nil || audits != 0 {
		t.Fatal("reused node inherited settings or audit")
	}
}

func TestProfileValidationAndExactAllowances(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	base := NodeProfileInput{BillingCurrency: "USD", MonthlyTrafficLimitGB: "0", Position: 1}
	for _, quota := range []struct {
		text  string
		bytes int64
	}{{"0", 0}, {"0.000000001", 1}, {"1000.001", 1_000_001_000_000}, {"9000000", 9_000_000_000_000_000}} {
		p := base
		p.MonthlyTrafficLimitGB = quota.text
		_, _, got, err := p.normalize(now)
		if err != nil || got != quota.bytes {
			t.Fatal(quota, got, err)
		}
	}
	for _, change := range []func(*NodeProfileInput){
		func(p *NodeProfileInput) { p.Region = "US<script>" },
		func(p *NodeProfileInput) { p.DisplayName = "forged\nname" },
		func(p *NodeProfileInput) { p.BillingAmount = "5" },
		func(p *NodeProfileInput) { p.BillingPeriod = "12m" },
		func(p *NodeProfileInput) { p.BillingPeriod = "forever"; p.ExpiresAt = "2026-11-01" },
		func(p *NodeProfileInput) { p.BillingPeriod = "1m"; p.ExpiresAt = "2026-02-29" },
		func(p *NodeProfileInput) { p.MonthlyTrafficLimitGB = "9000000.000000001" },
		func(p *NodeProfileInput) { p.MonthlyTrafficLimitGB = "1e3" },
		func(p *NodeProfileInput) { p.MonthlyTrafficLimitGB = "-1" },
		func(p *NodeProfileInput) { p.MonthlyTrafficLimitGB = "0.0000000001" },
		func(p *NodeProfileInput) { p.Position = 0 },
	} {
		p := base
		change(&p)
		if _, _, _, err := p.normalize(now); err == nil {
			t.Fatalf("invalid profile accepted: %+v", p)
		}
	}
	for _, amount := range []string{"", "0", "99.123456"} {
		p := base
		p.BillingPeriod = "forever"
		p.BillingAmount = amount
		cost, expiry, _, err := p.normalize(now)
		if err != nil || cost.Amount != amount || expiry != nil {
			t.Fatal("free, unset, and permanent conflated", cost, expiry, err)
		}
	}
}

func TestPinnedMonthEndAndConcurrentProfileEdits(t *testing.T) {
	_, s := newAdminTest(t)
	expiry := time.Date(2027, 1, 31, 12, 0, 0, 0, time.UTC).Unix()
	id, err := s.SaveReport(&ReportWrite{Hostname: "end-of-month", BillingPeriod: "1m", ExpiresAt: &expiry})
	if err != nil {
		t.Fatal(err)
	}
	profiles, _, _ := s.NodeProfiles()
	p := profiles[0].NodeProfileInput
	p.ExpiresAt = "2027-02-28"
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; results <- s.UpdateNodeProfile(id, p, "robin") }()
	}
	close(start)
	first, second := <-results, <-results
	if !((first == nil && errors.Is(second, ErrProfileConflict)) || (second == nil && errors.Is(first, ErrProfileConflict))) {
		t.Fatal(first, second)
	}
	for _, date := range []struct{ at, start, end time.Time }{
		{time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC), time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC), time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)},
		{time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC), time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC), time.Date(2027, 4, 30, 0, 0, 0, 0, time.UTC)},
	} {
		cycle, ok, err := activeTrafficCycle(s.db, id, "forever", nil, date.at)
		if err != nil || !ok || cycle.Start != date.start.Unix() || cycle.End != date.end.Unix() {
			t.Fatal("reset day drifted", cycle, err)
		}
	}
}
