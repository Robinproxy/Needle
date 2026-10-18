package server

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

type historyStream struct {
	raw, hourly, keys, columns, values string
}

var historyStreams = []historyStream{
	{"metrics", "metrics_hourly", "agent_id,hour_start",
		`cpu_avg,cpu_peak,memory_total,memory_used,memory_peak_pct,disk_total,disk_used,
		 net_up_avg,net_up_peak,net_down_avg,net_down_peak,total_sent,total_recv,
		 load1,load5,load15,uptime,sample_count,cpu_samples,memory_samples,disk_samples,network_samples,load_samples`,
		aggregateMetricValues},
	{"tcpping_results", "tcpping_hourly", "agent_id,name,hour_start",
		"latency_avg,latency_peak,sample_count,success_count",
		`AVG(CASE WHEN success=1 THEN latency_ms END), MAX(CASE WHEN success=1 THEN latency_ms END), COUNT(*), SUM(success)`},
}

func (h historyStream) rawKeys() string {
	return strings.ReplaceAll(h.keys, "hour_start", "(created_at/3600)*3600")
}

func (h historyStream) matchingHour() string {
	q := "h.agent_id=r.agent_id AND h.hour_start=(r.created_at/3600)*3600 AND r.created_at>=h.hour_start AND r.created_at<h.hour_start+3600"
	if h.raw == "tcpping_results" {
		q += " AND h.name=r.name"
	}
	return q
}

// Only the initial upgrade sees overlapping pre-checkpoint data. Recent whole
// hours can be rebuilt from complete raw data. Older overlaps are ambiguous:
// retain their summary and a copy of both sources for the 90-day history window.
func (s *Store) bootstrapHistory(tx *sql.Tx, h historyStream) error {
	for _, table := range []string{h.raw, h.hourly} {
		if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS " + table + "_rollup_legacy AS SELECT * FROM " + table + " WHERE 0"); err != nil {
			return err
		}
	}
	var last int64
	err := tx.QueryRow("SELECT last_id FROM history_rollup_state WHERE stream=?", h.raw).Scan(&last)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	cutoff := (s.now().Add(-rawRetention).Unix() + 3599) / 3600 * 3600
	for _, q := range []string{
		"INSERT INTO " + h.raw + "_rollup_legacy SELECT r.* FROM " + h.raw + " r WHERE r.created_at<? AND EXISTS(SELECT 1 FROM " + h.hourly + " h WHERE " + h.matchingHour() + ")",
		"INSERT INTO " + h.hourly + "_rollup_legacy SELECT h.* FROM " + h.hourly + " h WHERE h.hour_start<? AND EXISTS(SELECT 1 FROM " + h.raw + " r WHERE " + h.matchingHour() + ")",
		"DELETE FROM " + h.hourly + " AS h WHERE h.hour_start>=? AND EXISTS(SELECT 1 FROM " + h.raw + " r WHERE " + h.matchingHour() + ")",
	} {
		if _, err := tx.Exec(q, cutoff); err != nil {
			return err
		}
	}
	// Missing old archives (for example after long downtime) can be recovered
	// directly. Rows already represented by a legacy archive are not added.
	if _, err := tx.Exec("INSERT INTO " + h.hourly + "(" + h.keys + "," + h.columns + ") SELECT " + h.rawKeys() + "," + h.values +
		" FROM " + h.raw + " r WHERE NOT EXISTS(SELECT 1 FROM " + h.hourly + " h WHERE " + h.matchingHour() + ") GROUP BY " + h.rawKeys()); err != nil {
		return fmt.Errorf("bootstrap %s: %w", h.raw, err)
	}
	_, err = tx.Exec("INSERT INTO history_rollup_state(stream,last_id) SELECT ?,COALESCE(MAX(id),0) FROM "+h.raw, h.raw)
	return err
}

func weightedMeanSQL(table, column, count string) string {
	oldCount := legacySampleCountSQL(table, count, column)
	newCount := "excluded." + count
	return column + "=(COALESCE(" + table + "." + column + ",0)*1.0*" + oldCount +
		"+COALESCE(excluded." + column + ",0)*1.0*" + newCount + ")/NULLIF(" + oldCount + "+" + newCount + ",0)"
}

func legacySampleCountSQL(table, count, column string) string {
	return "COALESCE(" + table + "." + count + ",CASE WHEN " + table + "." + column + " IS NULL THEN 0 ELSE " + table + ".sample_count END)"
}

