package server

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var regionCodeRe = regexp.MustCompile(`(?i)^[a-z]{2}$`)

const (
	maxRegionLen       = 32
	maxTCPingNameLen   = 64
	maxTCPingTargetLen = 256
	maxTCPingPerReport = 32
)

func stripControlsCap(s string, maxRunes int) string {
	var b strings.Builder
	n := 0
	for _, ch := range s {
		if ch < 32 || ch == 127 {
			continue
		}
		b.WriteRune(ch)
		n++
		if n >= maxRunes {
			break
		}
	}
	return b.String()
}

func sanitizeRegion(r string) string {
	r = strings.TrimSpace(r)
	if r == "" {
		return ""
	}
	if regionCodeRe.MatchString(r) {
		return strings.ToUpper(r)
	}
	return stripControlsCap(r, maxRegionLen)
}

func sanitizeTCPingName(s string) string {
	return stripControlsCap(strings.TrimSpace(s), maxTCPingNameLen)
}

func sanitizeTCPingTarget(s string) string {
	return stripControlsCap(strings.TrimSpace(s), maxTCPingTargetLen)
}

//go:embed static/*
var staticFiles embed.FS

var Version = "dev"

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type Handler struct {
	store         *Store
	globalLimiter globalLimiter
	tokenLimiter  *tokenLimiter
	now           func() time.Time
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store, tokenLimiter: newTokenLimiter(), now: time.Now}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/report", h.handleReport)
	mux.HandleFunc("/api/unregister", h.handleUnregister)
	mux.HandleFunc("/api/health", h.handleHealth)
	mux.HandleFunc("/api/info", h.handleInfo)
	mux.HandleFunc("/api/agents", h.handleAgents)
	mux.HandleFunc("/api/agents/", h.handleAgentDetail)

	mime.AddExtensionType(".svg", "image/svg+xml")
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("static files: %v", err)
	}
	fileServer := http.FileServer(http.FS(staticFS))
	mux.Handle("/", fileServer)
}

func bearerToken(r *http.Request) (string, bool) {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || len(fields[1]) > 256 || hasControl(fields[1]) {
		return "", false
	}
	return fields[1], true
}

