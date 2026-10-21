package server

import (
	"database/sql"
	"errors"
	"regexp"
	"time"
)

// Identified reports are accepted for 24h; retain receipts for 48h so cleanup
// cannot make an expired unknown report count again. Agent default retry age is
// 30 minutes, configurable up to this window. Legacy reports keep old behavior.
const reportAcceptAge = 24 * time.Hour
const receiptRetention = 48 * time.Hour
const reportProtocol = "persistent-report-v1"

var reportIDPattern = regexp.MustCompile(`^[0-9a-f]{32}:[1-9][0-9]{0,18}$`)
var ErrReportConflict = errors.New("report ID reused with different content")
var ErrReportExpired = errors.New("report sample outside the accepted time window")

func (s *Store) migrateReportReceipts() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS report_receipts (
 agent_id INTEGER NOT NULL, report_id TEXT NOT NULL, digest TEXT NOT NULL,
 sample_at INTEGER NOT NULL, committed_at INTEGER NOT NULL,
 PRIMARY KEY(agent_id, report_id));
 CREATE INDEX IF NOT EXISTS idx_report_receipts_age ON report_receipts(committed_at)`)
	return err
}

// Called before any business write, with authorization rechecked in this tx.
func checkReceipt(tx *sql.Tx, r *ReportWrite, now time.Time) (int64, bool, error) {
	if r.ReportID == "" {
		return 0, false, nil
	}
	var id int64
	var digest string
	err := tx.QueryRow(`SELECT a.id,d.digest FROM agents a JOIN report_receipts d ON d.agent_id=a.id WHERE a.hostname=? AND d.report_id=?`, r.Hostname, r.ReportID).Scan(&id, &digest)
	if err == nil {
		if digest != r.Digest {
			return 0, false, ErrReportConflict
		}
		return id, true, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	if r.CreatedAt < now.Add(-reportAcceptAge).Unix() || r.CreatedAt > now.Add(5*time.Minute).Unix() {
		return 0, false, ErrReportExpired
	}
	return 0, false, nil
}
func commitReport(tx *sql.Tx, r *ReportWrite, id int64, now time.Time) (int64, error) {
	if r.ReportID != "" {
		if _, err := tx.Exec(`INSERT INTO report_receipts(agent_id,report_id,digest,sample_at,committed_at) VALUES(?,?,?,?,?)`, id, r.ReportID, r.Digest, r.CreatedAt, now.Unix()); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}
