package server

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
)

// Keep the old accumulator and its final metric id once, before accepting any
// reports with the new accounting rules. This is recovery evidence, not a
// recalculation: incomplete historical data must not silently change usage.
func (s *Store) migrateTrafficAccounting() error {
	for _, definition := range []string{
		"accounting_version INTEGER NOT NULL DEFAULT 0",
		"last_uptime INTEGER", "last_sample_at INTEGER",
		"counter_epoch INTEGER NOT NULL DEFAULT 0",
	} {
		if _, err := s.db.Exec("ALTER TABLE traffic_baselines ADD COLUMN " + definition); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS traffic_accounting_legacy (
  agent_id INTEGER NOT NULL, boundary INTEGER NOT NULL,
  usage_sent INTEGER NOT NULL, usage_recv INTEGER NOT NULL,
  last_sent INTEGER NOT NULL, last_recv INTEGER NOT NULL,
  end_metric_id INTEGER, captured_at INTEGER NOT NULL,
  repaired_at INTEGER, correction_sent INTEGER, correction_recv INTEGER,
  PRIMARY KEY(agent_id, boundary)
 )`)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT OR IGNORE INTO traffic_accounting_legacy
  (agent_id,boundary,usage_sent,usage_recv,last_sent,last_recv,end_metric_id,captured_at)
  SELECT b.agent_id,b.boundary,b.usage_sent,b.usage_recv,b.last_sent,b.last_recv,
   (SELECT MAX(m.id) FROM metrics m WHERE m.agent_id=b.agent_id AND m.created_at>=b.boundary AND m.created_at<=b.updated_at), ?
  FROM traffic_baselines b WHERE b.accounting_version=0 AND b.usage_sent IS NOT NULL
   AND b.usage_recv IS NOT NULL AND b.last_sent IS NOT NULL AND b.last_recv IS NOT NULL`, s.now().Unix())
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE traffic_baselines SET accounting_version=1 WHERE accounting_version=0"); err != nil {
		return err
	}
	return tx.Commit()
}

type trafficCounterState struct {
	Sent, Recv       int64 // Per-boot high watermarks, not the last potentially bad reading.
	Uptime, SampleAt int64
	Epoch            int64
}

// A counter decrease alone proves neither a reboot nor how much an interface
// transferred after a reset. Keep the high watermark until it is exceeded, or
// a newer report's uptime confirms a reboot. Returning to a pre-dip value then
// cannot charge the same bytes a second time. Missing uptime is not evidence.
func (s *trafficCounterState) consume(m *MetricRow) (sent, recv int64) {
	if s.SampleAt > 0 && (m.CreatedAt < s.SampleAt ||
		(m.CreatedAt == s.SampleAt && s.Uptime > 0 && m.Uptime > 0 && m.Uptime < s.Uptime)) {
		return 0, 0
	}
	reboot := s.Uptime > 0 && m.Uptime > 0 && m.Uptime < s.Uptime
	if reboot {
		sent, recv = m.TotalSent, m.TotalRecv
		s.Sent, s.Recv = m.TotalSent, m.TotalRecv
		s.Epoch++
	} else {
		sent = max(0, m.TotalSent-s.Sent)
		recv = max(0, m.TotalRecv-s.Recv)
		s.Sent = max(s.Sent, m.TotalSent)
		s.Recv = max(s.Recv, m.TotalRecv)
	}
	s.Uptime, s.SampleAt = m.Uptime, m.CreatedAt
	return sent, recv
}

func addTrafficUsage(usage, delta int64) (int64, error) {
	if usage < 0 || delta < 0 || delta > math.MaxInt64-usage {
		return 0, fmt.Errorf("traffic usage overflow or negative state")
	}
	return usage + delta, nil
}

func accumulateTrafficUsage(x execer, stampedAt, agentID, boundary int64, m *MetricRow) error {
	var baseSent, baseRecv, epoch int64
	var usageSent, usageRecv, lastSent, lastRecv, lastUptime, lastAt sql.NullInt64
	err := x.QueryRow(`SELECT total_sent,total_recv,usage_sent,usage_recv,last_sent,last_recv,last_uptime,last_sample_at,counter_epoch
  FROM traffic_baselines WHERE agent_id=? AND boundary=?`, agentID, boundary).Scan(&baseSent, &baseRecv, &usageSent, &usageRecv, &lastSent, &lastRecv, &lastUptime, &lastAt, &epoch)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var sent, recv int64
	var state trafficCounterState
	if !usageSent.Valid || !usageRecv.Valid || !lastSent.Valid || !lastRecv.Valid {
		// Conservative legacy seed. A counter below the cycle baseline is not a
		// reason to charge its entire lifetime value.
		sent = max(0, m.TotalSent-baseSent)
		recv = max(0, m.TotalRecv-baseRecv)
		state = trafficCounterState{Sent: max(baseSent, m.TotalSent), Recv: max(baseRecv, m.TotalRecv), Uptime: m.Uptime, SampleAt: m.CreatedAt}
	} else {
		if !lastUptime.Valid || !lastAt.Valid {
			// Existing Agents already report uptime; seed it from the preceding raw
			// sample so the first report after a Server upgrade can detect a reboot.
			err = x.QueryRow(`SELECT uptime,created_at FROM metrics WHERE agent_id=? AND id<? AND total_sent=? AND total_recv=? ORDER BY id DESC LIMIT 1`, agentID, m.ID, lastSent.Int64, lastRecv.Int64).Scan(&lastUptime, &lastAt)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
		}
		state = trafficCounterState{Sent: lastSent.Int64, Recv: lastRecv.Int64, Uptime: lastUptime.Int64, SampleAt: lastAt.Int64, Epoch: epoch}
		ds, dr := state.consume(m)
		sent, err = addTrafficUsage(usageSent.Int64, ds)
		if err != nil {
			return err
		}
		recv, err = addTrafficUsage(usageRecv.Int64, dr)
		if err != nil {
			return err
		}
	}
	_, err = x.Exec(`UPDATE traffic_baselines SET usage_sent=?,usage_recv=?,last_sent=?,last_recv=?,last_uptime=?,last_sample_at=?,counter_epoch=?,accounting_version=1,updated_at=?
  WHERE agent_id=? AND boundary=?`, sent, recv, state.Sent, state.Recv, state.Uptime, state.SampleAt, state.Epoch, stampedAt, agentID, boundary)
	return err
}
