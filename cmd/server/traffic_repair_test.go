package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"needle/internal/server"
)

func TestRepairTrafficCommandPreviewApplyAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "needle.db")
	s, err := server.NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertAgent("malibu", "US", nil, "forever")
	if err != nil {
		t.Fatal(err)
	}
	current := time.Now()
	now := time.Date(current.Year(), current.Month(), 15, 12, 0, 0, 0, current.Location())
	for i, m := range []server.MetricRow{{TotalSent: 1000, TotalRecv: 2000, Uptime: 100}, {TotalSent: 1100, TotalRecv: 2200, Uptime: 130}, {TotalSent: 900, TotalRecv: 1900, Uptime: 160}} {
		m.AgentID = id
		if err = s.InsertMetric(&m, now.Add(time.Duration(i-2)*30*time.Second).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	boundary := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	if err = s.EnsureTrafficBaseline(id, boundary); err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE traffic_baselines SET usage_sent=1000,usage_recv=2100,last_sent=900,last_recv=1900,updated_at=?,accounting_version=0 WHERE agent_id=?`, now.Unix(), id)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = runRepairTraffic(path, []string{"malibu"}); err != nil {
		t.Fatal(err)
	}
	check := func(dbPath string, wantSent, wantRecv int64) {
		t.Helper()
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var sent, recv int64
		if err = db.QueryRow("SELECT usage_sent,usage_recv FROM traffic_baselines WHERE agent_id=?", id).Scan(&sent, &recv); err != nil || sent != wantSent || recv != wantRecv {
			t.Fatalf("usage in %s: %d/%d %v", dbPath, sent, recv, err)
		}
	}
	check(path, 1000, 2100)
	if err = runRepairTraffic(path, []string{"--since", now.Add(-2 * time.Minute).Format(time.RFC3339), "--apply", "malibu"}); err != nil {
		t.Fatal(err)
	}
	check(path, 100, 200)
	backups, err := filepath.Glob(path + ".traffic-backup-*.db")
	if err != nil || len(backups) != 1 {
		t.Fatal("backup files", backups, err)
	}
	check(backups[0], 1000, 2100)
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions", info, err)
	}
	if err = runRepairTraffic(path, []string{"all"}); err != nil {
		t.Fatal(err)
	}
	if err = runRepairTraffic(path, []string{"no-such-agent"}); err == nil {
		t.Fatal("expected unknown agent error")
	}
	if err = runRepairTraffic(path, []string{"--unknown", "malibu"}); err == nil {
		t.Fatal("expected invalid flag error")
	}
}
