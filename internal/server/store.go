package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var (
	ErrTokenNotFound     = errors.New("token not found")
	ErrTokenAlreadyBound = errors.New("token already bound to another hostname")
	ErrHostnameTaken     = errors.New("hostname already bound to another token")
)

const (
	rawRetention    = 7 * 24 * time.Hour  // 30s-granularity metrics/tcpping rows
	hourlyRetention = 90 * 24 * time.Hour // hourly rollups backing the 30d view
)

type Store struct {
	db   *sql.DB
	path string
	now  func() time.Time
}

// execer is the shared subset of *sql.DB and *sql.Tx, so single-statement
// writes can run standalone or inside a report transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func NewStore(path string) (*Store, error) {
	return newStore(path, true)
}

// NewStoreCLI opens the DB for one-shot CLI ops without starting the purge loop.
func NewStoreCLI(path string) (*Store, error) {
	return newStore(path, false)
}

func newStore(path string, purge bool) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	// Restrict DB file perms (tokens live here). Best-effort for existing files.
	_ = os.Chmod(path, 0600)
	s := &Store{db: db, path: path, now: time.Now}
	if err := s.enableIncrementalVacuum(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if purge {
		s.startPurgeLoop()
	}
	return s, nil
}

// enableIncrementalVacuum switches the DB to incremental auto-vacuum so
// pages freed by the purge loop can be returned to the OS instead of the
// file staying at its historical peak size. The mode change only takes
// effect after a full VACUUM, so one runs once on upgrade.
func (s *Store) enableIncrementalVacuum() error {
	var mode int
	if err := s.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if mode == 2 { // already incremental
		return nil
	}
	if _, err := s.db.Exec("PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
		return err
	}
	_, err := s.db.Exec("VACUUM")
	return err
}

