package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func historyGET(t *testing.T, h *Handler, kind, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.handleAgentDetail(w, httptest.NewRequest(http.MethodGet, "/api/agents/1/"+kind+"?"+query, nil))
	return w
}

func TestHistoryRejectsInvalidAndOverflowingWindowsBeforeQuery(t *testing.T) {
	h, store := newTestHandler(t)
	if _, err := store.db.Exec("DROP TABLE metrics; DROP TABLE tcpping_results"); err != nil {
		t.Fatal(err)
	}
	now := h.now().Unix()
	queries := []string{
		"since=-9223372036854775808&until=0", "since=-1&until=9223372036854775807",
		"since=9223372036854775808&until=1", "since=1&until=-9223372036854775808",
		"since=0&until=1", "since=1&until=1", "since=2&until=1",
		"since=invalid&until=1", "since=1", "until=1", "since=&until=1",
		"range=", "range=nope", "range=0s", "range=-1h", "range=1ns", "range=999999999999h",
		"range=24h&range=48h", "range=24h&since=1&until=2", "since=1&since=2&until=3",
		fmt.Sprintf("since=%d&until=%d", now-9*86400, now),
		fmt.Sprintf("since=%d&until=%d", now-91*86400, now-90*86400),
		fmt.Sprintf("since=%d&until=%d", now, now+301),
	}
	for _, kind := range []string{"metrics", "tcpping"} {
		for _, query := range queries {
			if w := historyGET(t, h, kind, query); w.Code != http.StatusBadRequest {
				t.Fatalf("%s?%s: status=%d body=%s", kind, query, w.Code, w.Body.String())
			}
		}
	}
}

func TestHistoryWindowsShareSamplingPolicy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, hours := range []int{1, 24, 48, 167, 168, 169, 720, 900} {
		query := url.Values{"range": {fmt.Sprintf("%dh", hours)}}
		window, err := parseHistoryWindow(query, now)
		if err != nil {
			t.Fatal(err)
		}
		wantHours := min(hours, 720)
		if window.since != now.Add(-time.Duration(wantHours)*time.Hour).Unix() || window.until != now.Unix()+1 || window.useHourly != (hours > 168) {
			t.Fatalf("%dh: %+v", hours, window)
		}
		if hours <= 24 && window.bucketSeconds != 0 || hours > 25 && window.bucketSeconds == 0 {
			t.Fatalf("unexpected sampling policy: %dh %+v", hours, window)
		}
		if hours <= 169 {
			explicit, err := parseHistoryWindow(url.Values{
				"since": {fmt.Sprint(window.since)}, "until": {fmt.Sprint(window.until)},
			}, now)
			if err != nil || explicit != window {
				t.Fatalf("equivalent windows differ: %+v / %+v / %v", window, explicit, err)
			}
		}
	}
	// A daylight-saving day can contain 25 hours and must retain raw detail.
	window, err := parseHistoryWindow(url.Values{
		"since": {fmt.Sprint(now.Add(-25 * time.Hour).Unix())}, "until": {fmt.Sprint(now.Unix())},
	}, now)
	if err != nil || window.bucketSeconds != 0 || window.useHourly {
		t.Fatalf("25-hour day: %+v / %v", window, err)
	}
}

