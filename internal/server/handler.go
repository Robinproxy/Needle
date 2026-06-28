package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

var Version = "dev"

var reportThrottle sync.Map

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type Handler struct {
	store       *Store
	serverToken string
}

func NewHandler(store *Store, serverToken string) *Handler {
	return &Handler{store: store, serverToken: serverToken}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/report", h.handleReport)
	mux.HandleFunc("/api/unregister", h.handleUnregister)
	mux.HandleFunc("/api/info", h.handleInfo)
	mux.HandleFunc("/api/agents", h.handleAgents)
	mux.HandleFunc("/api/agents/", h.handleAgentDetail)

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("static files: %v", err)
	}
	fileServer := http.FileServer(http.FS(staticFS))
	mux.Handle("/", fileServer)
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
		Token    string `json:"token"`
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token := req.Token
	if token == "" {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if token != h.serverToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if req.Hostname == "" {
		http.Error(w, "hostname required", http.StatusBadRequest)
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
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}
	http.Error(w, "agent not found", http.StatusNotFound)
}

func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Token    string `json:"token"`
		Hostname string `json:"hostname"`
		Region   string `json:"region"`
		CPU      *struct {
			Percent float64 `json:"percent"`
		} `json:"cpu"`
		Memory *struct {
			Total uint64 `json:"total"`
			Used  uint64 `json:"used"`
		} `json:"memory"`
		Disk *struct {
			Total uint64 `json:"total"`
			Used  uint64 `json:"used"`
		} `json:"disk"`
		Network *struct {
			Up        int64 `json:"up"`
			Down      int64 `json:"down"`
			TotalSent int64 `json:"total_sent"`
			TotalRecv int64 `json:"total_recv"`
		} `json:"network"`
		Load *struct {
			Load1  float64 `json:"load1"`
			Load5  float64 `json:"load5"`
			Load15 float64 `json:"load15"`
		} `json:"load"`
		Uptime uint64 `json:"uptime"`
		CreatedAt *int64 `json:"created_at"`
		TCPing []struct {
			Name      string  `json:"name"`
			Target    string  `json:"target"`
			LatencyMs float64 `json:"latency_ms"`
			Success   bool    `json:"success"`
		} `json:"tcpping"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	token := req.Token
	if token == "" {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if token != h.serverToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if req.Hostname == "" {
		http.Error(w, "hostname required", http.StatusBadRequest)
		return
	}

	agentID, err := h.store.UpsertAgent(req.Hostname, req.Token, req.Region)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// per-agent rate limit: 50 req/s (20ms window)
	last, loaded := reportThrottle.LoadOrStore(agentID, time.Now().UnixMilli())
	if loaded && time.Now().UnixMilli()-last.(int64) < 20 {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}
	reportThrottle.Store(agentID, time.Now().UnixMilli())

	var createdAt int64
	if req.CreatedAt != nil {
		createdAt = *req.CreatedAt
	}
	now := time.Now()
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

		h.store.InsertMetric(&MetricRow{
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
		}, createdAt)
	}

	for _, t := range req.TCPing {
		h.store.InsertTCPing(&TCPingRow{
			AgentID:   agentID,
			Name:      t.Name,
			Target:    t.Target,
			LatencyMs: t.LatencyMs,
			Success:   t.Success,
		}, createdAt)
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
		Agent        AgentRow     `json:"agent"`
		Metric       *MetricRow   `json:"latest_metric,omitempty"`
		LatestTCPing []TCPingRow  `json:"latest_tcpping,omitempty"`
	}

	result := make([]agentWithMetric, 0, len(agents))
	for _, a := range agents {
		m, _ := h.store.GetLatestMetric(a.ID)
		t, _ := h.store.GetLatestTCPing(a.ID)
		result = append(result, agentWithMetric{Agent: a, Metric: m, LatestTCPing: t})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
		resetDay := 1
		if dStr := r.URL.Query().Get("reset_day"); dStr != "" {
			if d, err := strconv.Atoi(dStr); err == nil && d >= 1 && d <= 31 {
				resetDay = d
			}
		}
		sent, recv, err := h.store.GetTraffic(agentID, resetDay)
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
