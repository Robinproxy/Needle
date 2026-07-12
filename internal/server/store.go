package server

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var (
	ErrTokenNotFound      = errors.New("token not found")
	ErrTokenAlreadyBound = errors.New("token already bound to another hostname")
	ErrHostnameTaken      = errors.New("hostname already bound to another token")
)

type Store struct {
	db *sql.DB
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
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if purge {
		s.startPurgeLoop()
	}
	return s, nil
}

func (s *Store) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			hostname TEXT UNIQUE NOT NULL,
			token TEXT NOT NULL,
			region TEXT DEFAULT '',
			expires_at INTEGER,
			billing_period TEXT DEFAULT '',
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
		`CREATE INDEX IF NOT EXISTS idx_metrics_agent_time ON metrics(agent_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_tcpping_agent_time ON tcpping_results(agent_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS agent_tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token TEXT UNIQUE NOT NULL,
			hostname TEXT,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			bound_at INTEGER
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_tokens_hostname
			ON agent_tokens(hostname) WHERE hostname IS NOT NULL AND hostname != ''`,
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
	return nil
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
	t, err := s.LookupToken(token)
	if err != nil {
		return err
	}
	if t.Hostname != "" {
		var agentID int64
		err = s.db.QueryRow(`SELECT id FROM agents WHERE hostname = ?`, t.Hostname).Scan(&agentID)
		if err == nil {
			_ = s.DeleteAgent(agentID)
		} else if err != sql.ErrNoRows {
			return err
		}
	}
	_, err = s.db.Exec(`DELETE FROM agent_tokens WHERE token = ?`, token)
	return err
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
	return out, nil
}

func (s *Store) DeleteTokenByHostname(hostname string) error {
	_, err := s.db.Exec(`DELETE FROM agent_tokens WHERE hostname = ?`, hostname)
	return err
}

func (s *Store) UpsertAgent(hostname, token, region string, expiresAt *int64, billingPeriod string) (int64, error) {
	_, err := s.db.Exec(
		`INSERT INTO agents(hostname, token, region, expires_at, billing_period) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(hostname) DO UPDATE SET
		   token = excluded.token,
		   region = excluded.region,
		   expires_at = excluded.expires_at,
		   billing_period = excluded.billing_period`,
		hostname, token, region, expiresAt, billingPeriod,
	)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := s.db.QueryRow("SELECT id FROM agents WHERE hostname = ?", hostname).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) InsertMetric(m *MetricRow, createdAt int64) error {
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO metrics(agent_id, cpu_usage, memory_total, memory_used,
			disk_total, disk_used, network_up, network_down,
			total_sent, total_recv, load1, load5, load15, uptime, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.AgentID, m.CPUUsage, m.MemoryTotal, m.MemoryUsed,
		m.DiskTotal, m.DiskUsed, m.NetworkUp, m.NetworkDown,
		m.TotalSent, m.TotalRecv, m.Load1, m.Load5, m.Load15, m.Uptime, createdAt,
	)
	return err
}