func historyMergeSQL(h historyStream) string {
	var updates []string
	if h.raw == "metrics" {
		for _, group := range []struct {
			count string
			cols  []string
		}{
			{"cpu_samples", []string{"cpu_avg"}},
			{"memory_samples", []string{"memory_total", "memory_used"}},
			{"disk_samples", []string{"disk_total", "disk_used"}},
			{"network_samples", []string{"net_up_avg", "net_down_avg"}},
			{"load_samples", []string{"load1", "load5", "load15"}},
		} {
			for _, col := range group.cols {
				updates = append(updates, weightedMeanSQL(h.hourly, col, group.count))
			}
			updates = append(updates, group.count+"="+legacySampleCountSQL(h.hourly, group.count, group.cols[0])+"+excluded."+group.count)
		}
		for _, col := range []string{"cpu_peak", "memory_peak_pct", "net_up_peak", "net_down_peak", "total_sent", "total_recv", "uptime"} {
			updates = append(updates, col+"=COALESCE(MAX("+h.hourly+"."+col+",excluded."+col+"),"+h.hourly+"."+col+",excluded."+col+")")
		}
	} else {
		updates = append(updates, weightedMeanSQL(h.hourly, "latency_avg", "success_count"),
			"latency_peak=COALESCE(MAX(tcpping_hourly.latency_peak,excluded.latency_peak),tcpping_hourly.latency_peak,excluded.latency_peak)",
			"success_count=tcpping_hourly.success_count+excluded.success_count")
	}
	updates = append(updates, "sample_count="+h.hourly+".sample_count+excluded.sample_count")
	return "INSERT INTO " + h.hourly + "(" + h.keys + "," + h.columns + ") SELECT " + h.rawKeys() + "," + h.values +
		" FROM " + h.raw + " WHERE id>? AND id<=? GROUP BY " + h.rawKeys() +
		" ON CONFLICT(" + h.keys + ") DO UPDATE SET " + strings.Join(updates, ",")
}

func (s *Store) maintainHistory(purge bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire the SQLite write lock before reading the checkpoints. Reports,
	// deletion and a second purge cannot race a read-then-write transaction.
	if _, err := tx.Exec("UPDATE history_rollup_state SET last_id=last_id"); err != nil {
		return err
	}
	for _, h := range historyStreams {
		var last, end int64
		if err := tx.QueryRow("SELECT last_id FROM history_rollup_state WHERE stream=?", h.raw).Scan(&last); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT MAX(?,COALESCE(MAX(id),0)) FROM "+h.raw, last).Scan(&end); err != nil {
			return err
		}
		if end > last {
			if _, err := tx.Exec(historyMergeSQL(h), last, end); err != nil {
				return fmt.Errorf("rollup %s: %w", h.raw, err)
			}
			if _, err := tx.Exec("UPDATE history_rollup_state SET last_id=? WHERE stream=?", end, h.raw); err != nil {
				return err
			}
		}
		if purge {
			// Keep the entire boundary hour; never leave half an hour available
			// to a raw query while its archive represents the whole hour.
			cutoff := s.now().Add(-rawRetention).Unix() / 3600 * 3600
			if _, err := tx.Exec("DELETE FROM "+h.raw+" WHERE created_at<? AND id<=?", cutoff, end); err != nil {
				return err
			}
			hourlyCutoff := s.now().Add(-hourlyRetention).Unix() / 3600 * 3600
			for _, table := range []string{h.hourly, h.raw + "_rollup_legacy", h.hourly + "_rollup_legacy"} {
				col := "hour_start"
				if table == h.raw+"_rollup_legacy" {
					col = "created_at"
				}
				if _, err := tx.Exec("DELETE FROM "+table+" WHERE "+col+"<?", hourlyCutoff); err != nil {
					return err
				}
			}
		}
	}
	if purge {
		cutoff := s.now().Add(-62 * 24 * time.Hour).Unix()
		for _, table := range []string{"traffic_accounting_legacy", "traffic_baselines"} {
			if _, err := tx.Exec("DELETE FROM "+table+` WHERE boundary<? AND NOT EXISTS (
			 SELECT 1 FROM traffic_cycle_transition t WHERE t.agent_id=`+table+`.agent_id AND t.boundary=`+table+`.boundary AND t.until_at>?)`, cutoff, s.now().Unix()); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM traffic_cycle_transition WHERE until_at<=?", s.now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PurgeOldData() {
	// Rollups, ingestion checkpoints and deletions commit together. A failed
	// aggregation or deletion preserves both the raw data and the old checkpoint.
	if err := s.maintainHistory(true); err != nil {
		log.Printf("history maintenance: %v", err)
		return
	}
	if _, err := s.db.Exec("PRAGMA incremental_vacuum"); err != nil {
		log.Printf("incremental vacuum: %v", err)
	}
}
