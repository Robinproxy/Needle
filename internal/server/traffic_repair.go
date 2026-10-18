package server

import (
	"database/sql"
	"fmt"
	"os"
)

type TrafficRepairResult struct {
	ReadyToApply    bool   `json:"ready_to_apply"`
	AgentID         int64  `json:"agent_id"`
	Boundary        int64  `json:"boundary"`
	BeforeSent      int64  `json:"before_sent"`
	BeforeRecv      int64  `json:"before_recv"`
	AfterSent       int64  `json:"after_sent"`
	AfterRecv       int64  `json:"after_recv"`
	CorrectionSent  int64  `json:"correction_sent"`
	CorrectionRecv  int64  `json:"correction_recv"`
	SampleCount     int    `json:"sample_count"`
	CounterDrops    int    `json:"counter_drops"`
	EvidenceStart   int64  `json:"evidence_start"`
	EvidenceEnd     int64  `json:"evidence_end"`
	Applied         bool   `json:"applied"`
	AlreadyRepaired bool   `json:"already_repaired"`
	Reason          string `json:"reason,omitempty"`
}

// BackupTo takes a consistent SQLite backup, including committed WAL content.
// Refuse to overwrite an existing file and keep token data private throughout.
func (s *Store) BackupTo(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err = s.db.Exec("VACUUM INTO ?", path); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

// RepairTrafficUsage corrects only differences supported by retained, original
// legacy reports. The frozen pre-fix accumulator separates old bugs from later
// reports under the new rules. Unknown/pruned history is never reconstructed
// from hourly maxima or by assuming that every counter drop was a reboot.
// The CLI performs a backup before calling this with apply=true.
func (s *Store) RepairTrafficUsage(agentID, since int64, apply bool) (*TrafficRepairResult, error) {
	if apply && since <= 0 {
		return nil, fmt.Errorf("a trusted --since timestamp is required to apply a legacy correction")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := &TrafficRepairResult{AgentID: agentID}
	var expiry sql.NullInt64
	var period string
	if err = tx.QueryRow("SELECT expires_at,billing_period FROM agents WHERE id=?", agentID).Scan(&expiry, &period); err != nil {
		return nil, err
	}
	var expiresAt *int64
	if expiry.Valid {
		expiresAt = &expiry.Int64
	}
	day, configured := trafficResetDay(period, expiresAt)
	if !configured {
		result.Reason = "billing cycle is not configured"
		return result, nil
	}
	result.Boundary = monthlyBoundary(day, s.now()).Unix()
	if apply {
		// Acquire the write lock before reading the live accumulator, so a report
		// arriving concurrently cannot make the preview stale during this repair.
		if _, err = tx.Exec("UPDATE traffic_baselines SET usage_sent=usage_sent WHERE agent_id=? AND boundary=?", agentID, result.Boundary); err != nil {
			return nil, err
		}
	}
	var originalSent, originalRecv, tailSent, tailRecv int64
	var endID, repairedAt sql.NullInt64
	err = tx.QueryRow(`SELECT usage_sent,usage_recv,last_sent,last_recv,end_metric_id,repaired_at FROM traffic_accounting_legacy WHERE agent_id=? AND boundary=?`, agentID, result.Boundary).Scan(&originalSent, &originalRecv, &tailSent, &tailRecv, &endID, &repairedAt)
	if err == sql.ErrNoRows {
		result.Reason = "no legacy accumulator snapshot for this cycle"
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow("SELECT usage_sent,usage_recv FROM traffic_baselines WHERE agent_id=? AND boundary=?", agentID, result.Boundary).Scan(&result.BeforeSent, &result.BeforeRecv)
	if err != nil {
		return nil, err
	}
	result.AfterSent, result.AfterRecv = result.BeforeSent, result.BeforeRecv
	if repairedAt.Valid {
		result.AlreadyRepaired = true
		result.Reason = "legacy correction was already applied"
		return result, nil
	}
	if !endID.Valid {
		result.Reason = "legacy raw samples are unavailable"
		return result, nil
	}
	if result.BeforeSent < originalSent || result.BeforeRecv < originalRecv {
		result.Reason = "current usage was externally reduced after the legacy snapshot; manual review required"
		return result, nil
	}
	rows, err := tx.Query(`SELECT id,total_sent,total_recv,uptime,created_at FROM metrics WHERE agent_id=? AND id<=? AND created_at>=? ORDER BY id`, agentID, endID.Int64, max(result.Boundary, since))
	if err != nil {
		return nil, err
	}
	var samples []MetricRow
	for rows.Next() {
		var m MetricRow
		if err = rows.Scan(&m.ID, &m.TotalSent, &m.TotalRecv, &m.Uptime, &m.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		samples = append(samples, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result.SampleCount = len(samples)
	if len(samples) < 2 {
		result.Reason = "fewer than two retained legacy samples; cannot prove a correction"
		return result, nil
	}
	first, last := samples[0], samples[len(samples)-1]
	result.EvidenceStart, result.EvidenceEnd = first.CreatedAt, last.CreatedAt
	if last.ID != endID.Int64 || last.TotalSent != tailSent || last.TotalRecv != tailRecv {
		result.Reason = "legacy tail does not match retained samples; manual review required"
		return result, nil
	}
	state := trafficCounterState{Sent: first.TotalSent, Recv: first.TotalRecv, Uptime: first.Uptime, SampleAt: first.CreatedAt}
	var oldSent, oldRecv, newSent, newRecv int64
	for i := range samples {
		m := samples[i]
		// A zero/zero row may have been a partial report without network data.
		// Legacy storage cannot distinguish that from an actual all-zero reading.
		if m.Uptime <= 0 || (m.TotalSent == 0 && m.TotalRecv == 0) {
			result.Reason = "missing uptime or ambiguous zero counters in legacy samples; manual review required"
			return result, nil
		}
		if i == 0 {
			continue
		}
		prev := samples[i-1]
		if m.CreatedAt < prev.CreatedAt {
			result.Reason = "out-of-order legacy samples; manual review required"
			return result, nil
		}
		ds, dr := m.TotalSent-prev.TotalSent, m.TotalRecv-prev.TotalRecv
		if ds < 0 {
			ds = m.TotalSent
		}
		if dr < 0 {
			dr = m.TotalRecv
		}
		if oldSent, err = addTrafficUsage(oldSent, ds); err != nil {
			return nil, err
		}
		if oldRecv, err = addTrafficUsage(oldRecv, dr); err != nil {
			return nil, err
		}
		ds, dr = state.consume(&m)
		if newSent, err = addTrafficUsage(newSent, ds); err != nil {
			return nil, err
		}
		if newRecv, err = addTrafficUsage(newRecv, dr); err != nil {
			return nil, err
		}
		if m.Uptime >= prev.Uptime && (m.TotalSent < prev.TotalSent || m.TotalRecv < prev.TotalRecv) {
			result.CounterDrops++
		}
	}
	if result.CounterDrops == 0 {
		result.Reason = "no confirmed non-reboot counter drops in retained legacy samples"
		return result, nil
	}
	// Preserve the unknown prefix before the retained window. Never silently
	// add traffic, go negative, or apply a correction to incompatible totals.
	if oldSent < newSent || oldRecv < newRecv || originalSent < oldSent || originalRecv < oldRecv {
		result.Reason = "legacy totals are inconsistent with replay; manual review required"
		return result, nil
	}
	result.CorrectionSent, result.CorrectionRecv = oldSent-newSent, oldRecv-newRecv
	if result.BeforeSent < result.CorrectionSent || result.BeforeRecv < result.CorrectionRecv {
		result.Reason = "current usage was already adjusted or is inconsistent; manual review required"
		return result, nil
	}
	result.AfterSent = result.BeforeSent - result.CorrectionSent
	result.AfterRecv = result.BeforeRecv - result.CorrectionRecv
	result.ReadyToApply = since > 0
	if !apply {
		if since <= 0 {
			result.Reason = "candidate only: specify --since when the faulty accumulator was known active before applying"
		}
		return result, nil
	}
	_, err = tx.Exec(`UPDATE traffic_baselines SET usage_sent=?,usage_recv=?,
  last_sent=CASE WHEN counter_epoch=0 THEN MAX(last_sent,?) ELSE last_sent END,
  last_recv=CASE WHEN counter_epoch=0 THEN MAX(last_recv,?) ELSE last_recv END,
  last_uptime=COALESCE(last_uptime,?),last_sample_at=COALESCE(last_sample_at,?)
  WHERE agent_id=? AND boundary=?`, result.AfterSent, result.AfterRecv, state.Sent, state.Recv, last.Uptime, last.CreatedAt, agentID, result.Boundary)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`UPDATE traffic_accounting_legacy SET repaired_at=?,correction_sent=?,correction_recv=? WHERE agent_id=? AND boundary=? AND repaired_at IS NULL`, s.now().Unix(), result.CorrectionSent, result.CorrectionRecv, agentID, result.Boundary)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit traffic repair: %w", err)
	}
	result.Applied = true
	return result, nil
}
