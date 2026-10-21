//go:build sqlite_trace

package server

import (
	"database/sql"
	sqlite3 "github.com/mattn/go-sqlite3"
	"sync/atomic"
)

func init() {
	var queries, profileNS, busy atomic.Int64
	sqliteDriverName = "needle_traced_sqlite3"
	sql.Register(sqliteDriverName, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.SetTrace(&sqlite3.TraceConfig{EventMask: sqlite3.TraceStmt | sqlite3.TraceProfile, Callback: func(info sqlite3.TraceInfo) int {
			if info.EventCode == sqlite3.TraceStmt {
				queries.Add(1)
			} else {
				profileNS.Add(info.RunTimeNanosec)
				if info.DBError.Code == sqlite3.ErrBusy || info.DBError.Code == sqlite3.ErrLocked {
					busy.Add(1)
				}
			}
			return 0
		}})
	}})
	queryDiagnostics = func() map[string]any {
		return map[string]any{"sql_statements": queries.Load(), "sql_profile_ns": profileNS.Load(), "sqlite_busy_or_locked": busy.Load(), "sqlite_lock_wait_ns": nil}
	}
}
