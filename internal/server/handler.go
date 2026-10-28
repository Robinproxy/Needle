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
	"sync"
	"time"

	"needle/internal/logging"
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
	committedMu    sync.Mutex
	committed      map[int64]committedEntry
	adminRevision  int64
	schemaRevision int64

	store           *Store
	globalLimiter   globalLimiter
	tokenLimiter    *tokenLimiter
	now             func() time.Time
	realtimeGlobal  globalLimiter
	realtimeLimiter *tokenLimiter
	fullMu          sync.Mutex
	realtimeSlots   chan struct{}
	logs            *logging.Tracker
	admin           *adminState
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store, committed: make(map[int64]committedEntry), tokenLimiter: newTokenLimiter(), realtimeLimiter: newTokenLimiter(), realtimeSlots: make(chan struct{}, 16), now: time.Now, logs: logging.NewTracker("server.api", log.Printf), admin: newAdminState()}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/", h.handleAdmin)
	mux.HandleFunc("/admin", h.handleAdminPage)
	mux.HandleFunc("/api/report", h.handleReport)
	mux.HandleFunc("/api/report-capabilities", h.handleReportCapabilities)
	mux.HandleFunc("/api/realtime", h.handleRealtime)
	mux.HandleFunc("/api/events", h.handleEvents)
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
// First binding is committed atomically with the report in SaveReport.
func (h *Handler) authorizeAgent(token, hostname string) (*TokenRow, error) {
	if token == "" || hostname == "" {
		return nil, ErrTokenNotFound
	}
	row, err := h.store.LookupToken(token)
	if err != nil {
		return nil, err
	}
	if row.Hostname == "" {
		return row, nil
	}
	if row.Hostname != hostname {
		return nil, ErrTokenAlreadyBound
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
	h.logs.Check("stats", "ERROR", err)
	if err != nil {
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

	h.fullMu.Lock()
	id, err := h.store.revokeToken(token, req.Hostname)
	h.fullMu.Unlock()
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			http.Error(w, "unauthorized", 401)
		} else {
			http.Error(w, "internal error", 500)
		}
		return
	}
	h.tokenLimiter.Delete(id)
	h.realtimeLimiter.Delete(id)
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
		if errors.Is(err, ErrTokenNotFound) || errors.Is(err, ErrTokenAlreadyBound) {
			h.logs.Check("credential_lookup", "ERROR", nil)
			// Aggregate rejections across untrusted hostnames; never retain a
			// map entry or print a new line for every presented credential.
			h.logs.Check("authorization", "WARN", err, "hostname", req.Hostname, "status", 401)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		} else {
			h.logs.Check("credential_lookup", "ERROR", err, "hostname", req.Hostname)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	h.logs.Check("credential_lookup", "ERROR", nil)
	if !h.tokenLimiter.Allow(tokenRow.ID, now) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var createdAt int64
	if req.CreatedAt != nil {
		createdAt = *req.CreatedAt
	}
	if req.ReportID == "" && (createdAt < now.Add(-30*24*time.Hour).Unix() || createdAt > now.Add(5*time.Minute).Unix()) {
		createdAt = now.Unix()
	}

	encodedReport, _ := json.Marshal(req)
	digest := sha256.Sum256(encodedReport)
	write := &ReportWrite{
		ServerManaged: req.ServerManaged,
		CPUModel:      req.CPUModel, OSName: req.OSName, Arch: req.Arch,
		ReportID: req.ReportID, Digest: hex.EncodeToString(digest[:]),
		Token:    token,
		Interval: req.Interval, RealtimeInterval: req.RealtimeInterval,
		BillingCost:         req.BillingCost,
		Hostname:            req.Hostname,
		Region:              sanitizeRegion(req.Region),
		ExpiresAt:           req.ExpiresAt,
		BillingPeriod:       req.BillingPeriod,
		MonthlyTrafficLimit: req.MonthlyTrafficLimit,
		CreatedAt:           createdAt,
	}

	write.Metric, write.CPUCores = metricFromReport(&req)
	if write.Metric != nil {
		write.Metric.BootID = req.BootID
		if req.Network != nil {
			write.Metric.ScopeID = req.Network.ScopeID
		}
	}
	if req.TCPingCreatedAt != nil {
		write.TCPingCreatedAt = *req.TCPingCreatedAt
	}

	for _, t := range req.TCPing {
		name := sanitizeTCPingName(t.Name)
		write.TCPings = append(write.TCPings, TCPingRow{
			Name:      name,
			Target:    sanitizeTCPingTarget(t.Target),
			LatencyMs: t.LatencyMs,
			Success:   t.Success,
		})
	}

	h.fullMu.Lock()
	id, err := h.store.SaveReport(write)
	if err != nil {
		h.fullMu.Unlock()
		if errors.Is(err, ErrTokenNotFound) {
			http.Error(w, "unauthorized", 401)
			return
		}
		if errors.Is(err, ErrHostnameTaken) || errors.Is(err, ErrTokenAlreadyBound) {
			http.Error(w, "credential/hostname binding conflict", 409)
			return
		}
		if errors.Is(err, ErrReportConflict) {
			http.Error(w, err.Error(), 409)
			return
		}
		if errors.Is(err, ErrReportExpired) {
			http.Error(w, err.Error(), 422)
			return
		}
		h.logs.Check("save_report", "ERROR", err, "hostname", req.Hostname)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.logs.Check("save_report", "ERROR", nil, "hostname", req.Hostname)

	if !write.Duplicate {
		h.invalidateCommitted(id)
		h.publishNode(id, "history")
	}
	h.fullMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "report_id": req.ReportID, "protocol": reportProtocol, "duplicate": write.Duplicate})
}

