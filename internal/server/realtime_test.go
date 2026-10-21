package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sendRealtime(t *testing.T, h *Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/realtime", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.handleRealtime(w, r)
	return w
}
func realtimeFixture(t *testing.T) (*Handler, *Store, *time.Time, int64) {
	t.Helper()
	h, s := newTestHandler(t)
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	s.now = h.now
	s.AllowToken("live-secret")
	w := sendReport(t, h, "Bearer live-secret", fmt.Sprintf(`{"hostname":"live","billing_period":"forever","interval":30,"realtime_interval":5,"created_at":%d,"network":{"total_sent":100,"total_recv":200},"tcpping":[{"name":"CTv4","target":"127.0.0.1:80","success":true,"latency_ms":10}]}`, now.Unix()))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	id, _, _ := s.AgentIDByHostname("live")
	return h, s, &now, id
}
func TestRealtimeDoesNotWriteHistoryOrLedgerAndKeepsPing(t *testing.T) {
	h, s, now, id := realtimeFixture(t)
	var beforeMetrics, beforePing int
	s.db.QueryRow("SELECT COUNT(*) FROM metrics").Scan(&beforeMetrics)
	s.db.QueryRow("SELECT COUNT(*) FROM tcpping_results").Scan(&beforePing)
	before, _ := s.GetTrafficUsage(id)
	for i := 1; i <= 6; i++ {
		*now = now.Add(5 * time.Second)
		w := sendRealtime(t, h, "live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d,"cpu":{"percent":%d,"cores":4},"network":{"total_sent":999999,"total_recv":999999}}`, now.Unix(), i))
		if w.Code != 200 {
			t.Fatalf("realtime %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	var metrics, ping int
	s.db.QueryRow("SELECT COUNT(*) FROM metrics").Scan(&metrics)
	s.db.QueryRow("SELECT COUNT(*) FROM tcpping_results").Scan(&ping)
	after, _ := s.GetTrafficUsage(id)
	if metrics != beforeMetrics || ping != beforePing || *after != *before {
		t.Fatalf("realtime modified persistence: %d/%d %d/%d %+v/%+v", metrics, beforeMetrics, ping, beforePing, before, after)
	}
	a, _ := s.GetAgent(id)
	node, err := h.nodeSnapshot(a)
	if err != nil || node.Metric.CPUUsage != 6 || len(node.LatestTCPing) != 1 || !node.TCPingEnabled {
		t.Fatalf("snapshot=%+v %v", node, err)
	}
	if *node.Agent.LastSeen != now.Unix() || *a.LastSeen == now.Unix() {
		t.Fatal("receipt must update memory without DB last_seen writes")
	}
}

func TestNewerRealtimeDoesNotAdvanceHistoricalOrderOrTraffic(t *testing.T) {
	h, s, now, id := realtimeFixture(t)
	base := *now
	*now = base.Add(35 * time.Second)
	w := sendRealtime(t, h, "live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d,"cpu":{"percent":77}}`, now.Unix()))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	*now = base.Add(36 * time.Second)
	w = sendReport(t, h, "Bearer live-secret", fmt.Sprintf(`{"hostname":"live","billing_period":"forever","interval":30,"realtime_interval":5,"created_at":%d,"cpu":{"percent":20},"network":{"total_sent":160,"total_recv":280}}`, base.Add(30*time.Second).Unix()))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, _ := s.GetAgent(id)
	node, err := h.nodeSnapshot(a)
	usage, _ := s.GetTrafficUsage(id)
	if err != nil || node.Metric.CPUUsage != 77 || usage.Sent != 60 || usage.Recv != 80 || a.ReportSampleAt != base.Add(30*time.Second).Unix() {
		t.Fatalf("snapshot=%+v usage=%+v metadata=%+v err=%v", node, usage, a, err)
	}
	rows, _ := s.GetMetricsWindowSampled(id, base.Unix(), 0, 0)
	if len(rows) != 2 || rows[1].CPUUsage != 20 {
		t.Fatalf("historical sample lost: %+v", rows)
	}
}

func TestRealtimeBindingRevocationAndValidation(t *testing.T) {
	h, s, now, id := realtimeFixture(t)
	s.AllowToken("not-bound")
	for _, tc := range []struct {
		token, body string
		status      int
	}{
		{"not-bound", fmt.Sprintf(`{"hostname":"live","created_at":%d}`, now.Unix()), 401},
		{"live-secret", fmt.Sprintf(`{"hostname":"other","created_at":%d}`, now.Unix()), 401},
		{"live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d,"tcpping":[]}`, now.Unix()), 400},
		{"live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d,"billing_period":"forever"}`, now.Unix()), 400},
		{"live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d,"cpu":{"percent":101}}`, now.Unix()), 400},
		{"live-secret", `{"hostname":"live"}`, 400},
		{"live-secret", strings.Repeat("x", (64<<10)+1), 400},
	} {
		if w := sendRealtime(t, h, tc.token, tc.body); w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body[:min(80, len(tc.body))], w.Code, w.Body.String())
		}
	}
	*now = now.Add(time.Second)
	body := fmt.Sprintf(`{"hostname":"live","created_at":%d,"cpu":{"percent":5}}`, now.Unix())
	if w := sendRealtime(t, h, "live-secret", body); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := s.RevokeToken("live-secret"); err != nil {
		t.Fatal(err)
	}
	if _, found := s.live.nodes[id]; found {
		t.Fatal("revoked node left in cache")
	}
	if w := sendRealtime(t, h, "live-secret", body); w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestSeparateRateLimitsAndFreshness(t *testing.T) {
	h, s, now, id := realtimeFixture(t)
	*now = now.Add(200 * time.Second)
	body := fmt.Sprintf(`{"hostname":"live","created_at":%d,"memory":{"total":100,"used":50}}`, now.Unix())
	for i := 0; i < 3; i++ {
		w := sendRealtime(t, h, "live-secret", body)
		if i < 2 && w.Code != 200 || i == 2 && w.Code != 429 {
			t.Fatal(i, w.Code)
		}
	}
	a, _ := s.GetAgent(id)
	node, _ := h.nodeSnapshot(a)
	if !node.Live.HistoryStale || node.Live.ReceivedAt != now.Unix() {
		t.Fatalf("freshness=%+v", node.Live)
	}
	w := sendReport(t, h, "Bearer live-secret", `{"hostname":"live","interval":86400}`)
	if w.Code != 200 {
		t.Fatal("realtime exhausted history quota", w.Code)
	}
	a, _ = s.GetAgent(id)
	node, _ = h.nodeSnapshot(a)
	if node.Live.OfflineAfter < 86400 {
		t.Fatal("long interval would be prematurely offline")
	}
}

func TestSlowEventSubscriberIsBoundedAndDisconnected(t *testing.T) {
	l := newLiveState()
	client, _ := l.subscribe()
	l.mu.Lock()
	for i := 0; i < 40; i++ {
		l.broadcastLocked(nodeEvent{ID: int64(i)})
	}
	clients := len(l.clients)
	l.mu.Unlock()
	if clients != 0 || len(client) != 32 {
		t.Fatalf("clients=%d queued=%d", clients, len(client))
	}
	for range client {
	} // closed, without blocking the ingestion path
}

func TestShutdownReleasesEventStreams(t *testing.T) {
	h, _, _, _ := realtimeFixture(t)
	client, ok := h.store.live.subscribe()
	if !ok {
		t.Fatal("subscribe failed")
	}
	h.Close()
	if _, open := <-client; open {
		t.Fatal("shutdown left stream open")
	}
	if _, ok := h.store.live.subscribe(); ok {
		t.Fatal("shutdown accepted another stream")
	}
}

func TestRestartRestoresPersistedTimesWithoutInventingRealtime(t *testing.T) {
	h, s, now, id := realtimeFixture(t)
	storedAt := now.Unix()
	*now = now.Add(35 * time.Second)
	if w := sendRealtime(t, h, "live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d,"cpu":{"percent":77}}`, now.Unix())); w.Code != 200 {
		t.Fatal(w.Code)
	}
	reopened, err := NewStoreCLI(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewHandler(reopened)
	restarted.now = h.now
	reopened.now = h.now
	a, _ := reopened.GetAgent(id)
	snapshot, err := restarted.nodeSnapshot(a)
	if err != nil || snapshot.Live.ReceivedAt != storedAt || snapshot.Live.SampleAt != storedAt || snapshot.Metric.CPUUsage == 77 {
		t.Fatalf("restart invented fresh state: %+v %v", snapshot, err)
	}
}

func TestRealtimeConcurrencyCannotConsumeHistoricalSlots(t *testing.T) {
	h, _, now, _ := realtimeFixture(t)
	for i := 0; i < cap(h.realtimeSlots); i++ {
		h.realtimeSlots <- struct{}{}
	}
	defer func() {
		for len(h.realtimeSlots) > 0 {
			<-h.realtimeSlots
		}
	}()
	if w := sendRealtime(t, h, "live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d}`, now.Unix())); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if w := sendReport(t, h, "Bearer live-secret", `{"hostname":"live"}`); w.Code != 200 {
		t.Fatal("history blocked by realtime slots", w.Code)
	}
}

func TestProbeTimeIsIndependentOfSystemSampleAndSendTime(t *testing.T) {
	h, s, now, id := realtimeFixture(t)
	sampled := now.Unix()
	*now = now.Add(10 * time.Second)
	w := sendReport(t, h, "Bearer live-secret", fmt.Sprintf(`{"hostname":"live","created_at":%d,"tcpping_created_at":%d,"cpu":{"percent":15},"tcpping":[{"name":"CTv4","target":"127.0.0.1:80","latency_ms":10,"success":true}]}`, sampled, sampled+5))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	probes, err := s.GetLatestTCPing(id)
	if err != nil || len(probes) != 1 || probes[0].CreatedAt != sampled+5 {
		t.Fatalf("probe time was replaced by historical/send time: %+v %v", probes, err)
	}
	metric, _ := s.GetLatestMetric(id)
	if metric.CreatedAt != sampled {
		t.Fatal("probe time moved historical sample", metric.CreatedAt)
	}
}

func TestSSEInitialSnapshotAndRealtimeEvent(t *testing.T) {
	h, _, _, _ := realtimeFixture(t)
	mux := http.NewServeMux()
	h.Register(mux)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Config.WriteTimeout = 40 * time.Millisecond
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/events", nil)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("X-Accel-Buffering") != "no" || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(response.Header)
	}
	scanner := bufio.NewScanner(response.Body)
	if !scanner.Scan() || scanner.Text() != "event: snapshot" || !scanner.Scan() {
		t.Fatal("missing initial snapshot")
	}
	var nodes []agentWithMetric
	if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &nodes); err != nil || len(nodes) != 1 {
		t.Fatal(scanner.Text(), err)
	}
	if strings.Contains(scanner.Text(), "live-secret") {
		t.Fatal("SSE leaked token")
	}
	time.Sleep(80 * time.Millisecond) // Stream must outlive the ordinary route deadline.
	body := fmt.Sprintf(`{"hostname":"live","created_at":%d,"cpu":{"percent":33}}`, h.now().Unix())
	if w := sendRealtime(t, h, "live-secret", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for scanner.Scan() {
		if scanner.Text() == "event: node" {
			break
		}
	}
	if !scanner.Scan() || !strings.Contains(scanner.Text(), `"cpu_usage":33`) {
		t.Fatal("missing node delta", scanner.Text())
	}
	cancel()
}
