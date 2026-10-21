package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersistentReportReceiptLostAckRestartAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	h := NewHandler(s)
	h.now = s.now
	s.AllowToken("receipt-only")
	body := fmt.Sprintf(`{"hostname":"receipt","report_id":"0123456789abcdef0123456789abcdef:1","created_at":%d,"billing_period":"forever","uptime":200,"network":{"total_sent":100,"total_recv":200},"tcpping":[{"name":"CTv4","target":"127.0.0.1:80","success":true,"latency_ms":1}]}`, now.Unix())
	w := sendReport(t, h, "Bearer receipt-only", body)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"report_id":"0123456789abcdef0123456789abcdef:1"`) {
		t.Fatal(w.Body.String())
	}
	s.Close() // Commit succeeded, pretend the ACK was lost and restart Server.
	s, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.now = func() time.Time { return now.Add(time.Minute) }
	h = NewHandler(s)
	h.now = s.now
	w = sendReport(t, h, "Bearer receipt-only", body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, table := range []string{"metrics", "tcpping_results", "report_receipts"} {
		var n int
		s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		if n != 1 {
			t.Fatalf("%s=%d", table, n)
		}
	}
	now = now.Add(time.Second)
	w = sendReport(t, h, "Bearer receipt-only", strings.Replace(body, `"total_sent":100`, `"total_sent":999`, 1))
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestReceiptAndBusinessWriteRollBackTogether(t *testing.T) {
	s := newTransactionTestStore(t)
	s.AllowToken("rollback")
	// Leave token unbound: binding must roll back with the failed first report.
	s.db.Exec(`CREATE TRIGGER fail_receipt BEFORE INSERT ON report_receipts BEGIN SELECT RAISE(ABORT,'test'); END`)
	r := &ReportWrite{Token: "rollback", Hostname: "receipt", ReportID: "0123456789abcdef0123456789abcdef:1", Digest: "test", CreatedAt: time.Now().Unix(), Metric: &MetricRow{}}
	if _, err := s.SaveReport(r); err == nil {
		t.Fatal("injected receipt failure succeeded")
	}
	for _, table := range []string{"metrics", "agents", "report_receipts"} {
		var n int
		s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		if n != 0 {
			t.Fatalf("half commit %s=%d", table, n)
		}
	}
	token, err := s.LookupToken("rollback")
	if err != nil || token.Hostname != "" {
		t.Fatal("failed report partially bound token", token, err)
	}
	s.db.Exec(`DROP TRIGGER fail_receipt`)
	if _, err := s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
}
func TestUnknownExpiredIdentifiedReportNeverBecomesNow(t *testing.T) {
	h, s := newTestHandler(t)
	s.AllowToken("expired")
	for _, age := range []time.Duration{25 * time.Hour, 40 * 24 * time.Hour} {
		w := sendReport(t, h, "Bearer expired", fmt.Sprintf(`{"hostname":"old","report_id":"0123456789abcdef0123456789abcdef:2","created_at":%d}`, time.Now().Add(-age).Unix()))
		if w.Code != 422 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM metrics").Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
}

func TestDuplicateReportDoesNotRepeatTrafficOrFailedPingDenominator(t *testing.T) {
	h, s, now, id := realtimeFixture(t)
	*now = now.Add(30 * time.Second)
	body := fmt.Sprintf(`{"hostname":"live","report_id":"0123456789abcdef0123456789abcdef:7","created_at":%d,"billing_period":"forever","uptime":500,"network":{"total_sent":160,"total_recv":280},"tcpping":[{"name":"CTv4","target":"127.0.0.1:80","success":false,"latency_ms":0}]}`, now.Unix())
	first := sendReport(t, h, "Bearer live-secret", body)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	before, _ := s.GetTrafficUsage(id)
	// Credential rotation preserves the node identity and receipt namespace.
	if err := s.RevokeToken("live-secret"); err != nil {
		t.Fatal(err)
	}
	s.AllowToken("rotated-secret")
	s.BindToken("rotated-secret", "live")
	second := sendReport(t, h, "Bearer rotated-secret", body)
	if second.Code != 200 || !strings.Contains(second.Body.String(), `"duplicate":true`) {
		t.Fatal(second.Code, second.Body.String())
	}
	after, _ := s.GetTrafficUsage(id)
	if before.Sent != 60 || before.Recv != 80 || *after != *before {
		t.Fatal(before, after)
	}
	var metrics, pings, failed int
	s.db.QueryRow(`SELECT COUNT(*) FROM metrics WHERE agent_id=?`, id).Scan(&metrics)
	s.db.QueryRow(`SELECT COUNT(*),SUM(CASE WHEN success=0 THEN 1 ELSE 0 END) FROM tcpping_results WHERE agent_id=?`, id).Scan(&pings, &failed)
	if metrics != 2 || pings != 2 || failed != 1 {
		t.Fatal(metrics, pings, failed)
	}
}
func TestReceiptCleanupCannotReadmitExpiredSample(t *testing.T) {
	s := newTransactionTestStore(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	r := &ReportWrite{Hostname: "expiry", ReportID: "0123456789abcdef0123456789abcdef:1", Digest: "same", CreatedAt: now.Unix(), Metric: &MetricRow{}}
	if _, err := s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
	now = now.Add(49 * time.Hour)
	s.PurgeOldData()
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM report_receipts`).Scan(&count)
	if count != 0 {
		t.Fatal(count)
	}
	if _, err := s.SaveReport(r); err != ErrReportExpired {
		t.Fatal(err)
	}
}
