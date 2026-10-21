// needle-load exercises only a disposable child Server and synthetic local
// reports. It never runs Agents, probes remote targets or opens a real DB.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
	"needle/internal/agent"
	"needle/internal/agent/collector"
	"needle/internal/server"
)

var SourceID = "unrecorded"
var nodes = flag.Int("nodes", 100, "synthetic nodes")
var clients = flag.Int("sse", 1, "SSE clients (default server maximum is 32)")
var duration = flag.Duration("duration", time.Minute, "measurement duration; use 30m+ for stability runs")
var pattern = flag.String("pattern", "spread", "spread, burst or recovery")
var faults = flag.Bool("faults", false, "isolated DB busy, lost ACK and token revocation")
var slow = flag.Int("slow-clients", 0, "extra intentionally slow event clients")
var child = flag.String("child-db", "", "internal disposable child database")

func main() {
	flag.Parse()
	if *child != "" {
		serve()
		return
	}
	if *nodes < 1 || *nodes > 10000 || *clients < 0 || *slow < 0 {
		panic("invalid bounds")
	}
	if err := load(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func emit(data any) { _ = json.NewEncoder(os.Stdout).Encode(data) }
func serve() {
	if filepath.Base(*child) != "load.db" || !strings.HasPrefix(filepath.Base(filepath.Dir(*child)), "needle-load-") {
		panic("disposable database path required")
	}
	if _, err := os.Lstat(*child); !os.IsNotExist(err) {
		panic("refusing an existing database")
	}

	store, err := server.NewStoreCLI(*child)
	if err != nil {
		panic(err)
	}
	defer store.Close()
	for i := 0; i < *nodes; i++ {
		host := fmt.Sprintf("load-%04d", i)
		token := fmt.Sprintf("load-only-%04d", i)
		if err = store.AllowToken(token); err != nil {
			panic(err)
		}
		if err = store.BindToken(token, host); err != nil {
			panic(err)
		}
		_, err = store.SaveReport(&server.ReportWrite{Hostname: host, BillingPeriod: "forever", Interval: 30, RealtimeInterval: 5, CreatedAt: time.Now().Unix(), Metric: &server.MetricRow{Uptime: 100000, TotalSent: 1000, TotalRecv: 2000}})
		if err != nil {
			panic(err)
		}
	}
	store.PurgeOldData()
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			store.PurgeOldData()
		}
	}()
	h := server.NewHandler(store)
	mux := http.NewServeMux()
	h.Register(mux)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	srv := &http.Server{Handler: server.SecurityHeaders(mux), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}
	go srv.Serve(listener)
	proc, _ := process.NewProcess(int32(os.Getpid()))
	proc.CPUPercent()
	emit(map[string]any{"url": "http://" + listener.Addr().String(), "baseline": store.Diagnostics()})
	go func() {
		scan := bufio.NewScanner(os.Stdin)
		for scan.Scan() {
			switch scan.Text() {
			case "revoke":
				store.RevokeToken("load-only-0000")
			case "busy":
				go func() {
					db, err := sql.Open("sqlite3", *child+"?_busy_timeout=5000")
					if err != nil {
						return
					}
					defer db.Close()
					tx, err := db.Begin()
					if err != nil {
						return
					}
					defer tx.Rollback()
					if _, err = tx.Exec("UPDATE agents SET region=region"); err == nil {
						time.Sleep(4 * time.Second)
					}
				}()
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		d := store.Diagnostics()
		d["at"] = time.Now().Format(time.RFC3339Nano)
		d["goroutines"] = runtime.NumGoroutine()
		if m, e := proc.MemoryInfo(); e == nil {
			d["rss_bytes"] = m.RSS
		}
		if c, e := proc.CPUPercent(); e == nil {
			d["cpu_percent_one_core_100"] = c
		}
		if f, e := proc.NumFDs(); e == nil {
			d["file_descriptors"] = f
		}
		emit(d)
	}
}

type metrics struct {
	sync.Mutex
	latency                             map[string][]float64
	status                              map[string]map[int]int
	attempts                            int64
	sseDelay                            []float64
	connections, disconnects, endClosed int
	created, acked, dropped, retries    atomic.Int64
	queueMax                            atomic.Int64
	sampleWaitMS                        float64
}

func (m *metrics) record(path string, status int, d time.Duration) {
	m.Lock()
	defer m.Unlock()
	m.latency[path] = append(m.latency[path], float64(d.Microseconds())/1000)
	if m.status[path] == nil {
		m.status[path] = map[int]int{}
	}
	m.status[path][status]++
	m.attempts++
}

type measuredTransport struct {
	base    http.RoundTripper
	m       *metrics
	started time.Time
	ackLost sync.Map
}

func (t *measuredTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	if *pattern == "recovery" && path == "/api/report" && time.Since(t.started) > 5*time.Second && time.Since(t.started) < 15*time.Second {
		t.m.record(path, 0, 0)
		return nil, io.ErrUnexpectedEOF
	}
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	code := 0
	if resp != nil {
		code = resp.StatusCode
	}
	if path == "/api/report" || path == "/api/realtime" {
		t.m.record(path, code, time.Since(start))

	}
	if *faults && path == "/api/report" && code == 200 {
		if _, seen := t.ackLost.LoadOrStore(req.Header.Get("Authorization"), true); !seen {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, io.ErrUnexpectedEOF
		}
	}
	return resp, err
}
func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	return v[int(float64(len(v)-1)*p)]
}
func distribution(v []float64) map[string]any {
	return map[string]any{"count": len(v), "p50_ms": percentile(v, .5), "p95_ms": percentile(v, .95), "p99_ms": percentile(v, .99)}
}
func load() error {
	dir, err := os.MkdirTemp("", "needle-load-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "-child-db", filepath.Join(dir, "load.db"), "-nodes", fmt.Sprint(*nodes))
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	logs, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		return err
	}
	defer logs.Close()
	cmd.Stderr = logs
	if err = cmd.Start(); err != nil {
		return err
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 4096), 4<<20)
	if !scan.Scan() {
		logs.Sync()
		b, _ := os.ReadFile(filepath.Join(dir, "server.log"))
		return fmt.Errorf("child failed: %s", string(b))
	}
	var startup struct {
		URL      string         `json:"url"`
		Baseline map[string]any `json:"baseline"`
	}
	if err = json.Unmarshal(scan.Bytes(), &startup); err != nil {
		return err
	}
	var sampleMu sync.Mutex
	var samples []map[string]any
	doneStats := make(chan struct{})
	go func() {
		defer close(doneStats)
		for scan.Scan() {
			var s map[string]any
			if json.Unmarshal(scan.Bytes(), &s) == nil {
				sampleMu.Lock()
				samples = append(samples, s)
				sampleMu.Unlock()
			}
		}
	}()
	infoCPU, _ := cpu.Info()
	infoMem, _ := mem.VirtualMemory()
	hardware := map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version(), "logical_cpus": runtime.NumCPU()}
	if len(infoCPU) > 0 {
		hardware["cpu"] = infoCPU[0].ModelName
	}
	if infoMem != nil {
		hardware["memory_bytes"] = infoMem.Total
	}
	m := &metrics{latency: map[string][]float64{}, status: map[string]map[int]int{}}
	started := time.Now()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 4096
	transport.MaxIdleConnsPerHost = 2048
	measured := &measuredTransport{base: transport, m: m, started: started}
	httpClient := &http.Client{Transport: measured, Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	var workers sync.WaitGroup
	var sentAt sync.Map
	for client := 0; client < *clients+*slow; client++ {
		workers.Add(1)
		go func(slow bool) {
			defer workers.Done()
			last := map[int64]int64{}
			for ctx.Err() == nil {
				req, _ := http.NewRequestWithContext(ctx, "GET", startup.URL+"/api/events", nil)
				resp, e := (&http.Client{Transport: transport}).Do(req)
				if e != nil {
					break
				}
				m.Lock()
				if resp.StatusCode != 200 {
					if m.status["sse_rejected"] == nil {
						m.status["sse_rejected"] = make(map[int]int)
					}
					m.status["sse_rejected"][resp.StatusCode]++
					m.Unlock()
					resp.Body.Close()
					return
				}
				m.connections++
				m.Unlock()
				reader := bufio.NewScanner(resp.Body)
				reader.Buffer(make([]byte, 4096), 4<<20)
				for reader.Scan() {
					if slow {
						select {
						case <-time.After(100 * time.Millisecond):
						case <-ctx.Done():
						}
						continue
					}
					line := reader.Text()
					if strings.HasPrefix(line, "data: ") {
						var event struct {
							Kind string `json:"kind"`
							ID   int64  `json:"id"`
							Node *struct {
								Metric *server.MetricRow `json:"latest_metric"`
							} `json:"node"`
						}
						if json.Unmarshal([]byte(line[6:]), &event) == nil && event.Node != nil && event.Node.Metric != nil && last[event.ID] != event.Node.Metric.Uptime {
							metric := event.Node.Metric
							last[event.ID] = metric.Uptime
							if ts, ok := sentAt.Load(fmt.Sprintf("%d:%d", event.ID, metric.Uptime)); ok {
								m.Lock()
								m.sseDelay = append(m.sseDelay, float64(time.Since(ts.(time.Time)).Microseconds())/1000)
								m.Unlock()
							}
						}
					}
				}
				resp.Body.Close()
				m.Lock()
				if ctx.Err() == nil {
					m.disconnects++
				} else {
					m.endClosed++
				}
				m.Unlock()
				if ctx.Err() != nil {
					return
				}
				select {
				case <-time.After(time.Second):
				case <-ctx.Done():
					return
				}
			}
		}(client >= *clients)
	}
	for node := 0; node < *nodes; node++ {
		for _, history := range []bool{false, true} {
			workers.Add(1)
			go func(i int, history bool) {
				defer workers.Done()
				step := 5 * time.Second
				if history {
					step = 30 * time.Second
				}
				phase := time.Duration(i) * step / time.Duration(*nodes)
				if *pattern == "burst" {
					phase = 0
				}
				timer := time.NewTimer(phase)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}

				// Transport measurements distinguish individual Server attempts from queue/retry wait.
				previousKey := ""
				for seq := 1; ctx.Err() == nil; seq++ {
					sampleTime := time.Now()
					uptime := sampleTime.UnixNano()
					id := int64(i + 1)
					if previousKey != "" {
						sentAt.Delete(previousKey)
					}
					previousKey = fmt.Sprintf("%d:%d", id, uptime)
					sentAt.Store(previousKey, sampleTime)
					data := &agent.ReportData{Hostname: fmt.Sprintf("load-%04d", i), ReportID: fmt.Sprintf("%032x:%d", i+1, seq), CreatedAt: sampleTime.Unix(), Interval: 30, RealtimeInterval: 5, BillingPeriod: "forever", Uptime: uint64(uptime), CPU: &collector.CPUStats{Percent: float64(seq % 100), Cores: 4}, Network: &collector.NetworkStats{TotalSent: int64(1000 + seq*100), TotalRecv: int64(2000 + seq*200)}}
					if history {
						for p := 0; p < 3; p++ {
							data.TCPing = append(data.TCPing, collector.TCPingResult{Name: fmt.Sprintf("synthetic-%d", p), Target: "127.0.0.1:9", Success: true, LatencyMs: float64(p + 1)})
						}
					}
					// One bounded report per simulated sender; the separate producer/queue is
					// covered by Agent unit tests. This tool measures Server ingress, not Agent CPU.
					body, _ := json.Marshal(data)
					path := "/api/report"
					if !history {
						path = "/api/realtime"
						body, _ = json.Marshal(agent.RealtimeFromReport(data))
					}
					if history {
						m.created.Add(1)
					}
					req, _ := http.NewRequestWithContext(ctx, "POST", startup.URL+path, strings.NewReader(string(body)))
					req.Header.Set("Authorization", fmt.Sprintf("Bearer load-only-%04d", i))
					req.Header.Set("Content-Type", "application/json")
					// Retry identified history only after a protocol capability check.
					if history {
						// Real Agent reliable transport is tested separately; preserve frozen request bytes here.
						capReq, _ := http.NewRequestWithContext(ctx, "GET", startup.URL+"/api/report-capabilities", nil)
						capReq.Header = req.Header.Clone()
						capResp, e := httpClient.Do(capReq)
						confirmed := false
						if e == nil {
							var caps map[string]any
							json.NewDecoder(capResp.Body).Decode(&caps)
							capResp.Body.Close()
							confirmed = caps["protocol"] == agent.ReportProtocol
						}
						for attempt := 0; ; attempt++ {
							if attempt > 0 {
								m.retries.Add(1)
								m.queueMax.Store(max(m.queueMax.Load(), 1))
								select {
								case <-time.After(min(time.Duration(1<<min(attempt-1, 5))*time.Second, 30*time.Second)):
								case <-ctx.Done():
									break
								}
							}
							r := req.Clone(ctx)
							r.Body = io.NopCloser(strings.NewReader(string(body)))
							resp, e := httpClient.Do(r)
							code := 0
							if e == nil {
								code = resp.StatusCode
								io.Copy(io.Discard, resp.Body)
								resp.Body.Close()
							}
							if e == nil && code == 200 {
								m.acked.Add(1)
								break
							}
							if ctx.Err() != nil || !confirmed || time.Since(sampleTime) > 30*time.Minute || (e == nil && code != 408 && code != 429 && code != 500 && code != 502 && code != 503 && code != 504) {
								m.dropped.Add(1)
								break
							}
						}
						m.Lock()
						m.sampleWaitMS = max(m.sampleWaitMS, float64(time.Since(sampleTime).Milliseconds()))
						m.Unlock()
					} else {
						if resp, e := httpClient.Do(req); e == nil {
							io.Copy(io.Discard, resp.Body)
							resp.Body.Close()
						}
					}

					target := sampleTime.Add(step)
					select {
					case <-ctx.Done():
						return
					case <-time.After(max(time.Duration(0), time.Until(target))):
					}
				}
			}(node, history)
		}
	}
	if *faults {
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case <-time.After(10 * time.Second):
				fmt.Fprintln(stdin, "busy")
			case <-ctx.Done():
				return
			}
			select {
			case <-time.After(10 * time.Second):
				fmt.Fprintln(stdin, "revoke")
			case <-ctx.Done():
			}
		}()
	}
	<-ctx.Done()
	workers.Wait()
	time.Sleep(1100 * time.Millisecond)
	cmd.Process.Kill()
	cmd.Wait()
	<-doneStats
	m.Lock()
	defer m.Unlock()
	sampleMu.Lock()
	defer sampleMu.Unlock()
	out := map[string]any{"source_id": SourceID, "hardware": hardware, "nodes": *nodes, "sse_clients": *clients, "slow_clients": *slow, "duration": duration.String(), "pattern": *pattern, "faults": *faults, "intervals_seconds": map[string]int{"realtime": 5, "history": 30}, "status": m.status, "history_created": m.created.Load(), "history_acked": m.acked.Load(), "history_failed_or_cancelled": m.dropped.Load(), "retry_attempts": m.retries.Load(), "history_single_sender_pending_max": m.queueMax.Load(), "max_client_sample_to_ack_ms": m.sampleWaitMS, "sse_connections": m.connections, "sse_disconnects": m.disconnects, "sse_closed_at_test_end": m.endClosed, "sse_event_delay": distribution(m.sseDelay), "latency": map[string]any{"history_attempt": distribution(m.latency["/api/report"]), "realtime_attempt": distribution(m.latency["/api/realtime"])}, "server_baseline": startup.Baseline, "server_samples": samples, "limitations": []string{"SSE timing ends at the synthetic reader, not browser paint", "client loops are closed-loop ingress tests, not Agent queue/CPU benchmarks", "SQLite profile time includes execution; exact lock-wait time unavailable", "child metrics exclude load-generator process; file descriptors may be unavailable on this OS"}}
	emit(out)
	return nil
}