func (s *Store) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			id INTEGER PRIMARY KEY,
			hostname TEXT UNIQUE NOT NULL,
			region TEXT DEFAULT '',
			expires_at INTEGER,
			billing_period TEXT DEFAULT '',
			last_seen INTEGER,
			created_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id INTEGER NOT NULL,
			cpu_usage REAL,
			memory_total INTEGER,
			memory_used INTEGER,
			disk_total INTEGER,
			disk_used INTEGER,
			network_up REAL,
			network_down REAL,
			load1 REAL,
			load5 REAL,
			load15 REAL,
			uptime INTEGER,
			created_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS tcpping_results (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			target TEXT NOT NULL,
			latency_ms REAL,
			success INTEGER DEFAULT 1,
			created_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS agent_tcpping_current (
			agent_id INTEGER PRIMARY KEY,
			results_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metrics_agent_time ON metrics(agent_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_tcpping_agent_time ON tcpping_results(agent_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS agent_tokens (
			id INTEGER PRIMARY KEY,
			token TEXT UNIQUE NOT NULL,
			hostname TEXT,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			bound_at INTEGER
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_tokens_hostname
			ON agent_tokens(hostname) WHERE hostname IS NOT NULL AND hostname != ''`,
		`CREATE TABLE IF NOT EXISTS traffic_baselines (
			agent_id INTEGER NOT NULL,
			boundary INTEGER NOT NULL,
			total_sent INTEGER NOT NULL,
			total_recv INTEGER NOT NULL,
			usage_sent INTEGER,
			usage_recv INTEGER,
			last_sent INTEGER,
			last_recv INTEGER,
			updated_at INTEGER,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (agent_id, boundary)
		)`,
		`CREATE TABLE IF NOT EXISTS metrics_hourly (
			agent_id INTEGER NOT NULL,
			hour_start INTEGER NOT NULL,
			cpu_avg REAL, cpu_peak REAL,
			memory_total INTEGER, memory_used INTEGER, memory_peak_pct REAL,
			disk_total INTEGER, disk_used INTEGER,
			net_up_avg REAL, net_up_peak REAL,
			net_down_avg REAL, net_down_peak REAL,
			total_sent INTEGER, total_recv INTEGER,
			load1 REAL, load5 REAL, load15 REAL,
			uptime INTEGER,
			sample_count INTEGER NOT NULL,
			PRIMARY KEY (agent_id, hour_start)
		)`,
		`CREATE TABLE IF NOT EXISTS tcpping_hourly (
			agent_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			hour_start INTEGER NOT NULL,
			latency_avg REAL, latency_peak REAL,
			sample_count INTEGER NOT NULL,
			success_count INTEGER NOT NULL,
			PRIMARY KEY (agent_id, name, hour_start)
		)`,
	}
	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// migrate cumulative traffic columns (added in v0.3)
	s.db.Exec("ALTER TABLE metrics ADD COLUMN total_sent INTEGER DEFAULT 0")
	s.db.Exec("ALTER TABLE metrics ADD COLUMN total_recv INTEGER DEFAULT 0")
	// migrate expires_at and billing_period (added in v0.3.5)
	s.db.Exec("ALTER TABLE agents ADD COLUMN expires_at INTEGER")
	s.db.Exec("ALTER TABLE agents ADD COLUMN billing_period TEXT DEFAULT ''")
	// Persist traffic deltas across kernel counter resets (for example a
	// reboot). Nullable columns let upgraded databases lazily seed the current
	// cycle from their existing baseline without changing the displayed usage.
	s.db.Exec("ALTER TABLE traffic_baselines ADD COLUMN usage_sent INTEGER")
	s.db.Exec("ALTER TABLE traffic_baselines ADD COLUMN usage_recv INTEGER")
	s.db.Exec("ALTER TABLE traffic_baselines ADD COLUMN last_sent INTEGER")
	s.db.Exec("ALTER TABLE traffic_baselines ADD COLUMN last_recv INTEGER")
	s.db.Exec("ALTER TABLE traffic_baselines ADD COLUMN updated_at INTEGER")
	if err := s.migrateDropAutoIncrement(); err != nil {
		return err
	}
	// drop redundant plaintext token copy (v0.6.2); auth uses agent_tokens only
	s.db.Exec("ALTER TABLE agents DROP COLUMN token")
	// last_seen (v0.6.3): report receipt time, survives metric purge.
	// After the autoincrement rebuild so the rebuild cannot drop it.
	s.db.Exec("ALTER TABLE agents ADD COLUMN last_seen INTEGER")
	s.db.Exec("ALTER TABLE agents ADD COLUMN monthly_traffic_limit INTEGER NOT NULL DEFAULT 0")
	s.db.Exec("ALTER TABLE agents ADD COLUMN cpu_cores INTEGER NOT NULL DEFAULT 0")
	if err := s.migrateTrafficAccounting(); err != nil {
		return err
	}
	return s.migrateAccountingAudit()
}

// migrateDropAutoIncrement rebuilds tables that historically used AUTOINCREMENT.
// With AUTOINCREMENT, INSERT ... ON CONFLICT DO UPDATE/NOTHING advances
// sqlite_sequence even on the UPDATE/IGNORE branch, so agents burned one id
// per report (upsert). Rebuild as plain INTEGER PRIMARY KEY (max(id)+1),
// which does not preallocate rowids. Idempotent.
func (s *Store) migrateDropAutoIncrement() error {
	for _, t := range []string{"agents", "agent_tokens"} {
		if err := s.rebuildWithoutAutoInc(t); err != nil {
			return fmt.Errorf("migrate %s: %w", t, err)
		}
	}
	return nil
}

func (s *Store) rebuildWithoutAutoInc(table string) error {
	var sqlText string
	err := s.db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table,
	).Scan(&sqlText)
	if err == sql.ErrNoRows {
		return nil // table not created yet; CREATE handles it
	}
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToUpper(sqlText), "AUTOINCREMENT") {
		return nil // already migrated
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	defs := map[string]string{
		"agents": `CREATE TABLE agents_new (
			id INTEGER PRIMARY KEY,
			hostname TEXT UNIQUE NOT NULL,
			token TEXT NOT NULL,
			region TEXT DEFAULT '',
			expires_at INTEGER,
			billing_period TEXT DEFAULT '',
			created_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		"agent_tokens": `CREATE TABLE agent_tokens_new (
			id INTEGER PRIMARY KEY,
			token TEXT UNIQUE NOT NULL,
			hostname TEXT,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			bound_at INTEGER
		)`,
	}
	cols := map[string]string{
		"agents":       "id, hostname, token, region, expires_at, billing_period, created_at",
		"agent_tokens": "id, token, hostname, created_at, bound_at",
	}

	tmp := table + "_new"
	if _, err := tx.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tmp)); err != nil {
		return err
	}
	if _, err := tx.Exec(defs[table]); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(
		`INSERT INTO %s(%s) SELECT %s FROM %s`, tmp, cols[table], cols[table], table,
	)); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`DROP TABLE %s`, table)); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, tmp, table)); err != nil {
		return err
	}
	// Rebuild standalone indexes dropped with the table.
	// agents: hostname UNIQUE is inline, nothing to rebuild.
	if table == "agent_tokens" {
		if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_tokens_hostname
			ON agent_tokens(hostname) WHERE hostname IS NOT NULL AND hostname != ''`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AllowToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token is required")
	}
	_, err := s.db.Exec(
		`INSERT INTO agent_tokens(token, hostname, created_at) VALUES(?, NULL, ?)
		 ON CONFLICT(token) DO NOTHING`,
		token, time.Now().Unix(),
	)
	return err
}

func (s *Store) LookupToken(token string) (*TokenRow, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrTokenNotFound
	}
	row := s.db.QueryRow(
		`SELECT id, token, hostname, created_at, bound_at FROM agent_tokens WHERE token = ?`,
		token,
	)
	var t TokenRow
	var hostname sql.NullString
	var boundAt sql.NullInt64
	err := row.Scan(&t.ID, &t.Token, &hostname, &t.CreatedAt, &boundAt)
	if err == sql.ErrNoRows {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, err
	}
	if hostname.Valid {
		t.Hostname = hostname.String
	}
	if boundAt.Valid {
		v := boundAt.Int64
		t.BoundAt = &v
	}
	return &t, nil
}

// BindToken binds an unbound token to hostname on first report.
func (s *Store) BindToken(token, hostname string) error {
	token = strings.TrimSpace(token)
	hostname = strings.TrimSpace(hostname)
	if token == "" || hostname == "" {
		return fmt.Errorf("token and hostname are required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var curHost sql.NullString
	err = tx.QueryRow(`SELECT hostname FROM agent_tokens WHERE token = ?`, token).Scan(&curHost)
	if err == sql.ErrNoRows {
		return ErrTokenNotFound
	}
	if err != nil {
		return err
	}
	if curHost.Valid && curHost.String != "" {
		if curHost.String != hostname {
			return ErrTokenAlreadyBound
		}
		return tx.Commit()
	}

	var other string
	err = tx.QueryRow(
		`SELECT token FROM agent_tokens WHERE hostname = ? AND token != ?`,
		hostname, token,
	).Scan(&other)
	if err == nil {
		return ErrHostnameTaken
	}
	if err != sql.ErrNoRows {
		return err
	}

	now := time.Now().Unix()
	res, err := tx.Exec(
		`UPDATE agent_tokens SET hostname = ?, bound_at = ? WHERE token = ? AND (hostname IS NULL OR hostname = '')`,
		hostname, now, token,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTokenAlreadyBound
	}
	return tx.Commit()
}

func (s *Store) RevokeToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var hostname sql.NullString
	err = tx.QueryRow(`SELECT hostname FROM agent_tokens WHERE token = ?`, token).Scan(&hostname)
	if err == sql.ErrNoRows {
		return ErrTokenNotFound
	}
	if err != nil {
		return err
	}

	if hostname.Valid && hostname.String != "" {
		var agentID int64
		err = tx.QueryRow(`SELECT id FROM agents WHERE hostname = ?`, hostname.String).Scan(&agentID)
		if err == nil {
			if err := deleteAgentTx(tx, agentID); err != nil {
				return err
			}
		} else if err != sql.ErrNoRows {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM agent_tokens WHERE token = ?`, token); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListTokens() ([]TokenRow, error) {
	rows, err := s.db.Query(
		`SELECT id, token, hostname, created_at, bound_at FROM agent_tokens ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TokenRow
	for rows.Next() {
		var t TokenRow
		var hostname sql.NullString
		var boundAt sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Token, &hostname, &t.CreatedAt, &boundAt); err != nil {
			return nil, err
		}
		if hostname.Valid {
			t.Hostname = hostname.String
		}
		if boundAt.Valid {
			v := boundAt.Int64
			t.BoundAt = &v
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpsertAgent(hostname, region string, expiresAt *int64, billingPeriod string) (int64, error) {
	return upsertAgent(s.db, hostname, region, expiresAt, billingPeriod, s.now().Unix())
}

func upsertAgent(x execer, hostname, region string, expiresAt *int64, billingPeriod string, lastSeen int64) (int64, error) {
	_, err := x.Exec(
		`INSERT INTO agents(hostname, region, expires_at, billing_period, last_seen) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(hostname) DO UPDATE SET
		   region = excluded.region,
		   expires_at = excluded.expires_at,
		   billing_period = excluded.billing_period,
		   last_seen = excluded.last_seen`,
		hostname, region, expiresAt, billingPeriod, lastSeen,
	)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := x.QueryRow("SELECT id FROM agents WHERE hostname = ?", hostname).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) InsertMetric(m *MetricRow, createdAt int64) error {
	return insertMetric(s.db, m, createdAt)
}

func insertMetric(x execer, m *MetricRow, createdAt int64) error {
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	result, err := x.Exec(
		`INSERT INTO metrics(agent_id, cpu_usage, memory_total, memory_used,
			disk_total, disk_used, network_up, network_down,
			total_sent, total_recv, load1, load5, load15, uptime, created_at, missing_metrics)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.AgentID, metricValue(m, missingCPU, m.CPUUsage), metricValue(m, missingMemory, m.MemoryTotal), metricValue(m, missingMemory, m.MemoryUsed),
		metricValue(m, missingDisk, m.DiskTotal), metricValue(m, missingDisk, m.DiskUsed), metricValue(m, missingNetwork, m.NetworkUp), metricValue(m, missingNetwork, m.NetworkDown),
		metricValue(m, missingNetwork, m.TotalSent), metricValue(m, missingNetwork, m.TotalRecv),
		metricValue(m, missingLoad, m.Load1), metricValue(m, missingLoad, m.Load5), metricValue(m, missingLoad, m.Load15), m.Uptime, createdAt, m.MissingMetrics,
	)
	if err != nil {
		return err
	}
	m.ID, err = result.LastInsertId()
	m.CreatedAt = createdAt
	return err
}