// authorizeAgent checks whitelist token and hostname binding rules.
// On first report with unbound token, binds hostname.
func (h *Handler) authorizeAgent(token, hostname string) (*TokenRow, error) {
	if token == "" {
		return nil, errors.New("missing token")
	}
	if hostname == "" {
		return nil, errors.New("hostname required")
	}
	row, err := h.store.LookupToken(token)
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return nil, errors.New("unauthorized")
		}
		return nil, err
	}
	if row.Hostname == "" {
		if err := h.store.BindToken(token, hostname); err != nil {
			log.Printf("report: bind failed for hostname %q: %v", hostname, err)
			return nil, err
		}
		log.Printf("agent registered: hostname=%q", hostname)
		row.Hostname = hostname
		return row, nil
	}
	if row.Hostname != hostname {
		return nil, errors.New("token bound to another hostname")
	}
	return row, nil
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats, _ := h.store.GetStats()
	if stats == nil {
		stats = &ServerStats{}
	}
	info := map[string]interface{}{
		"version":  Version,
		"db_stats": stats,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (h *Handler) handleUnregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req struct {
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if req.Hostname == "" {
		http.Error(w, "hostname required", http.StatusBadRequest)
		return
	}

	row, err := h.store.LookupToken(token)
	if err != nil || row.Hostname == "" || row.Hostname != req.Hostname {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	agents, err := h.store.GetAgents()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, a := range agents {
		if a.Hostname == req.Hostname {
			if err := h.store.DeleteAgent(a.ID); err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			h.tokenLimiter.Delete(row.ID)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}
	// agent row gone but token still bound
	_ = h.store.RevokeToken(token)
	http.Error(w, "agent not found", http.StatusNotFound)
}

func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	now := h.now()
	if !h.globalLimiter.Allow(now) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req reportRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := validateReport(&req, now); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tokenRow, err := h.authorizeAgent(token, req.Hostname)
	if err != nil {
		switch {
		case errors.Is(err, ErrHostnameTaken), errors.Is(err, ErrTokenAlreadyBound):
			log.Printf("report: conflict for hostname %q: %v", req.Hostname, err)
			http.Error(w, err.Error(), http.StatusConflict)
		case err.Error() == "hostname required":
			http.Error(w, "hostname required", http.StatusBadRequest)
		default:
			log.Printf("report: unauthorized for hostname %q", req.Hostname)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
		return
	}
	if !h.tokenLimiter.Allow(tokenRow.ID, now) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	region := sanitizeRegion(req.Region)
	agentID, err := h.store.UpsertAgent(req.Hostname, token, region, req.ExpiresAt, req.BillingPeriod)
	if err != nil {
		log.Printf("report: upsert agent %q: %v", req.Hostname, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var createdAt int64
	if req.CreatedAt != nil {
		createdAt = *req.CreatedAt
	}
	if createdAt < now.Add(-30*24*time.Hour).Unix() || createdAt > now.Add(5*time.Minute).Unix() {
		createdAt = now.Unix()
	}

	if req.CPU != nil {
		var memTotal, memUsed, diskTotal, diskUsed int64
		if req.Memory != nil {
			memTotal = int64(req.Memory.Total)
			memUsed = int64(req.Memory.Used)
		}
		if req.Disk != nil {
			diskTotal = int64(req.Disk.Total)
			diskUsed = int64(req.Disk.Used)
		}
		var netUp, netDown, totalSent, totalRecv int64
		if req.Network != nil {
			netUp = req.Network.Up
			netDown = req.Network.Down
			totalSent = req.Network.TotalSent
			totalRecv = req.Network.TotalRecv
		}
		var load1, load5, load15 float64
		if req.Load != nil {
			load1 = req.Load.Load1
			load5 = req.Load.Load5
			load15 = req.Load.Load15
		}

		if err := h.store.InsertMetric(&MetricRow{
			AgentID:     agentID,
			CPUUsage:    req.CPU.Percent,
			MemoryTotal: memTotal,
			MemoryUsed:  memUsed,
			DiskTotal:   diskTotal,
			DiskUsed:    diskUsed,
			NetworkUp:   float64(netUp),
			NetworkDown: float64(netDown),
			TotalSent:   totalSent,
			TotalRecv:   totalRecv,
			Load1:       load1,
			Load5:       load5,
			Load15:      load15,
			Uptime:      int64(req.Uptime),
		}, createdAt); err != nil {
			log.Printf("report: insert metric for agent %q: %v", req.Hostname, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	for _, t := range req.TCPing {
		name := sanitizeTCPingName(t.Name)
		if name == "" {
			continue
		}
		if err := h.store.InsertTCPing(&TCPingRow{
			AgentID:   agentID,
			Name:      name,
			Target:    sanitizeTCPingTarget(t.Target),
			LatencyMs: t.LatencyMs,
			Success:   t.Success,
		}, createdAt); err != nil {
			log.Printf("report: insert tcpping for agent %q: %v", req.Hostname, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agents, err := h.store.GetAgents()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type agentWithMetric struct {
		Agent        AgentRow    `json:"agent"`
		Metric       *MetricRow  `json:"latest_metric,omitempty"`
		LatestTCPing []TCPingRow `json:"latest_tcpping,omitempty"`
		ExpiryDays   int         `json:"expiry_days"`
		ExpiryDate   string      `json:"expiry_date"`
	}

	result := make([]agentWithMetric, 0, len(agents))
	for _, a := range agents {
		m, _ := h.store.GetLatestMetric(a.ID)
		t, _ := h.store.GetLatestTCPing(a.ID)
		expiryDays, expiryDate := 0, ""
		if a.ExpiresAt != nil && a.BillingPeriod != "" {
			expiryDays, expiryDate = calcNextReset(*a.ExpiresAt, a.BillingPeriod)
		}
		result = append(result, agentWithMetric{Agent: a, Metric: m, LatestTCPing: t, ExpiryDays: expiryDays, ExpiryDate: expiryDate})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	agentID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid agent id", http.StatusBadRequest)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if len(parts) == 1 {
		m, _ := h.store.GetLatestMetric(agentID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(m)
		return
	}

	since := time.Now().Add(-1 * time.Hour).Unix()
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		if s, err := strconv.ParseInt(sinceStr, 10, 64); err == nil {
			since = s
		}
	}
	if rangeStr := r.URL.Query().Get("range"); rangeStr != "" {
		if d, err := time.ParseDuration(rangeStr); err == nil {
			if d > 720*time.Hour {
				d = 720 * time.Hour
			}
			since = time.Now().Add(-d).Unix()
		}
	}

	switch parts[1] {
	case "metrics":
		metrics, err := h.store.GetMetrics(agentID, since)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for i := range metrics {
			if metrics[i].NetworkUp > 1e12 {
				metrics[i].NetworkUp = 0
			}
			if metrics[i].NetworkDown > 1e12 {
				metrics[i].NetworkDown = 0
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metrics)

	case "tcpping":
		results, err := h.store.GetTCPingResults(agentID, since)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)

	case "traffic":
		sent, recv, err := h.store.GetTraffic(agentID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int64{"sent": sent, "recv": recv})

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}
