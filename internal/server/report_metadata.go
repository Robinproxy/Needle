package server

import (
	"database/sql"
	"time"
)

// A historical report can add history and counter evidence, but cannot roll
// the live configuration or enabled probe snapshot back to an older sample.
func saveReportMetadata(tx *sql.Tx, r *ReportWrite, now time.Time, sampledAt int64) (int64, bool, error) {
	// Lock before reading freshness so concurrent writers cannot both decide
	// to replace the current configuration from a stale SQLite snapshot.
	if _, err := tx.Exec("UPDATE agents SET last_seen=last_seen WHERE hostname=?", r.Hostname); err != nil {
		return 0, false, err
	}
	var id int64
	var expiry, lastSample sql.NullInt64
	var period string
	err := tx.QueryRow(`SELECT id,expires_at,billing_period,COALESCE(report_sample_at,
	 (SELECT MAX(created_at) FROM metrics WHERE agent_id=agents.id)) FROM agents WHERE hostname=?`, r.Hostname).
		Scan(&id, &expiry, &period, &lastSample)
	if err != nil && err != sql.ErrNoRows {
		return 0, false, err
	}
	if err == nil && lastSample.Valid && sampledAt < lastSample.Int64 {
		_, err := tx.Exec("UPDATE agents SET last_seen=? WHERE id=?", now.Unix(), id)
		return id, false, err
	}
	if err == nil {
		var oldExpiry *int64
		if expiry.Valid {
			oldExpiry = &expiry.Int64
		}
		oldDay, oldConfigured := trafficResetDay(period, oldExpiry)
		day, configured := trafficResetDay(r.BillingPeriod, r.ExpiresAt)
		if oldConfigured != configured || oldDay != day {
			// A real reset-day change invalidates the migration override once;
			// changing back later must not revive an obsolete accumulator.
			if _, err := tx.Exec("DELETE FROM traffic_cycle_transition WHERE agent_id=?", id); err != nil {
				return 0, false, err
			}
		}
	}
	id, err = upsertAgent(tx, r.Hostname, r.Region, r.ExpiresAt, r.BillingPeriod, now.Unix())
	if err != nil {
		return 0, false, err
	}
	_, err = tx.Exec(`UPDATE agents SET report_sample_at=?,monthly_traffic_limit=?,
	 cpu_cores=CASE WHEN ?>0 THEN ? ELSE cpu_cores END WHERE id=?`,
		sampledAt, r.MonthlyTrafficLimit, r.CPUCores, r.CPUCores, id)
	return id, true, err
}