func (h *Handler) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := h.snapshots()
	h.logs.Check("snapshots", "ERROR", err)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	parts := strings.Split(path, "/")
	if parts[0] == "" {
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
		h.fullMu.Lock()
		defer h.fullMu.Unlock()
		if err := h.syncAdminRevision(); err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		a, err := h.store.GetAgent(agentID)
		if err != nil {
			http.Error(w, "agent not found", 404)
			return
		}
		node, err := h.nodeSnapshot(a)
		if err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(node.Metric)
		return
	}

	now := h.now()
	since := now.Add(-1 * time.Hour).Unix()
	until := int64(0)
	bucketSeconds := int64(0)
	useHourly := false
	sinceStr, untilStr := r.URL.Query().Get("since"), r.URL.Query().Get("until")
	if untilStr != "" {
		var parseErr error
		since, parseErr = strconv.ParseInt(sinceStr, 10, 64)
		if parseErr == nil {
			until, parseErr = strconv.ParseInt(untilStr, 10, 64)
		}
		// Windows up to a week: day drill-down (~24h) and week drill-down (~7d).
		if parseErr != nil || until <= since || until-since > int64((8*24*time.Hour)/time.Second) || until > now.Add(5*time.Minute).Unix() {
			http.Error(w, "invalid time window", http.StatusBadRequest)
			return
		}
		// Windows starting before raw retention read the hourly rollups; this
		// also gives straddling windows a complete (if coarser) picture.
		if since < now.Add(-rawRetention).Unix() {
			useHourly = true
		} else if dur := until - since; dur > int64((25*time.Hour)/time.Second) {
			// Day windows stay raw; longer (week) windows aim for ~720
			// points, in whole minutes.
			bucketSeconds = (dur/720 + 59) / 60 * 60
			if bucketSeconds < 60 {
				bucketSeconds = 60
			}
		}
	} else if rangeStr := r.URL.Query().Get("range"); rangeStr != "" {
		if d, err := time.ParseDuration(rangeStr); err == nil {
			// 30d is the longest view, backed by hourly rollups (90d retention).
			if d > 720*time.Hour {
				d = 720 * time.Hour
			}
			since = now.Add(-d).Unix()
			if d > 168*time.Hour {
				// Beyond raw retention: read the hourly tables directly.
				useHourly = true
			} else {
				bucketSeconds = historyBucketSeconds(d)
			}
		}
	}

	switch parts[1] {
	case "metrics":
		var metrics []MetricRow
		var err error
		if useHourly {
			metrics, err = h.store.GetMetricsHourly(agentID, since, until)
		} else {
			metrics, err = h.store.GetMetricsWindowSampled(agentID, since, until, bucketSeconds)
		}
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
		var results []TCPingRow
		var err error
		if useHourly {
			results, err = h.store.GetTCPingHourly(agentID, since, until)
		} else {
			results, err = h.store.GetTCPingResultsWindowSampled(agentID, since, until, bucketSeconds)
		}
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

func metricFromReport(req *reportRequest) (*MetricRow, int) {
	write := &ReportWrite{}
	if req.CPU != nil || req.Memory != nil || req.Disk != nil || req.Network != nil || req.Load != nil {
		missing := 0
		var cpuUsage float64
		if req.CPU != nil {
			write.CPUCores = req.CPU.Cores
			cpuUsage = req.CPU.Percent
		} else {
			missing |= missingCPU
		}
		var memTotal, memUsed, diskTotal, diskUsed int64
		if req.Memory != nil {
			memTotal = int64(req.Memory.Total)
			memUsed = int64(req.Memory.Used)
		} else {
			missing |= missingMemory
		}
		if req.Disk != nil {
			diskTotal = int64(req.Disk.Total)
			diskUsed = int64(req.Disk.Used)
		} else {
			missing |= missingDisk
		}
		var netUp, netDown, totalSent, totalRecv int64
		if req.Network != nil {
			netUp = req.Network.Up
			netDown = req.Network.Down
			totalSent = req.Network.TotalSent
			totalRecv = req.Network.TotalRecv
		} else {
			missing |= missingNetwork
		}
		var load1, load5, load15 float64
		if req.Load != nil {
			load1 = req.Load.Load1
			load5 = req.Load.Load5
			load15 = req.Load.Load15
		} else {
			missing |= missingLoad
		}
		write.Metric = &MetricRow{
			MissingMetrics: missing,
			CPUUsage:       cpuUsage,
			MemoryTotal:    memTotal,
			MemoryUsed:     memUsed,
			DiskTotal:      diskTotal,
			DiskUsed:       diskUsed,
			NetworkUp:      float64(netUp),
			NetworkDown:    float64(netDown),
			TotalSent:      totalSent,
			TotalRecv:      totalRecv,
			Load1:          load1,
			Load5:          load5,
			Load15:         load15,
			Uptime:         int64(req.Uptime),
		}
	}

	return write.Metric, write.CPUCores
}

func (h *Handler) handleReportCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	if _, err := h.store.LookupToken(token); err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			http.Error(w, "unauthorized", 401)
		} else {
			http.Error(w, "internal error", 500)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"protocol": reportProtocol, "max_report_age_seconds": int(reportAcceptAge.Seconds()), "unregister_retains_history": true})
}