func TestHistoryEquivalentURLsPreservePeaksAndLoss(t *testing.T) {
	h, store := newTestHandler(t)
	since := h.now().Add(-48 * time.Hour).Unix()
	if _, err := store.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 0 UNION ALL SELECT x+1 FROM seq WHERE x<5759)
		INSERT INTO metrics(agent_id,cpu_usage,uptime,created_at) SELECT 1,CASE WHEN x%8=0 THEN 80 ELSE 20 END,100,?+x*30 FROM seq`, since); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 0 UNION ALL SELECT x+1 FROM seq WHERE x<5759)
		INSERT INTO tcpping_results(agent_id,name,target,latency_ms,success,created_at)
		SELECT 1,name,'example.com:80',15,CASE WHEN x%4=0 THEN 0 ELSE 1 END,?+x*30
		FROM seq CROSS JOIN (SELECT 'CTv4' name UNION ALL SELECT 'CMv6')`, since); err != nil {
		t.Fatal(err)
	}
	for _, hours := range []int{24, 48} {
		rangeQuery := fmt.Sprintf("range=%dh", hours)
		window, err := parseHistoryWindow(url.Values{"range": {fmt.Sprintf("%dh", hours)}}, h.now())
		if err != nil {
			t.Fatal(err)
		}
		explicit := fmt.Sprintf("since=%d&until=%d", window.since, window.until)
		for _, kind := range []string{"metrics", "tcpping"} {
			a, b := historyGET(t, h, kind, rangeQuery), historyGET(t, h, kind, explicit)
			if a.Code != 200 || b.Code != 200 || a.Body.String() != b.Body.String() {
				t.Fatalf("%s %dh: equivalent URLs disagree: %d/%d", kind, hours, a.Code, b.Code)
			}
			if kind == "metrics" {
				var rows []MetricRow
				if err := json.Unmarshal(a.Body.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				if hours == 24 && len(rows) != 2880 || hours == 48 && (len(rows) > 721 || len(rows) < 2) {
					t.Fatalf("%dh metric row count=%d", hours, len(rows))
				}
				if hours == 48 && rows[0].CPUPeak != 80 {
					t.Fatalf("sampling lost CPU peak: %+v", rows[0])
				}
			} else {
				var rows []TCPingRow
				if err := json.Unmarshal(a.Body.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				var samples, successes int64
				for _, row := range rows {
					samples += row.SampleCount
					successes += row.SuccessCount
				}
				want := int64(hours * 120 * 2)
				if samples != want || successes != want*3/4 || hours == 24 && len(rows) != int(want) || hours == 48 && len(rows) > 2*721 {
					t.Fatalf("%dh probe rows=%d samples=%d successes=%d", hours, len(rows), samples, successes)
				}
			}
		}
	}
}

func TestHistoryDenseDayAggregatesInsteadOfTruncating(t *testing.T) {
	h, store := newTestHandler(t)
	now := h.now().Unix()
	if _, err := store.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 0 UNION ALL SELECT x+1 FROM seq WHERE x<4999)
		INSERT INTO metrics(agent_id,cpu_usage,uptime,created_at) SELECT 1,CASE WHEN x=4999 THEN 99 ELSE 10 END,100,?+x FROM seq`, now-5000); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 0 UNION ALL SELECT x+1 FROM seq WHERE x<32768)
		INSERT INTO tcpping_results(agent_id,name,target,latency_ms,success,created_at)
		SELECT 1,'CTv4','example.com:80',CASE WHEN x=32768 THEN 900 ELSE 10 END,1,?+x FROM seq`, now-32769); err != nil {
		t.Fatal(err)
	}
	var metrics []MetricRow
	w := historyGET(t, h, "metrics", "range=24h")
	if err := json.Unmarshal(w.Body.Bytes(), &metrics); err != nil || w.Code != 200 || len(metrics) == 0 || len(metrics) > 721 {
		t.Fatalf("dense metrics: status=%d len=%d err=%v", w.Code, len(metrics), err)
	}
	last := metrics[len(metrics)-1]
	if last.CreatedAt != now-1 || last.CPUPeak != 99 {
		t.Fatalf("sampling truncated newest metric: %+v", last)
	}
	var pings []TCPingRow
	w = historyGET(t, h, "tcpping", "range=24h")
	if err := json.Unmarshal(w.Body.Bytes(), &pings); err != nil || w.Code != 200 || len(pings) == 0 || len(pings) > 721 {
		t.Fatalf("dense pings: status=%d len=%d err=%v", w.Code, len(pings), err)
	}
	var count int64
	for _, ping := range pings {
		count += ping.SampleCount
	}
	if count != 32769 || pings[len(pings)-1].LatencyPeak != 900 || pings[len(pings)-1].CreatedAt != now-1 {
		t.Fatalf("sampling truncated probes: count=%d last=%+v", count, pings[len(pings)-1])
	}
}

