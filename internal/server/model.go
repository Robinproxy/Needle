package server

type AgentRow struct {
	ID            int64  `json:"id"`
	Hostname      string `json:"hostname"`
	Token         string `json:"-"`
	Region        string `json:"region"`
	CreatedAt     int64  `json:"created_at"`
	ExpiresAt     *int64 `json:"expires_at"`
	BillingPeriod string `json:"billing_period"`
}

type MetricRow struct {
	ID          int64   `json:"id"`
	AgentID     int64   `json:"agent_id"`
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryTotal int64   `json:"memory_total"`
	MemoryUsed  int64   `json:"memory_used"`
	DiskTotal   int64   `json:"disk_total"`
	DiskUsed    int64   `json:"disk_used"`
	NetworkUp   float64 `json:"network_up"`
	NetworkDown float64 `json:"network_down"`
	TotalSent   int64   `json:"total_sent"`
	TotalRecv   int64   `json:"total_recv"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	Uptime      int64   `json:"uptime"`
	CreatedAt   int64   `json:"created_at"`
}

type TCPingRow struct {
	ID        int64   `json:"id"`
	AgentID   int64   `json:"agent_id"`
	Name      string  `json:"name"`
	Target    string  `json:"target"`
	LatencyMs float64 `json:"latency_ms"`
	Success   bool    `json:"success"`
	CreatedAt int64   `json:"created_at"`
}

// TokenRow is an allowed agent credential (whitelist).
// Hostname empty means allowed but not yet bound on first report.
type TokenRow struct {
	ID        int64
	Token     string
	Hostname  string // empty if unbound
	CreatedAt int64
	BoundAt   *int64
}
