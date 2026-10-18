package server

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Agents encode YAML dates as noon UTC; midnight UTC is also accepted as a
// date-only value. Both represent a calendar date in the Server timezone. Other times retain
// the instant interpretation used by API clients supplying a time of day.
func billingAnchor(unix int64, loc *time.Location) time.Time {
	anchor := time.Unix(unix, 0)
	date := anchor.UTC()
	if (date.Hour() != 0 && date.Hour() != 12) || date.Minute() != 0 || date.Second() != 0 {
		return anchor.In(loc)
	}
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
}

type trafficCycle struct {
	Start int64
	End   int64
}

func activeTrafficCycle(x execer, agentID int64, period string, expiry *int64, now time.Time) (trafficCycle, bool, error) {
	day, ok := trafficResetDay(period, expiry)
	if !ok {
		return trafficCycle{}, false, nil
	}
	var cycle trafficCycle
	var transitionExpiry sql.NullInt64
	var transitionPeriod string
	// A date correction on upgrade must not reset an already running cycle.
	// Renewal dates and payment periods do not alter a monthly reset day.
	err := x.QueryRow(`SELECT boundary,until_at,expires_at,billing_period FROM traffic_cycle_transition
	 WHERE agent_id=? AND ?<until_at AND ?>=boundary`,
		agentID, now.Unix(), now.Unix()).Scan(&cycle.Start, &cycle.End, &transitionExpiry, &transitionPeriod)
	if err != nil && err != sql.ErrNoRows {
		return trafficCycle{}, false, err
	}
	if err == nil {
		var expiry *int64
		if transitionExpiry.Valid {
			expiry = &transitionExpiry.Int64
		}
		if transitionDay, configured := trafficResetDay(transitionPeriod, expiry); configured && transitionDay == day {
			return cycle, true, nil
		}
	}
	start := monthlyBoundary(day, now)
	end := clampedMonthDay(start.Year(), start.Month()+1, day, now.Location())
	return trafficCycle{Start: start.Unix(), End: end.Unix()}, true, nil
}

func (s *Store) migrateAccountingAudit() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, definition := range []struct{ table, col string }{
		{"agents", "report_sample_at INTEGER"},
		{"metrics", "missing_metrics INTEGER"},
		{"metrics_hourly", "cpu_samples INTEGER"},
		{"metrics_hourly", "memory_samples INTEGER"},
		{"metrics_hourly", "disk_samples INTEGER"},
		{"metrics_hourly", "network_samples INTEGER"},
		{"metrics_hourly", "load_samples INTEGER"},
	} {
		if _, err := tx.Exec("ALTER TABLE " + definition.table + " ADD COLUMN " + definition.col); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE agents SET report_sample_at=COALESCE(
	 (SELECT MAX(created_at) FROM metrics WHERE agent_id=agents.id),
	 (SELECT MAX(created_at) FROM tcpping_results WHERE agent_id=agents.id),last_seen)
	 WHERE report_sample_at IS NULL`); err != nil {
		return err
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS history_rollup_state(stream TEXT PRIMARY KEY, last_id INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS accounting_migrations(name TEXT PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS traffic_cycle_transition(agent_id INTEGER PRIMARY KEY, expires_at INTEGER, billing_period TEXT NOT NULL, boundary INTEGER NOT NULL, until_at INTEGER NOT NULL)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	// Legacy hourly aggregates can overlap partially retained raw hours. Save
	// ambiguous old evidence, keep its summary, and never blindly add it twice.
	for _, stream := range historyStreams {
		if err := s.bootstrapHistory(tx, stream); err != nil {
			return err
		}
	}
	var migrated int
	err = tx.QueryRow("SELECT 1 FROM accounting_migrations WHERE name='calendar_dates'").Scan(&migrated)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		rows, err := tx.Query("SELECT id,expires_at,billing_period FROM agents WHERE expires_at IS NOT NULL AND billing_period!='forever'")
		if err != nil {
			return err
		}
		type candidate struct {
			id, expiry int64
			period     string
		}
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.expiry, &c.period); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, c)
		}
		rowErr := rows.Err()
		rows.Close()
		if rowErr != nil {
			return rowErr
		}
		for _, c := range candidates {
			now := s.now()
			old := monthlyBoundary(time.Unix(c.expiry, 0).Day(), now)
			day, _ := trafficResetDay(c.period, &c.expiry)
			current := monthlyBoundary(day, now)
			if current.Equal(old) {
				continue
			}
			// Very old installs may still derive usage from raw rows instead of
			// a persisted baseline. Preserve that same starting point as well.
			oldEnd := clampedMonthDay(old.Year(), old.Month()+1, time.Unix(c.expiry, 0).Day(), now.Location())
			if err := ensureTrafficBaseline(tx, now.Unix(), c.id, trafficCycle{Start: old.Unix(), End: oldEnd.Unix()}); err != nil {
				return err
			}
			next := clampedMonthDay(now.Year(), now.Month(), day, now.Location())
			if !next.After(now) {
				next = clampedMonthDay(now.Year(), now.Month()+1, day, now.Location())
			}
			if _, err := tx.Exec(`INSERT INTO traffic_cycle_transition(agent_id,expires_at,billing_period,boundary,until_at)
			 SELECT ?,?,?,?,? WHERE EXISTS(SELECT 1 FROM traffic_baselines WHERE agent_id=? AND boundary=?)`,
				c.id, c.expiry, c.period, old.Unix(), next.Unix(), c.id, old.Unix()); err != nil {
				return fmt.Errorf("preserve traffic cycle: %w", err)
			}
		}
		if _, err := tx.Exec("INSERT INTO accounting_migrations(name) VALUES('calendar_dates')"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
