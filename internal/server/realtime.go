package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const maxLiveNodes = 4096
const maxEventClients = 32

type liveEntry struct {
	metric   *MetricRow
	received int64
	token    string // private: never serialized into a public event
	revision uint64
}
type nodeEvent struct {
	Kind string           `json:"kind"`
	ID   int64            `json:"id"`
	Node *agentWithMetric `json:"node,omitempty"`
}
type liveState struct {
	mu       sync.Mutex
	nodes    map[int64]liveEntry
	clients  map[chan nodeEvent]bool
	sequence uint64
	closed   bool
}

func newLiveState() *liveState {
	return &liveState{nodes: make(map[int64]liveEntry), clients: make(map[chan nodeEvent]bool)}
}
func defaultReportInterval(value int) int {
	if value <= 0 {
		return 30
	}
	return value
}

func (l *liveState) broadcastLocked(event nodeEvent) {
	for client := range l.clients {
		select {
		case client <- event:
		default:
			close(client)
			delete(l.clients, client)
		}
	}
}
func (l *liveState) remove(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.nodes, id)
	l.broadcastLocked(nodeEvent{Kind: "remove", ID: id})
}
func (l *liveState) prune(valid map[int64]bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id := range l.nodes {
		if !valid[id] {
			delete(l.nodes, id)
		}
	}
}
func (l *liveState) subscribe() (chan nodeEvent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || len(l.clients) >= maxEventClients {
		return nil, false
	}
	client := make(chan nodeEvent, 32)
	l.clients[client] = true
	return client, true
}

// Close ends streaming handlers before HTTP graceful shutdown waits for them.
// A new Server process starts with a new cache and accepting event hub.
func (h *Handler) Close() {
	l := h.store.live
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	for client := range l.clients {
		close(client)
		delete(l.clients, client)
	}
}
func (l *liveState) unsubscribe(client chan nodeEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clients[client] {
		delete(l.clients, client)
		close(client)
	}
}

type freshness struct {
	SampleAt         int64 `json:"sample_at"`
	ReceivedAt       int64 `json:"received_at"`
	LastFullReportAt int64 `json:"last_full_report_at"`
	LastFullSampleAt int64 `json:"last_full_sample_at"`
	LastPingAt       int64 `json:"last_ping_at"`
	OfflineAfter     int   `json:"offline_after"`
	HistoryStale     bool  `json:"history_stale"`
}
type agentWithMetric struct {
	Agent         AgentRow      `json:"agent"`
	Metric        *MetricRow    `json:"latest_metric,omitempty"`
	LatestTCPing  []TCPingRow   `json:"latest_tcpping,omitempty"`
	TCPingEnabled bool          `json:"tcpping_enabled"`
	Traffic       *TrafficUsage `json:"traffic,omitempty"`
	ExpiryDays    int           `json:"expiry_days"`
	ExpiryDate    string        `json:"expiry_date"`
	Live          freshness     `json:"live"`
	Revision      uint64        `json:"revision"`
}

