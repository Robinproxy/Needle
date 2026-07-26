package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"math"
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
		// All assets are same-origin since v0.6.1; 'unsafe-inline' is needed
		// for the inline event handlers and style attributes in the dashboard.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; "+
				"base-uri 'self'; frame-ancestors 'none'")
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
	mux.Handle("/", staticHandler(staticFS))
}

// staticHandler serves the embedded assets with content-hash ETags: embed.FS
// carries no modtimes, so without this every dashboard refresh re-downloads
// every asset. no-cache forces revalidation, keeping clients current across
// deploys at the cost of a cheap 304 round trip.
func staticHandler(fsys fs.FS) http.Handler {
	etags := make(map[string]string)
	fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		etags[path] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})

	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if etag, ok := etags[path]; ok {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", etag)
			if strings.Contains(r.Header.Get("If-None-Match"), etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
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
	stats, err := h.store.GetStats()
	if err != nil {
		log.Printf("info: stats: %v", err)
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

	agentID, found, err := h.store.AgentIDByHostname(req.Hostname)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		// agent row gone but token still bound
		_ = h.store.RevokeToken(token)
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if err := h.store.DeleteAgent(agentID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.tokenLimiter.Delete(row.ID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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

	var createdAt int64
	if req.CreatedAt != nil {
		createdAt = *req.CreatedAt
	}
	if createdAt < now.Add(-30*24*time.Hour).Unix() || createdAt > now.Add(5*time.Minute).Unix() {
		createdAt = now.Unix()
	}

	write := &ReportWrite{
		Hostname:      req.Hostname,
		Region:        sanitizeRegion(req.Region),
		ExpiresAt:     req.ExpiresAt,
		BillingPeriod: req.BillingPeriod,
		CreatedAt:     createdAt,
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
		write.Metric = &MetricRow{
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
		}
	}

	if req.ExpiresAt != nil && req.Network != nil {
		resetDay := time.Unix(*req.ExpiresAt, 0).Day()
		boundary := monthlyBoundary(resetDay, now).Unix()
		write.BaselineBoundary = &boundary
	}

	for _, t := range req.TCPing {
		name := sanitizeTCPingName(t.Name)
		if name == "" {
			continue
		}
		write.TCPings = append(write.TCPings, TCPingRow{
			Name:      name,
			Target:    sanitizeTCPingTarget(t.Target),
			LatencyMs: t.LatencyMs,
			Success:   t.Success,
		})
	}

	if _, err := h.store.SaveReport(write); err != nil {
		log.Printf("report: save for agent %q: %v", req.Hostname, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
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
		Agent        AgentRow      `json:"agent"`
		Metric       *MetricRow    `json:"latest_metric,omitempty"`
		LatestTCPing []TCPingRow   `json:"latest_tcpping,omitempty"`
		Traffic      *TrafficUsage `json:"traffic,omitempty"`
		ExpiryDays   int           `json:"expiry_days"`
		ExpiryDate   string        `json:"expiry_date"`
	}

	now := h.now()
	result := make([]agentWithMetric, 0, len(agents))
	for _, a := range agents {
		m, _ := h.store.GetLatestMetric(a.ID)
		t, _ := h.store.GetLatestTCPing(a.ID)
		traffic, err := h.store.GetTrafficUsage(a.ID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		expiryDays, expiryDate := 0, ""
		if a.ExpiresAt != nil && a.BillingPeriod != "" {
			expiryDays, expiryDate = calcNextReset(*a.ExpiresAt, a.BillingPeriod, now)
		}
		result = append(result, agentWithMetric{Agent: a, Metric: m, LatestTCPing: t, Traffic: traffic, ExpiryDays: expiryDays, ExpiryDate: expiryDate})
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

	now := h.now()
	since := now.Add(-1 * time.Hour).Unix()
	until := int64(0)
	bucketSeconds := int64(0)
	sinceStr, untilStr := r.URL.Query().Get("since"), r.URL.Query().Get("until")
	if untilStr != "" {
		var parseErr error
		since, parseErr = strconv.ParseInt(sinceStr, 10, 64)
		if parseErr == nil {
			until, parseErr = strconv.ParseInt(untilStr, 10, 64)
		}
		if parseErr != nil || until <= since || until-since > int64((25*time.Hour)/time.Second) || until > now.Add(5*time.Minute).Unix() {
			http.Error(w, "invalid time window", http.StatusBadRequest)
			return
		}
	} else if sinceStr != "" {
		var parseErr error
		since, parseErr = strconv.ParseInt(sinceStr, 10, 64)
		if parseErr != nil || since > now.Add(5*time.Minute).Unix() {
			http.Error(w, "invalid since", http.StatusBadRequest)
			return
		}
	} else if rangeStr := r.URL.Query().Get("range"); rangeStr != "" {
		if d, err := time.ParseDuration(rangeStr); err == nil {
			// Metrics are purged after 7 days; longer ranges would only pad
			// the window with rows that no longer exist.
			if d > 168*time.Hour {
				d = 168 * time.Hour
			}
			since = now.Add(-d).Unix()
			bucketSeconds = historyBucketSeconds(d)
		}
	}

	switch parts[1] {
	case "metrics":
		metrics, err := h.store.GetMetricsWindowSampled(agentID, since, until, bucketSeconds)
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
		results, err := h.store.GetTCPingResultsWindowSampled(agentID, since, until, bucketSeconds)
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

func historyBucketSeconds(d time.Duration) int64 {
	if d < 7*24*time.Hour {
		return 0
	}
	bucket := int64(math.Ceil(d.Seconds() / 720))
	if bucket < 900 {
		bucket = 900
	}
	// Whole-minute buckets keep chart timestamps and query plans predictable.
	return ((bucket + 59) / 60) * 60
}