func (s *Store) InsertTCPing(t *TCPingRow, createdAt int64) error {
	return insertTCPing(s.db, t, createdAt)
}

func insertTCPing(x execer, t *TCPingRow, createdAt int64) error {
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	suc := 0
	if t.Success {
		suc = 1
	}
	result, err := x.Exec(
		`INSERT INTO tcpping_results(agent_id, name, target, latency_ms, success, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		t.AgentID, t.Name, t.Target, t.LatencyMs, suc, createdAt,
	)
	if err != nil {
		return err
	}
	t.ID, err = result.LastInsertId()
	t.CreatedAt = createdAt
	return err
}

// ReportWrite is one agent report; SaveReport persists it atomically.
type ReportWrite struct {
	CPUCores            int
	MonthlyTrafficLimit int64
	Hostname            string
	Region              string
	ExpiresAt           *int64
	BillingPeriod       string
	Metric              *MetricRow  // nil when the report carries no metric
	TCPings             []TCPingRow // AgentID is filled in by SaveReport
	CreatedAt           int64
}

// SaveReport writes the agent upsert, metric, traffic baseline, and TCPing
// rows in one transaction: a report either lands completely or not at all,
// and the WAL commits once instead of once per row.
func (s *Store) SaveReport(r *ReportWrite) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := s.now()
	createdAt := r.CreatedAt
	if createdAt <= 0 || createdAt > now.Unix() {
		createdAt = now.Unix()
	}
	agentID, metadataCurrent, err := saveReportMetadata(tx, r, now, createdAt)
	if err != nil {
		return 0, err
	}
	if r.Metric != nil {
		r.Metric.AgentID = agentID
		if err := insertMetric(tx, r.Metric, createdAt); err != nil {
			return 0, err
		}
	}
	// Use sample time for delayed reports, capped at receipt time so a slightly
	// fast Agent clock cannot start the next billing cycle early.
	if r.Metric != nil && r.Metric.MissingMetrics&missingNetwork == 0 {
		at := time.Unix(createdAt, 0).In(now.Location())
		cycle, configured, err := activeTrafficCycle(tx, agentID, r.BillingPeriod, r.ExpiresAt, at)
		if err != nil {
			return 0, err
		}
		if configured {
			// After insertion so a new baseline can see this report.
			if err := ensureTrafficBaseline(tx, now.Unix(), agentID, cycle); err != nil {
				return 0, err
			}
			if err := accumulateTrafficUsage(tx, now.Unix(), agentID, cycle, r.Metric); err != nil {
				return 0, err
			}
		}
	}
	for i := range r.TCPings {
		r.TCPings[i].AgentID = agentID
		if err := insertTCPing(tx, &r.TCPings[i], createdAt); err != nil {
			return 0, err
		}
	}
	if !metadataCurrent {
		return agentID, tx.Commit()
	}
	// Keep the current report independent of history timestamps. Empty results
	// explicitly disable probing, even when two reports arrive in one second.
	current := r.TCPings
	if current == nil {
		current = []TCPingRow{}
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO agent_tcpping_current(agent_id, results_json) VALUES(?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET results_json = excluded.results_json`, agentID, string(encoded)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return agentID, nil
}

func (s *Store) GetAgents() ([]AgentRow, error) {
	rows, err := s.db.Query("SELECT id, hostname, created_at, region, expires_at, billing_period, last_seen, monthly_traffic_limit, cpu_cores FROM agents ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []AgentRow
	for rows.Next() {
		var a AgentRow
		if err := rows.Scan(&a.ID, &a.Hostname, &a.CreatedAt, &a.Region, &a.ExpiresAt, &a.BillingPeriod, &a.LastSeen, &a.MonthlyTrafficLimit, &a.CPUCores); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// AgentIDByHostname reports the agent's id, or ok=false when no such agent.
func (s *Store) AgentIDByHostname(hostname string) (id int64, ok bool, err error) {
	err = s.db.QueryRow(`SELECT id FROM agents WHERE hostname = ?`, hostname).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (s *Store) GetLatestMetric(agentID int64) (*MetricRow, error) {
	row := s.db.QueryRow(
		`SELECT id, agent_id, `+rawMetricValues+`, `+metricMaskSQL(false)+`
		 FROM metrics WHERE agent_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		agentID,
	)
	var m MetricRow
	err := row.Scan(&m.ID, &m.AgentID, &m.CPUUsage, &m.MemoryTotal, &m.MemoryUsed,
		&m.DiskTotal, &m.DiskUsed, &m.NetworkUp, &m.NetworkDown,
		&m.TotalSent, &m.TotalRecv, &m.Load1, &m.Load5, &m.Load15, &m.Uptime, &m.CreatedAt, &m.MissingMetrics)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) GetMetricsWindowSampled(agentID int64, since, until, bucketSeconds int64) ([]MetricRow, error) {
	if bucketSeconds > 0 {
		return s.getMetricsAggregated(agentID, since, until, bucketSeconds)
	}
	rows, err := s.db.Query(
		`SELECT id, agent_id, `+rawMetricValues+`, `+metricMaskSQL(false)+`
		 FROM metrics WHERE agent_id = ? AND created_at >= ? AND (? = 0 OR created_at < ?) ORDER BY created_at`,
		agentID, since, until, until,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []MetricRow
	for rows.Next() {
		var m MetricRow
		if err := rows.Scan(&m.ID, &m.AgentID, &m.CPUUsage, &m.MemoryTotal, &m.MemoryUsed,
			&m.DiskTotal, &m.DiskUsed, &m.NetworkUp, &m.NetworkDown,
			&m.TotalSent, &m.TotalRecv, &m.Load1, &m.Load5, &m.Load15, &m.Uptime, &m.CreatedAt, &m.MissingMetrics); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, rows.Err()
}

func (s *Store) getMetricsAggregated(agentID int64, since, until, bucketSeconds int64) ([]MetricRow, error) {
	rows, err := s.db.Query(
		`SELECT MAX(id), agent_id, COALESCE(AVG(cpu_usage),0),
			CAST(ROUND(COALESCE(AVG(memory_total),0)) AS INTEGER), CAST(ROUND(COALESCE(AVG(memory_used),0)) AS INTEGER),
			CAST(ROUND(COALESCE(AVG(disk_total),0)) AS INTEGER), CAST(ROUND(COALESCE(AVG(disk_used),0)) AS INTEGER),
			COALESCE(AVG(network_up),0), COALESCE(AVG(network_down),0), COALESCE(MAX(total_sent),0), COALESCE(MAX(total_recv),0),
			COALESCE(AVG(load1),0), COALESCE(AVG(load5),0), COALESCE(AVG(load15),0), MAX(uptime), MAX(created_at),
			COALESCE(MAX(cpu_usage),0), COALESCE(MAX(CASE WHEN memory_total > 0 THEN memory_used * 100.0 / memory_total END),0),
			COALESCE(MAX(network_up),0), COALESCE(MAX(network_down),0), `+metricMaskSQL(true)+`
		 FROM metrics WHERE agent_id = ? AND created_at >= ? AND (? = 0 OR created_at < ?)
		 GROUP BY agent_id, created_at / ? ORDER BY created_at`,
		agentID, since, until, until, bucketSeconds,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []MetricRow
	for rows.Next() {
		var m MetricRow
		if err := rows.Scan(&m.ID, &m.AgentID, &m.CPUUsage, &m.MemoryTotal, &m.MemoryUsed,
			&m.DiskTotal, &m.DiskUsed, &m.NetworkUp, &m.NetworkDown,
			&m.TotalSent, &m.TotalRecv, &m.Load1, &m.Load5, &m.Load15, &m.Uptime, &m.CreatedAt,
			&m.CPUPeak, &m.MemoryPeakPct, &m.NetworkUpPeak, &m.NetworkDownPeak, &m.MissingMetrics); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, rows.Err()
}

// GetMetricsHourly serves windows beyond raw retention from metrics_hourly,
// in the same avg+peak row shape as getMetricsAggregated. At 1h granularity a
// 30d window is at most 720 rows — no query-time aggregation needed.
func (s *Store) GetMetricsHourly(agentID, since, until int64) ([]MetricRow, error) {
	rows, err := s.db.Query(
		`SELECT 0, agent_id, COALESCE(cpu_avg, 0), CAST(ROUND(COALESCE(memory_total, 0)) AS INTEGER), CAST(ROUND(COALESCE(memory_used, 0)) AS INTEGER),
			CAST(ROUND(COALESCE(disk_total, 0)) AS INTEGER), CAST(ROUND(COALESCE(disk_used, 0)) AS INTEGER), COALESCE(net_up_avg, 0), COALESCE(net_down_avg, 0),
			COALESCE(total_sent, 0), COALESCE(total_recv, 0), COALESCE(load1, 0), COALESCE(load5, 0),
			COALESCE(load15, 0), COALESCE(uptime, 0), hour_start,
			COALESCE(cpu_peak, 0), COALESCE(memory_peak_pct, 0), COALESCE(net_up_peak, 0), COALESCE(net_down_peak, 0),
			(CASE WHEN COALESCE(cpu_samples,CASE WHEN cpu_avg IS NULL THEN 0 ELSE sample_count END)=0 THEN 1 ELSE 0 END +
			 CASE WHEN COALESCE(memory_samples,CASE WHEN memory_total IS NULL THEN 0 ELSE sample_count END)=0 THEN 2 ELSE 0 END +
			 CASE WHEN COALESCE(disk_samples,CASE WHEN disk_total IS NULL THEN 0 ELSE sample_count END)=0 THEN 4 ELSE 0 END +
			 CASE WHEN COALESCE(network_samples,CASE WHEN net_up_avg IS NULL THEN 0 ELSE sample_count END)=0 THEN 8 ELSE 0 END +
			 CASE WHEN COALESCE(load_samples,CASE WHEN load1 IS NULL THEN 0 ELSE sample_count END)=0 THEN 16 ELSE 0 END)
		 FROM metrics_hourly WHERE agent_id = ? AND hour_start >= ? AND (? = 0 OR hour_start < ?)
		 ORDER BY hour_start`,
		agentID, since, until, until,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []MetricRow
	for rows.Next() {
		var m MetricRow
		if err := rows.Scan(&m.ID, &m.AgentID, &m.CPUUsage, &m.MemoryTotal, &m.MemoryUsed,
			&m.DiskTotal, &m.DiskUsed, &m.NetworkUp, &m.NetworkDown,
			&m.TotalSent, &m.TotalRecv, &m.Load1, &m.Load5, &m.Load15, &m.Uptime, &m.CreatedAt,
			&m.CPUPeak, &m.MemoryPeakPct, &m.NetworkUpPeak, &m.NetworkDownPeak, &m.MissingMetrics); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, rows.Err()
}

// GetTCPingHourly mirrors GetMetricsHourly for TCPing lines. Target is not
// kept in the rollup; the dashboard keys series by name only.
func (s *Store) GetTCPingHourly(agentID, since, until int64) ([]TCPingRow, error) {
	rows, err := s.db.Query(
		`SELECT 0, agent_id, name, '', COALESCE(latency_avg, 0),
			CASE WHEN success_count > 0 THEN 1 ELSE 0 END,
			sample_count, success_count, hour_start, COALESCE(latency_peak, 0)
		 FROM tcpping_hourly WHERE agent_id = ? AND hour_start >= ? AND (? = 0 OR hour_start < ?)
		 ORDER BY hour_start, name`,
		agentID, since, until, until,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TCPingRow
	for rows.Next() {
		var t TCPingRow
		var success int
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Name, &t.Target, &t.LatencyMs, &success,
			&t.SampleCount, &t.SuccessCount, &t.CreatedAt, &t.LatencyPeak); err != nil {
			return nil, err
		}
		t.Success = success == 1
		results = append(results, t)
	}
	return results, rows.Err()
}

func (s *Store) GetTCPingResultsWindowSampled(agentID int64, since, until, bucketSeconds int64) ([]TCPingRow, error) {
	if bucketSeconds > 0 {
		return s.getTCPingResultsAggregated(agentID, since, until, bucketSeconds)
	}
	rows, err := s.db.Query(
		`SELECT id, agent_id, name, target, latency_ms, success, 1, success, created_at
		 FROM tcpping_results WHERE agent_id = ? AND created_at >= ? AND (? = 0 OR created_at < ?) ORDER BY created_at`,
		agentID, since, until, until,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TCPingRow
	for rows.Next() {
		var t TCPingRow
		var success int
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Name, &t.Target, &t.LatencyMs, &success, &t.SampleCount, &t.SuccessCount, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Success = success == 1
		results = append(results, t)
	}
	return results, rows.Err()
}

func (s *Store) getTCPingResultsAggregated(agentID int64, since, until, bucketSeconds int64) ([]TCPingRow, error) {
	rows, err := s.db.Query(
		`SELECT MAX(id), agent_id, name, target,
			COALESCE(AVG(CASE WHEN success = 1 THEN latency_ms END), 0),
			CASE WHEN SUM(success) > 0 THEN 1 ELSE 0 END,
			COUNT(*), SUM(success), MAX(created_at), COALESCE(MAX(CASE WHEN success = 1 THEN latency_ms END), 0)
		 FROM tcpping_results WHERE agent_id = ? AND created_at >= ? AND (? = 0 OR created_at < ?)
		 GROUP BY agent_id, name, target, created_at / ? ORDER BY created_at, name`,
		agentID, since, until, until, bucketSeconds,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TCPingRow
	for rows.Next() {
		var t TCPingRow
		var success int
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Name, &t.Target, &t.LatencyMs, &success, &t.SampleCount, &t.SuccessCount, &t.CreatedAt, &t.LatencyPeak); err != nil {
			return nil, err
		}
		t.Success = success == 1
		results = append(results, t)
	}
	return results, rows.Err()
}

type ServerStats struct {
	AgentCount  int   `json:"agent_count"`
	MetricCount int   `json:"metric_count"`
	TCPingCount int   `json:"tcpping_count"`
	DBSize      int64 `json:"db_size"`
}

func (s *Store) GetLatestTCPing(agentID int64) ([]TCPingRow, error) {
	var encoded string
	err := s.db.QueryRow("SELECT results_json FROM agent_tcpping_current WHERE agent_id = ?", agentID).Scan(&encoded)
	if err == nil {
		var current []TCPingRow
		if err := json.Unmarshal([]byte(encoded), &current); err != nil {
			return nil, err
		}
		return current, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	// Existing databases retain their last raw batch until the next Agent report.

	rows, err := s.db.Query(
		`SELECT id, agent_id, name, target, latency_ms, success, created_at
		 FROM tcpping_results WHERE agent_id = ? AND created_at = (
		   SELECT MAX(created_at) FROM tcpping_results WHERE agent_id = ?
		 )`,
		agentID, agentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TCPingRow
	for rows.Next() {
		var t TCPingRow
		var success int
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Name, &t.Target, &t.LatencyMs, &success, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Success = success == 1
		results = append(results, t)
	}
	return results, rows.Err()
}

func (s *Store) GetTraffic(agentID int64) (sent, recv int64, err error) {
	usage, err := s.GetTrafficUsage(agentID)
	if err != nil {
		return 0, 0, err
	}
	return usage.Sent, usage.Recv, nil
}

func (s *Store) GetTrafficUsage(agentID int64) (*TrafficUsage, error) {
	var expiresAt sql.NullInt64
	var billingPeriod string
	err := s.db.QueryRow(
		"SELECT expires_at, billing_period FROM agents WHERE id = ?", agentID,
	).Scan(&expiresAt, &billingPeriod)
	if err == sql.ErrNoRows {
		return &TrafficUsage{Reason: "agent_not_found"}, nil
	}
	if err != nil {
		return nil, err
	}
	var expiry *int64
	if expiresAt.Valid {
		expiry = &expiresAt.Int64
	}
	cycle, configured, err := activeTrafficCycle(s.db, agentID, billingPeriod, expiry, s.now())
	if err != nil {
		return nil, err
	}
	if !configured {
		return &TrafficUsage{Reason: "billing_not_configured"}, nil
	}

	usage := &TrafficUsage{Available: true}

	var baseSent, baseRecv, usageSent, usageRecv sql.NullInt64
	err = s.db.QueryRow(
		`SELECT total_sent, total_recv, usage_sent, usage_recv
		 FROM traffic_baselines WHERE agent_id = ? AND boundary = ?`,
		agentID, cycle.Start,
	).Scan(&baseSent, &baseRecv, &usageSent, &usageRecv)
	if err == sql.ErrNoRows {
		// No snapshot yet (pre-baseline install or first report of the cycle
		// not seen): fall back to the earliest surviving metric of the cycle.
		err = s.db.QueryRow(
			`SELECT total_sent, total_recv FROM metrics WHERE agent_id = ? AND created_at >= ? AND created_at < ? AND `+networkCounterPresent+` ORDER BY created_at, id LIMIT 1`,
			agentID, cycle.Start, cycle.End,
		).Scan(&baseSent, &baseRecv)
	}
	if err == sql.ErrNoRows {
		usage.Reason = "no_data"
		return usage, nil
	}
	if err != nil {
		return nil, err
	}
	if usageSent.Valid && usageRecv.Valid {
		usage.Sent = usageSent.Int64
		usage.Recv = usageRecv.Int64
		usage.HasData = true
		return usage, nil
	}

	var latestSent, latestRecv sql.NullInt64
	err = s.db.QueryRow(
		`SELECT total_sent, total_recv FROM metrics WHERE agent_id = ? AND created_at >= ? AND created_at < ? AND `+networkCounterPresent+` ORDER BY created_at DESC, id DESC LIMIT 1`,
		agentID, cycle.Start, cycle.End,
	).Scan(&latestSent, &latestRecv)
	if err == sql.ErrNoRows {
		usage.Reason = "no_data"
		return usage, nil
	}
	if err != nil {
		return nil, err
	}

	usage.Sent = latestSent.Int64 - baseSent.Int64
	usage.Recv = latestRecv.Int64 - baseRecv.Int64
	usage.Sent = max(0, usage.Sent)
	usage.Recv = max(0, usage.Recv)
	usage.HasData = true
	return usage, nil
}

// EnsureTrafficBaseline snapshots the billing cycle's starting traffic
// counters, once per agent per cycle. Seeded from the earliest metric at or
// after the boundary so pre-upgrade data still in the retention window is
// preserved; a no-op until such a metric exists (retried on the next report).
func (s *Store) EnsureTrafficBaseline(agentID, boundary int64) error {
	var expiry sql.NullInt64
	var period string
	if err := s.db.QueryRow("SELECT expires_at,billing_period FROM agents WHERE id=?", agentID).Scan(&expiry, &period); err != nil {
		return err
	}
	var expiresAt *int64
	if expiry.Valid {
		expiresAt = &expiry.Int64
	}
	cycle, configured, err := activeTrafficCycle(s.db, agentID, period, expiresAt, time.Unix(boundary, 0).In(s.now().Location()))
	if err != nil {
		return err
	}
	if !configured {
		return nil
	}
	if cycle.Start != boundary {
		return fmt.Errorf("traffic boundary is not the start of a configured cycle")
	}
	return ensureTrafficBaseline(s.db, s.now().Unix(), agentID, cycle)
}

func ensureTrafficBaseline(x execer, stampedAt, agentID int64, cycle trafficCycle) error {
	_, err := x.Exec(
		`INSERT OR IGNORE INTO traffic_baselines(agent_id, boundary, total_sent, total_recv, created_at, accounting_version)
		 SELECT ?, ?, total_sent, total_recv, ?, 1
		 FROM metrics WHERE agent_id = ? AND created_at >= ? AND created_at < ? AND `+networkCounterPresent+`
		 ORDER BY created_at, id LIMIT 1`,
		agentID, cycle.Start, stampedAt, agentID, cycle.Start, cycle.End,
	)
	return err
}

// Permanent nodes use calendar months; finite nodes keep their renewal day.
func trafficResetDay(period string, expiresAt *int64) (int, bool) {
	if period == "forever" {
		return 1, true
	}
	if expiresAt == nil {
		return 0, false
	}
	return billingAnchor(*expiresAt, time.Local).Day(), true
}

func monthlyBoundary(day int, now time.Time) time.Time {
	y, m, _ := now.Date()
	boundary := clampedMonthDay(y, m, day, now.Location())
	if now.Before(boundary) {
		// time.Date normalizes month 0 to December of the previous year.
		boundary = clampedMonthDay(y, m-1, day, now.Location())
	}
	return boundary
}

// clampedMonthDay returns midnight of the given day in y/m, clamped to the
// month's last day (reset day 31 in February yields Feb 28/29, not a
// normalized date in March).
func clampedMonthDay(y int, m time.Month, day int, loc *time.Location) time.Time {
	if last := time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day(); day > last {
		day = last
	}
	return time.Date(y, m, day, 0, 0, 0, 0, loc)
}

func calcNextReset(expiresAtUnix int64, period string, now time.Time) (int, string) {
	anchor := billingAnchor(expiresAtUnix, now.Location())
	addMonths := 0
	switch period {
	case "1m":
		addMonths = 1
	case "3m":
		addMonths = 3
	case "6m":
		addMonths = 6
	case "12m":
		addMonths = 12
	}
	if addMonths == 0 {
		return 0, ""
	}
	// Each candidate is derived from the original anchor (not the previous
	// candidate) with the day clamped per month, so a day-31 anchor stays on
	// the month's last day instead of drifting via AddDate normalization.
	hour, min, sec := anchor.Clock()
	loc := anchor.Location()
	y, m, _ := anchor.Date()
	nextReset := anchor
	for k := 1; !nextReset.After(now); k++ {
		b := clampedMonthDay(y, m+time.Month(k*addMonths), anchor.Day(), loc)
		nextReset = time.Date(b.Year(), b.Month(), b.Day(), hour, min, sec, 0, loc)
	}
	// Calendar days are independent of 23/25-hour daylight-saving days.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	due := time.Date(nextReset.Year(), nextReset.Month(), nextReset.Day(), 0, 0, 0, 0, time.UTC)
	days := int(due.Sub(today) / (24 * time.Hour))
	if days == 0 {
		days = 1
	}
	return days, nextReset.Format("2006-01-02")
}

func (s *Store) GetStats() (*ServerStats, error) {
	var stats ServerStats
	counts := []struct {
		query string
		dst   *int
	}{
		{"SELECT COUNT(*) FROM agents", &stats.AgentCount},
		{"SELECT COUNT(*) FROM metrics", &stats.MetricCount},
		{"SELECT COUNT(*) FROM tcpping_results", &stats.TCPingCount},
	}
	for _, c := range counts {
		if err := s.db.QueryRow(c.query).Scan(c.dst); err != nil {
			return nil, err
		}
	}
	if fi, err := os.Stat(s.path); err == nil {
		stats.DBSize = fi.Size()
	}
	return &stats, nil
}

func (s *Store) DeleteAgent(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := deleteAgentTx(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteAgentTx(tx *sql.Tx, id int64) error {
	var hostname string
	err := tx.QueryRow(`SELECT hostname FROM agents WHERE id = ?`, id).Scan(&hostname)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	if _, err := tx.Exec("DELETE FROM metrics WHERE agent_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM tcpping_results WHERE agent_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM agents WHERE id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM traffic_accounting_legacy WHERE agent_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM traffic_baselines WHERE agent_id = ?", id); err != nil {
		return err
	}
	// Hourly rollups too: agent ids are reused (plain INTEGER PRIMARY KEY),
	// so leftovers would surface as the previous agent's history on the
	// next agent that claims the id.
	if _, err := tx.Exec("DELETE FROM metrics_hourly WHERE agent_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM tcpping_hourly WHERE agent_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM agent_tcpping_current WHERE agent_id = ?", id); err != nil {
		return err
	}
	for _, table := range []string{"traffic_cycle_transition", "metrics_rollup_legacy", "metrics_hourly_rollup_legacy", "tcpping_results_rollup_legacy", "tcpping_hourly_rollup_legacy"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE agent_id=?", id); err != nil {
			return err
		}
	}
	if hostname != "" {
		if _, err := tx.Exec(`DELETE FROM agent_tokens WHERE hostname = ?`, hostname); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) startPurgeLoop() {
	go func() {
		s.PurgeOldData()
		for range time.NewTicker(1 * time.Hour).C {
			s.PurgeOldData()
		}
	}()
}

func (s *Store) Close() error {
	return s.db.Close()
}