func (h *Handler) nodeSnapshot(a AgentRow) (*agentWithMetric, error) {
	base, err := h.committedNode(a)
	if err != nil {
		return nil, err
	}
	result := &base
	probes := result.LatestTCPing
	if a.ExpiresAt != nil {
		result.ExpiryDays, result.ExpiryDate = calcRenewalDue(*a.ExpiresAt, a.BillingPeriod, h.now())
	}
	if a.LastSeen != nil {
		result.Live.LastFullReportAt = *a.LastSeen
		result.Live.ReceivedAt = *a.LastSeen
	}
	result.Live.LastFullSampleAt = a.ReportSampleAt
	for _, p := range probes {
		result.Live.LastPingAt = max(result.Live.LastPingAt, p.CreatedAt)
	}
	l := h.store.live
	l.mu.Lock()
	entry, ok := l.nodes[a.ID]
	if ok && entry.token != "" {
		// Also covers token revocation/deletion by another Server CLI process.
		var valid bool
		err = h.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_tokens t JOIN agents a ON a.hostname=t.hostname WHERE a.id=? AND t.token=?)`, a.ID, entry.token).Scan(&valid)
		if err == nil && !valid {
			delete(l.nodes, a.ID)
			entry = liveEntry{}
		}
	}
	if err == nil {
		result.Revision = entry.revision
		if entry.metric != nil && (result.Metric == nil || entry.metric.CreatedAt >= result.Metric.CreatedAt) {
			result.Metric = entry.metric
		}
		result.Live.ReceivedAt = max(result.Live.ReceivedAt, entry.received)
	}
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if result.Metric != nil {
		result.Live.SampleAt = result.Metric.CreatedAt
	}
	seen := result.Live.ReceivedAt
	result.Agent.LastSeen = &seen
	interval := defaultReportInterval(a.ReportInterval)
	onlineInterval := interval
	if a.RealtimeInterval > 0 {
		onlineInterval = a.RealtimeInterval
	}
	result.Live.OfflineAfter = max(120, 3*onlineInterval+15)
	historyAt := result.Live.LastFullReportAt
	if result.Live.LastFullSampleAt > 0 {
		historyAt = min(historyAt, result.Live.LastFullSampleAt)
	}
	result.Live.HistoryStale = h.now().Unix()-historyAt > int64(max(120, 3*interval+15))
	return result, nil
}

func (h *Handler) snapshots() ([]agentWithMetric, error) {
	// Keep metadata, current probes and accounting in the same committed view.
	// Realtime counter order remains independent and never advances DB ordering.
	h.fullMu.Lock()
	defer h.fullMu.Unlock()
	if err := h.syncAdminRevision(); err != nil {
		return nil, err
	}
	agents, err := h.store.GetAgents()
	if err != nil {
		return nil, err
	}
	result := make([]agentWithMetric, 0, len(agents))
	valid := make(map[int64]bool, len(agents))
	for _, a := range agents {
		node, err := h.nodeSnapshot(a)
		if err != nil {
			return nil, err
		}
		result = append(result, *node)
		valid[a.ID] = true
	}
	h.store.live.prune(valid)
	return result, nil
}

func (h *Handler) publishNode(id int64, kind string) {
	l := h.store.live
	l.mu.Lock()
	hasClients := len(l.clients) > 0
	l.mu.Unlock()
	if !hasClients && cacheCommittedSnapshots {
		return
	}
	if err := h.syncAdminRevision(); err != nil {
		return
	}
	a, err := h.store.GetAgent(id)
	if err != nil {
		return
	}
	node, err := h.nodeSnapshot(a)
	if err != nil {
		return
	}
	l = h.store.live
	l.mu.Lock()
	defer l.mu.Unlock()
	var present bool
	if err := h.store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND hostname=?)", id, node.Agent.Hostname).Scan(&present); err != nil || !present {
		return
	}
	l.sequence++
	node.Revision = l.sequence
	entry, exists := l.nodes[id]
	if exists || len(l.nodes) < maxLiveNodes {
		entry.revision = l.sequence
		l.nodes[id] = entry
	}
	l.broadcastLocked(nodeEvent{Kind: kind, ID: id, Node: node})
}

func (h *Handler) handleRealtime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	select {
	case h.realtimeSlots <- struct{}{}:
		defer func() { <-h.realtimeSlots }()
	default:
		http.Error(w, "realtime concurrency limit reached", 503)
		return
	}
	now := h.now()
	if !h.realtimeGlobal.Allow(now) {
		http.Error(w, "realtime rate limit exceeded", 429)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid realtime body", 400)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		http.Error(w, "invalid realtime body", 400)
		return
	}
	allowed := map[string]bool{"hostname": true, "created_at": true, "cpu": true, "memory": true, "disk": true, "network": true, "load": true, "uptime": true}
	for key := range fields {
		if !allowed[key] {
			http.Error(w, "unexpected realtime field", 400)
			return
		}
	}
	var req reportRequest
	if json.Unmarshal(body, &req) != nil || validateReport(&req, now) != nil || req.CreatedAt == nil || *req.CreatedAt < now.Add(-5*time.Minute).Unix() || *req.CreatedAt > now.Add(5*time.Minute).Unix() {
		http.Error(w, "invalid realtime sample", 400)
		return
	}
	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	row, err := h.store.LookupToken(token)
	if err != nil && !errors.Is(err, ErrTokenNotFound) {
		http.Error(w, "internal error", 500)
		return
	}
	if err != nil || row.Hostname == "" || row.Hostname != req.Hostname {
		http.Error(w, "registered token and matching hostname required", 401)
		return
	}
	if !h.realtimeLimiter.Allow(row.ID, now) {
		http.Error(w, "realtime rate limit exceeded", 429)
		return
	}
	id, found, err := h.store.AgentIDByHostname(req.Hostname)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if !found {
		http.Error(w, "register with a full report first", 409)
		return
	}
	metric, _ := metricFromReport(&req)
	if metric == nil {
		metric = &MetricRow{MissingMetrics: 31, Uptime: int64(req.Uptime)}
	}
	metric.CreatedAt = min(*req.CreatedAt, now.Unix())
	metric.AgentID = id
	l := h.store.live
	l.mu.Lock()
	var valid bool
	err = h.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM agents a JOIN agent_tokens t ON t.hostname=a.hostname WHERE a.id=? AND t.token=?)`, id, token).Scan(&valid)
	entry, exists := l.nodes[id]
	if err != nil {
		l.mu.Unlock()
		http.Error(w, "internal error", 500)
		return
	}
	if !valid {
		delete(l.nodes, id)
		l.mu.Unlock()
		http.Error(w, "unauthorized", 401)
		return
	}
	if !exists && len(l.nodes) >= maxLiveNodes {
		l.mu.Unlock()
		http.Error(w, "realtime cache capacity reached", 503)
		return
	}
	if entry.token != token {
		entry = liveEntry{token: token}
	}
	if entry.metric == nil || metric.CreatedAt > entry.metric.CreatedAt {
		entry.metric = metric
	}
	entry.received = now.Unix()
	l.nodes[id] = entry
	l.mu.Unlock()
	h.fullMu.Lock()
	h.publishNode(id, "realtime")
	h.fullMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}

func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	client, ok := h.store.live.subscribe()
	if !ok {
		http.Error(w, "too many event clients", 503)
		return
	}
	defer h.store.live.unsubscribe(client)
	// Subscribe before loading the snapshot, so updates during the read remain
	// queued. Per-node revisions and sample timestamps reject older queued data.
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{}) // Only this streaming route bypasses the global 60s deadline.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(event string, data any) error {
		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded); err != nil {
			return err
		}
		err = controller.Flush()
		_ = controller.SetWriteDeadline(time.Time{})
		return err
	}
	resync := func() error {
		nodes, err := h.snapshots()
		if err != nil {
			return err
		}
		return write("snapshot", nodes)
	}
	if resync() != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-client:
			if !open {
				return
			}
			if write("node", event) != nil {
				return
			}
		case <-heartbeat.C:
			// The periodic snapshot doubles as a heartbeat, reconciles external CLI
			// deletions/reordering and ages online/history freshness without writes.
			if resync() != nil {
				return
			}
		}
	}
}