func (s *Store) InsertTCPing(t *TCPingRow, createdAt int64) error {
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	suc := 0
	if t.Success {
		suc = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO tcpping_results(agent_id, name, target, latency_ms, success, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		t.AgentID, t.Name, t.Target, t.LatencyMs, suc, createdAt,
	)
	return err
}

func (s *Store) GetAgents() ([]AgentRow, error) {
	rows, err := s.db.Query("SELECT id, hostname, created_at, region, expires_at, billing_period FROM agents ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []AgentRow
	for rows.Next() {
		var a AgentRow
		if err := rows.Scan(&a.ID, &a.Hostname, &a.CreatedAt, &a.Region, &a.ExpiresAt, &a.BillingPeriod); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, nil
}

func (s *Store) GetLatestMetric(agentID int64) (*MetricRow, error) {
	row := s.db.QueryRow(
		`SELECT id, agent_id, cpu_usage, memory_total, memory_used,
			disk_total, disk_used, network_up, network_down,
			total_sent, total_recv, load1, load5, load15, uptime, created_at
		 FROM metrics WHERE agent_id = ? ORDER BY created_at DESC LIMIT 1`,
		agentID,
	)
	var m MetricRow
	err := row.Scan(&m.ID, &m.AgentID, &m.CPUUsage, &m.MemoryTotal, &m.MemoryUsed,
		&m.DiskTotal, &m.DiskUsed, &m.NetworkUp, &m.NetworkDown,
		&m.TotalSent, &m.TotalRecv, &m.Load1, &m.Load5, &m.Load15, &m.Uptime, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) GetMetrics(agentID int64, since int64) ([]MetricRow, error) {
	rows, err := s.db.Query(
		`SELECT id, agent_id, cpu_usage, memory_total, memory_used,
			disk_total, disk_used, network_up, network_down,
			total_sent, total_recv, load1, load5, load15, uptime, created_at
		 FROM metrics WHERE agent_id = ? AND created_at >= ? ORDER BY created_at`,
		agentID, since,
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
			&m.TotalSent, &m.TotalRecv, &m.Load1, &m.Load5, &m.Load15, &m.Uptime, &m.CreatedAt); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, nil
}

func (s *Store) GetTCPingResults(agentID int64, since int64) ([]TCPingRow, error) {
	rows, err := s.db.Query(
		`SELECT id, agent_id, name, target, latency_ms, success, created_at
		 FROM tcpping_results WHERE agent_id = ? AND created_at >= ? ORDER BY created_at`,
		agentID, since,
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
	return results, nil
}

type ServerStats struct {
	AgentCount  int   `json:"agent_count"`
	MetricCount int   `json:"metric_count"`
	TCPingCount int   `json:"tcpping_count"`
	DBSize      int64 `json:"db_size"`
}

func (s *Store) GetLatestTCPing(agentID int64) ([]TCPingRow, error) {
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
	return results, nil
}

func (s *Store) GetTraffic(agentID int64) (sent, recv int64, err error) {
	var expiresAt sql.NullInt64
	err = s.db.QueryRow(
		"SELECT expires_at FROM agents WHERE id = ?", agentID,
	).Scan(&expiresAt)
	if err != nil || !expiresAt.Valid {
		return 0, 0, nil
	}

	resetDay := time.Unix(expiresAt.Int64, 0).Day()
	boundary := monthlyBoundary(resetDay)
	boundaryUnix := boundary.Unix()

	var baseSent, baseRecv sql.NullInt64
	err = s.db.QueryRow(
		`SELECT total_sent, total_recv FROM metrics WHERE agent_id = ? AND created_at >= ? ORDER BY created_at LIMIT 1`,
		agentID, boundaryUnix,
	).Scan(&baseSent, &baseRecv)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}

	var latestSent, latestRecv sql.NullInt64
	err = s.db.QueryRow(
		`SELECT total_sent, total_recv FROM metrics WHERE agent_id = ? ORDER BY created_at DESC LIMIT 1`,
		agentID,
	).Scan(&latestSent, &latestRecv)
	if err != nil {
		return 0, 0, err
	}

	sent = latestSent.Int64 - baseSent.Int64
	recv = latestRecv.Int64 - baseRecv.Int64
	if sent < 0 {
		sent = latestSent.Int64
	}
	if recv < 0 {
		recv = latestRecv.Int64
	}
	return sent, recv, nil
}

func monthlyBoundary(day int) time.Time {
	now := time.Now()
	y, m, _ := now.Date()
	loc := now.Location()

	boundary := time.Date(y, m, day, 0, 0, 0, 0, loc)
	if boundary.Month() != m {
		boundary = time.Date(y, m+1, 0, 0, 0, 0, 0, loc)
	}

	if now.Before(boundary) {
		boundary = boundary.AddDate(0, -1, 0)
		y2, m2, _ := boundary.Date()
		boundary = time.Date(y2, m2, day, 0, 0, 0, 0, loc)
		if boundary.Month() != m2 {
			boundary = time.Date(y2, m2+1, 0, 0, 0, 0, 0, loc)
		}
	}

	return boundary
}

func calcNextReset(expiresAtUnix int64, period string) (int, string) {
	anchor := time.Unix(expiresAtUnix, 0)
	now := time.Now()
	addMonths := 0
	switch period {
	case "1m": addMonths = 1
	case "3m": addMonths = 3
	case "6m": addMonths = 6
	case "12m": addMonths = 12
	}
	if addMonths == 0 {
		return 0, ""
	}
	nextReset := anchor
	for !nextReset.After(now) {
		nextReset = nextReset.AddDate(0, addMonths, 0)
	}
	days := int(nextReset.Sub(now).Hours()/24) + 1
	return days, nextReset.Format("2006-01-02")
}

func (s *Store) GetStats() (*ServerStats, error) {
	var stats ServerStats
	s.db.QueryRow("SELECT COUNT(*) FROM agents").Scan(&stats.AgentCount)
	s.db.QueryRow("SELECT COUNT(*) FROM metrics").Scan(&stats.MetricCount)
	s.db.QueryRow("SELECT COUNT(*) FROM tcpping_results").Scan(&stats.TCPingCount)

	var path string
	s.db.QueryRow("PRAGMA database_list").Scan(new(string), new(string), &path)
	if fi, err := os.Stat(path); err == nil {
		stats.DBSize = fi.Size()
	}
	return &stats, nil
}

func (s *Store) DeleteAgent(id int64) error {
	var hostname string
	_ = s.db.QueryRow(`SELECT hostname FROM agents WHERE id = ?`, id).Scan(&hostname)

	_, err := s.db.Exec("DELETE FROM metrics WHERE agent_id = ?", id)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM tcpping_results WHERE agent_id = ?", id)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM agents WHERE id = ?", id)
	if err != nil {
		return err
	}
	if hostname != "" {
		_, _ = s.db.Exec(`DELETE FROM agent_tokens WHERE hostname = ?`, hostname)
	}
	return nil
}

func (s *Store) PurgeOldData() {
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	if res, err := s.db.Exec("DELETE FROM metrics WHERE created_at < ?", cutoff); err != nil {
		log.Printf("purge metrics: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("purged %d old metric rows", n)
	}
	if res, err := s.db.Exec("DELETE FROM tcpping_results WHERE created_at < ?", cutoff); err != nil {
		log.Printf("purge tcpping: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("purged %d old tcpping rows", n)
	}
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

func nowUnix() int64 {
	return time.Now().Unix()
}
