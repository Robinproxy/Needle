package server

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A real v0.7.0 accumulator after the Malibu sequence. The cycle's original
// raw sample has aged out, but its durable baseline and the bad event remain.
func legacyMalibuStore(t *testing.T) (*Store, int64, time.Time) {
	t.Helper()
	at := time.Date(2026, 10, 18, 4, 8, 4, 0, time.UTC)
	s, id := newTrafficTestStore(t, at, 6)
	boundary := monthlyBoundary(6, at)
	samples := []MetricRow{
		{TotalSent: 403080392907, TotalRecv: 480446842075, Uptime: 5900000, CreatedAt: boundary.Unix()},
		{TotalSent: 621593958449, TotalRecv: 711820379053, Uptime: 6965412, CreatedAt: at.Add(-45 * time.Second).Unix()},
		{TotalSent: 617770990288, TotalRecv: 708005318972, Uptime: 6965426, CreatedAt: at.Add(-31 * time.Second).Unix()},
		{TotalSent: 617792170688, TotalRecv: 708026637168, Uptime: 6965457, CreatedAt: at.Unix()},
	}
	for i := range samples {
		samples[i].AgentID = id
		if err := s.InsertMetric(&samples[i], samples[i].CreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnsureTrafficBaseline(id, boundary.Unix()); err != nil {
		t.Fatal(err)
	}
	_, err := s.db.Exec(`UPDATE traffic_baselines SET usage_sent=836305736230,usage_recv=939400174146,last_sent=617792170688,last_recv=708026637168,updated_at=?,accounting_version=0 WHERE agent_id=?`, at.Unix(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM metrics WHERE created_at<?", at.Add(-rawRetention).Unix()); err != nil {
		t.Fatal(err)
	}
	path := s.path
	s.Close()
	s, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.now = func() time.Time { return at }
	return s, id, at
}

func TestLegacyTrafficRepairPreviewApplyAndIdempotence(t *testing.T) {
	s, id, at := legacyMalibuStore(t)
	result, err := s.RepairTrafficUsage(id, at.Add(-time.Hour).Unix(), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != "" || result.CounterDrops != 1 || result.SampleCount != 3 || result.AfterSent != 218513565542 || result.AfterRecv != 231373536978 {
		t.Fatalf("preview: %+v", result)
	}
	assertUsage(t, s, id, 836305736230, 939400174146)
	result, err = s.RepairTrafficUsage(id, at.Add(-time.Hour).Unix(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatalf("apply: %+v", result)
	}
	assertUsage(t, s, id, 218513565542, 231373536978)
	again, err := s.RepairTrafficUsage(id, at.Add(-time.Hour).Unix(), true)
	if err != nil || !again.AlreadyRepaired {
		t.Fatalf("second apply: %+v %v", again, err)
	}
	// Repair the high watermark too; a rebound must not charge the same bytes.
	saveTrafficSample(t, s, at.Add(30*time.Second), 621593958549, 711820379253, 6965487)
	assertUsage(t, s, id, 218513565642, 231373537178)
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM metrics WHERE agent_id=?", id).Scan(&count); err != nil || count != 4 {
		t.Fatalf("history changed: %d %v", count, err)
	}
	if _, err = s.LookupToken("traffic-test-token"); err != nil {
		t.Fatal("repair removed token", err)
	}
}

func TestTrafficRepairPreservesNewConfirmedReboot(t *testing.T) {
	s, id, at := legacyMalibuStore(t)
	// The first report after migration detects a real reboot from old raw uptime.
	saveTrafficSample(t, s, at.Add(30*time.Second), 10, 20, 10)
	assertUsage(t, s, id, 836305736240, 939400174166)
	result, err := s.RepairTrafficUsage(id, at.Add(-time.Hour).Unix(), true)
	if err != nil || !result.Applied {
		t.Fatalf("repair: %+v %v", result, err)
	}
	assertUsage(t, s, id, 218513565552, 231373536998)
	// Old boot's watermark must never be restored over a newer boot.
	saveTrafficSample(t, s, at.Add(60*time.Second), 30, 50, 40)
	assertUsage(t, s, id, 218513565572, 231373537028)
}

func TestTrafficRepairSurvivesServerReopen(t *testing.T) {
	s, id, at := legacyMalibuStore(t)
	result, err := s.RepairTrafficUsage(id, at.Add(-time.Hour).Unix(), true)
	if err != nil || !result.Applied {
		t.Fatal(result, err)
	}
	path := s.path
	s.Close()
	s, err = NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.now = func() time.Time { return at }
	result, err = s.RepairTrafficUsage(id, at.Add(-time.Hour).Unix(), true)
	if err != nil || !result.AlreadyRepaired {
		t.Fatalf("after reopen: %+v %v", result, err)
	}
	assertUsage(t, s, id, 218513565542, 231373536978)
}

func TestTrafficRepairRefusesUnprovenCorrections(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"missing uptime", "UPDATE metrics SET uptime=0"},
		{"ambiguous missing network", "UPDATE metrics SET total_sent=0,total_recv=0 WHERE id=(SELECT MIN(id) FROM metrics)"},
		{"missing tail", "DELETE FROM metrics WHERE id=(SELECT MAX(id) FROM metrics)"},
		{"partial manual correction", "UPDATE traffic_baselines SET usage_sent=usage_sent-1"},
		{"manual correction", "UPDATE traffic_baselines SET usage_sent=214711777781,usage_recv=227579795093"},
		{"unknown prefix counters", "UPDATE traffic_accounting_legacy SET usage_sent=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id, at := legacyMalibuStore(t)
			if _, err := s.db.Exec(tc.query); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetTrafficUsage(id)
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.RepairTrafficUsage(id, at.Add(-time.Hour).Unix(), true)
			if err != nil {
				t.Fatal(err)
			}
			if result.Applied || result.Reason == "" {
				t.Fatalf("unsafe repair: %+v", result)
			}
			assertUsage(t, s, id, before.Sent, before.Recv)
		})
	}
}

func TestTrafficRepairRollsBackIfAuditUpdateFails(t *testing.T) {
	s, id, at := legacyMalibuStore(t)
	_, err := s.db.Exec(`CREATE TRIGGER fail_traffic_repair BEFORE UPDATE ON traffic_accounting_legacy BEGIN SELECT RAISE(ABORT,'forced failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RepairTrafficUsage(id, at.Add(-time.Hour).Unix(), true); err == nil {
		t.Fatal("expected failed repair")
	}
	assertUsage(t, s, id, 836305736230, 939400174146)
}

func TestTrafficBackupIncludesWALAndNeverOverwrites(t *testing.T) {
	s, id, _ := legacyMalibuStore(t)
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := s.BackupTo(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions", info, err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sent int64
	if err = db.QueryRow("SELECT usage_sent FROM traffic_baselines WHERE agent_id=?", id).Scan(&sent); err != nil || sent != 836305736230 {
		t.Fatalf("backup: %d %v", sent, err)
	}
	if err = s.BackupTo(path); err == nil {
		t.Fatal("must not overwrite an existing backup")
	}
}

func TestDeleteAgentRemovesTrafficRepairEvidence(t *testing.T) {
	s, id, _ := legacyMalibuStore(t)
	if err := s.DeleteAgent(id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM traffic_accounting_legacy WHERE agent_id=?", id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy snapshot leaked after deletion: %d %v", count, err)
	}
}

func TestTrafficRepairRequiresTrustedStartTime(t *testing.T) {
	s, id, _ := legacyMalibuStore(t)
	result, err := s.RepairTrafficUsage(id, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReadyToApply || result.Reason == "" || result.AfterSent != 218513565542 {
		t.Fatalf("unbounded preview: %+v", result)
	}
	if _, err = s.RepairTrafficUsage(id, 0, true); err == nil {
		t.Fatal("must not apply without trusted evidence interval")
	}
	assertUsage(t, s, id, 836305736230, 939400174146)
}
