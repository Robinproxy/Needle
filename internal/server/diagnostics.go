package server

import "os"

var sqliteDriverName = "sqlite3"
var queryDiagnostics = func() map[string]any {
	return map[string]any{"sql_trace": "not compiled; build load tool with -tags sqlite_trace"}
}

// Diagnostics is for isolated local load tooling, not an HTTP management API.
func (s *Store) Diagnostics() map[string]any {
	out := queryDiagnostics()
	stats := s.db.Stats()
	out["db_pool_wait_count"], out["db_pool_wait_ns"] = stats.WaitCount, stats.WaitDuration.Nanoseconds()
	if info, err := os.Stat(s.path); err == nil {
		out["db_bytes"] = info.Size()
	}
	if info, err := os.Stat(s.path + "-wal"); err == nil {
		out["wal_bytes"] = info.Size()
	} else {
		out["wal_bytes"] = int64(0)
	}
	for _, table := range []string{"metrics", "tcpping_results", "report_receipts"} {
		var n int64
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err == nil {
			out[table+"_rows"] = n
		}
	}
	return out
}
