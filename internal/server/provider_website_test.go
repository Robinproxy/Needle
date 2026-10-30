package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProviderWebsiteValidation(t *testing.T) {
	for _, website := range []string{"", "https://example.com", "http://example.com:8080/billing?a=1&b=2#renew", "https://[2001:db8::1]/", "HTTPS://example.com", "https://example.com/" + strings.Repeat("a", 2028)} {
		p := NodeProfileInput{BillingCurrency: "USD", MonthlyTrafficLimitGB: "0", Position: 1, ProviderURL: "  " + website + "  "}
		if _, _, _, err := p.normalize(time.Now()); err != nil || p.ProviderURL != website {
			t.Fatalf("valid website rejected: %q / %v", website, err)
		}
	}
	for _, website := range []string{
		"example.com", "/billing", "//example.com", "javascript:alert(1)", "data:text/html,test", "file:///etc/passwd", "ftp://example.com",
		"https:///billing", "https://", "https://user:password@example.com", "https://example.com@other.example", "https://example.com\\@other.example",
		"https://example.com/forged\nline", "https://example.com/forged\x00line", "https://example.com/a b", "https://example.com/\u2028line",
		"https://example.com:invalid", "https://example.com:0", "https://example.com:65536", "https://[invalid",
		"https://example.com/" + strings.Repeat("a", 2029), "https://example.com/" + string([]byte{0xff}),
	} {
		p := NodeProfileInput{BillingCurrency: "USD", MonthlyTrafficLimitGB: "0", Position: 1, ProviderURL: website}
		if _, _, _, err := p.normalize(time.Now()); err == nil {
			t.Errorf("invalid website accepted: %q", website)
		}
	}
}

func TestProviderWebsiteAdminRoundTripAndPrivacy(t *testing.T) {
	h, s := newAdminTest(t)
	cookie, csrf := adminLogin(t, h, s)
	id, err := s.SaveReport(&ReportWrite{Hostname: "website-node", ServerManaged: true})
	if err != nil {
		t.Fatal(err)
	}
	profiles, _, _ := s.NodeProfiles()
	before := profiles[0]
	p := before.NodeProfileInput
	p.ProviderURL = "  https://example.com/client?plan=private#renew  "
	encoded, _ := json.Marshal(p)
	path := fmt.Sprintf("nodes/%d", id)
	if w := adminCall(h, "PATCH", path, string(encoded), nil, ""); w.Code != 401 {
		t.Fatalf("anonymous website edit accepted: %d", w.Code)
	}
	if w := adminCall(h, "PATCH", path, string(encoded), cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	profiles, _, _ = s.NodeProfiles()
	want := strings.TrimSpace(p.ProviderURL)
	if profiles[0].ProviderURL != want {
		t.Fatal("provider URL did not round-trip", profiles[0])
	}
	before.ProviderURL, before.Version, before.OrderVersion = want, profiles[0].Version, profiles[0].OrderVersion
	if !reflect.DeepEqual(before, profiles[0]) {
		t.Fatalf("website edit changed other settings: before=%+v after=%+v", before, profiles[0])
	}
	if w := adminCall(h, "GET", "nodes", "", cookie, ""); w.Code != 200 || !strings.Contains(w.Body.String(), want) {
		t.Fatal("website missing from authenticated API", w.Code, w.Body.String())
	}
	public := httptest.NewRecorder()
	h.handleAgents(public, httptest.NewRequest("GET", "/api/agents", nil))
	if strings.Contains(public.Body.String(), "provider_url") || strings.Contains(public.Body.String(), "plan=private") {
		t.Fatal("provider website leaked into public dashboard", public.Body.String())
	}
	p = profiles[0].NodeProfileInput
	p.ProviderURL = "javascript:alert(1)"
	encoded, _ = json.Marshal(p)
	if w := adminCall(h, "PATCH", path, string(encoded), cookie, csrf); w.Code != 422 {
		t.Fatal("unsafe URL accepted", w.Code, w.Body.String())
	}
	a, _ := s.GetAgent(id)
	if a.ProviderURL != want || a.ProfileRevision != p.Version {
		t.Fatal("rejected edit changed saved website or revision", a)
	}
	p.ProviderURL = ""
	encoded, _ = json.Marshal(p)
	if w := adminCall(h, "PATCH", path, string(encoded), cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, _ = s.GetAgent(id)
	if a.ProviderURL != "" {
		t.Fatal("cleared URL was retained", a)
	}
}

func TestProviderWebsiteMigrationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "needle.db")
	s, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	id, err := s.SaveReport(&ReportWrite{Hostname: "existing", Region: "US", BillingPeriod: "forever", MonthlyTrafficLimit: 1e12})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetAgent(id)
	if _, err := s.db.Exec("ALTER TABLE agents DROP COLUMN provider_url"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.GetAgent(id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing profile: %+v / %+v / %v", before, after, err)
	}
	profiles, _, _ := s.NodeProfiles()
	p := profiles[0].NodeProfileInput
	p.ProviderURL = "https://example.com/billing"
	if err := s.UpdateNodeProfile(id, p, "preview"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err = s.GetAgent(id)
	if err != nil || after.ProviderURL != p.ProviderURL {
		t.Fatal("website did not survive restart", after, err)
	}
	if err := s.DeleteAgent(id); err != nil {
		t.Fatal(err)
	}
	id, err = s.SaveReport(&ReportWrite{Hostname: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	after, err = s.GetAgent(id)
	if err != nil || after.ProviderURL != "" {
		t.Fatal("new node inherited a deleted provider URL", after, err)
	}
}