func TestHistoryQueryBudgetsAndCanceledContexts(t *testing.T) {
	h, s := newTestHandler(t)
	for _, query := range []string{
		`WITH RECURSIVE seq(x) AS (SELECT 0 UNION ALL SELECT x+1 FROM seq WHERE x<4096)
		INSERT INTO metrics(agent_id,cpu_usage,uptime,created_at) SELECT 1,10,100,1700000000+x*3600 FROM seq`,
		`INSERT INTO metrics_hourly(agent_id,hour_start,cpu_avg,sample_count) SELECT 1,created_at,10,1 FROM metrics`,
		`WITH RECURSIVE seq(x) AS (SELECT 0 UNION ALL SELECT x+1 FROM seq WHERE x<32768)
		INSERT INTO tcpping_results(agent_id,name,target,latency_ms,success,created_at) SELECT 1,'old-'||x,'example.com:80',10,1,1700000000 FROM seq`,
		`INSERT INTO tcpping_hourly(agent_id,name,hour_start,latency_avg,sample_count,success_count) SELECT 1,name,1700000000,10,1,1 FROM tcpping_results`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	queries := []func(context.Context) error{
		func(ctx context.Context) error { _, err := s.GetMetricsWindowSampled(ctx, 1, 0, 0, 0); return err },
		func(ctx context.Context) error { _, err := s.GetMetricsWindowSampled(ctx, 1, 0, 0, 60); return err },
		func(ctx context.Context) error { _, err := s.GetMetricsHourly(ctx, 1, 0, 0); return err },
		func(ctx context.Context) error {
			_, err := s.GetTCPingResultsWindowSampled(ctx, 1, 0, 0, 0)
			return err
		},
		func(ctx context.Context) error {
			_, err := s.GetTCPingResultsWindowSampled(ctx, 1, 0, 0, 60)
			return err
		},
		func(ctx context.Context) error { _, err := s.GetTCPingHourly(ctx, 1, 0, 0); return err },
	}
	for i, query := range queries {
		if err := query(context.Background()); !errors.Is(err, errHistoryTooLarge) {
			t.Fatalf("query %d: expected row budget error, got %v", i, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := query(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("query %d ignored canceled context: %v", i, err)
		}
	}
	if w := historyGET(t, h, "tcpping", "since=1699999999&until=1700000001"); w.Code != 422 {
		t.Fatalf("too many historical series silently truncated: %d %s", w.Code, w.Body.String())
	}
}

func TestHistoryCanceledQueriesReleaseConcurrencySlots(t *testing.T) {
	h, store := newTestHandler(t)
	store.db.SetMaxOpenConns(1)
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, cap(h.historySlots))
	for i := 0; i < cap(h.historySlots); i++ {
		kind := []string{"metrics", "tcpping"}[i%2]
		req := httptest.NewRequest(http.MethodGet, "/api/agents/1/"+kind+"?range=48h", nil).WithContext(ctx)
		go func() {
			h.handleAgentDetail(httptest.NewRecorder(), req)
			done <- struct{}{}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for store.db.Stats().WaitCount < int64(cap(h.historySlots)) {
		if time.Now().After(deadline) {
			t.Fatal("queries did not enter database wait")
		}
		time.Sleep(time.Millisecond)
	}
	if w := historyGET(t, h, "metrics", "range=24h"); w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("history queries queued without bound: %d %v", w.Code, w.Header())
	}
	cancel()
	for i := 0; i < cap(h.historySlots); i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("canceled history query is still waiting for the database")
		}
	}
	if len(h.historySlots) != 0 {
		t.Fatal("canceled queries leaked history slots")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if w := historyGET(t, h, "metrics", "range=24h"); w.Code != 200 {
		t.Fatalf("history did not recover: %d %s", w.Code, w.Body.String())
	}
}

func TestHistoryCancelsRunningSQLiteAggregation(t *testing.T) {
	_, store := newTestHandler(t)
	if err := store.InsertMetric(&MetricRow{AgentID: 1, CPUUsage: 10}, 1700000000); err != nil {
		t.Fatal(err)
	}
	// An intentionally expensive view exercises cancellation while SQLite is
	// executing an aggregate, rather than only cancellation in the pool queue.
	if _, err := store.db.Exec(`ALTER TABLE metrics RENAME TO metric_seed;
		CREATE VIEW metrics AS WITH RECURSIVE seq(x) AS (SELECT 0 UNION ALL SELECT x+1 FROM seq WHERE x<100000000)
		SELECT m.* FROM metric_seed m CROSS JOIN seq`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := store.GetMetricsWindowSampled(ctx, 1, 0, 0, 60); done <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for store.db.Stats().InUse == 0 {
		if time.Now().After(deadline) {
			t.Fatal("aggregate did not start")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("running aggregate ignored cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SQLite aggregation did not stop on cancellation")
	}
	var result int
	if err := store.db.QueryRow("SELECT 1").Scan(&result); err != nil || result != 1 {
		t.Fatalf("connection unusable after interruption: %d / %v", result, err)
	}
}
