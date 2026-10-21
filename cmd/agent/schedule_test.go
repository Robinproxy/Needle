package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	agentpkg "needle/internal/agent"
)

func TestHistoryQueueDoesNotUseLatestOnlyAndLogsOverflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	delivered := make(chan int64, historyQueueSize+2)
	logs := make(chan string, 4)
	var sequence atomic.Int64
	sample := func() *agentpkg.ReportData { return &agentpkg.ReportData{CreatedAt: sequence.Add(1)} }
	send := func(ctx context.Context, data *agentpkg.ReportData) error {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		delivered <- data.CreatedAt
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSampling(ctx, ticks, nil, sample, sample, send, send, func(format string, args ...any) {
			select {
			case logs <- fmt.Sprintf(format, args...):
			default:
			}
		})
	}()
	<-started
	for i := 0; i < historyQueueSize+10; i++ {
		ticks <- time.Now()
	}
	select {
	case line := <-logs:
		if !strings.Contains(line, `issue="sample_not_queued"`) || !strings.Contains(line, "queue full") {
			t.Fatal(line)
		}
	case <-time.After(time.Second):
		t.Fatal("queue overflow was silent")
	}
	close(release)
	for expected := int64(1); expected <= historyQueueSize; expected++ {
		select {
		case got := <-delivered:
			if got != expected {
				t.Fatalf("FIFO replaced sample %d with %d", expected, got)
			}
		case <-time.After(time.Second):
			t.Fatal("history queue did not drain")
		}
	}
	cancel()
	<-done
}

func TestReportFailuresRecoverWithoutLoggingHealthySamples(t *testing.T) {
	for _, channel := range []string{"history", "realtime"} {
		t.Run(channel, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			historyTicks, realtimeTicks := make(chan time.Time), make(chan time.Time)
			lines := make(chan string, 16)
			attempts := make(chan struct{}, 16)
			n := 0
			send := func(context.Context, *agentpkg.ReportData) error {
				n++
				attempts <- struct{}{}
				if n <= 2 {
					return errors.New("temporary failure")
				}
				return nil
			}
			otherSend := func(context.Context, *agentpkg.ReportData) error { return nil }
			historySend, realtimeSend := send, otherSend
			if channel == "realtime" {
				historySend, realtimeSend = otherSend, send
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				sample := func() *agentpkg.ReportData { return &agentpkg.ReportData{CreatedAt: time.Now().Unix()} }
				runSampling(ctx, historyTicks, realtimeTicks, sample, sample, historySend, realtimeSend,
					func(format string, args ...any) { lines <- fmt.Sprintf(format, args...) })
			}()
			ticks := historyTicks
			if channel == "realtime" {
				ticks = realtimeTicks
				ticks <- time.Now()
			}
			for i := 0; i < 3; i++ {
				select {
				case <-attempts:
				case <-time.After(time.Second):
					t.Fatal("report worker stalled")
				}
				ticks <- time.Now()
			}
			for _, event := range []string{"failed", "recovered"} {
				select {
				case line := <-lines:
					if !strings.Contains(line, "module="+channel+" event="+event) {
						t.Fatalf("unexpected transition: %s", line)
					}
					if event == "recovered" && !strings.Contains(line, "count=2 suppressed=1") {
						t.Fatalf("missing suppressed failure count: %s", line)
					}
				case <-time.After(time.Second):
					t.Fatal("missing failure or recovery message")
				}
			}
			cancel()
			<-done
			for len(lines) > 0 {
				line := <-lines
				if !strings.Contains(line, "event=shutdown_discard") {
					t.Fatalf("healthy reports produced logs: %s", line)
				}
			}
		})
	}
}

func TestOldServerFallbackDoesNotTreatAuthenticationAsUnsupported(t *testing.T) {
	for _, status := range []int{404, 405, 501, 401, 403} {
		t.Run(time.Duration(status).String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			historyTicks, realtimeTicks := make(chan time.Time), make(chan time.Time)
			attempts, full := make(chan struct{}, 4), make(chan struct{}, 4)
			sample := func() *agentpkg.ReportData { return &agentpkg.ReportData{} }
			done := make(chan struct{})
			go func() {
				defer close(done)
				runSampling(ctx, historyTicks, realtimeTicks, sample, sample,
					func(context.Context, *agentpkg.ReportData) error { full <- struct{}{}; return nil },
					func(context.Context, *agentpkg.ReportData) error {
						attempts <- struct{}{}
						return &agentpkg.HTTPStatusError{Status: status}
					}, func(string, ...any) {})
			}()
			<-full
			realtimeTicks <- time.Now()
			<-attempts
			realtimeTicks <- time.Now()
			historyTicks <- time.Now()
			<-full
			if status == 401 || status == 403 {
				select {
				case <-attempts:
				case <-time.After(time.Second):
					t.Fatal("authentication failure incorrectly disabled realtime")
				}
			} else {
				select {
				case <-attempts:
					t.Fatal("unsupported interface retried")
				case <-time.After(10 * time.Millisecond):
				}
			}
			cancel()
			<-done
		})
	}
}

func TestRealtimeConfigurationCompatibility(t *testing.T) {
	for _, value := range []string{"5", "10", "7", "0"} {
		var cfg Config
		if err := yaml.Unmarshal([]byte("interval: 30\nrealtime_interval: "+value), &cfg); err != nil {
			t.Fatal(err)
		}
		if err := cfg.validateProbesAndInterval(); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"-1", "31", "1.5", "'5'", "null", "true", "99999999999999999999999999"} {
		var cfg Config
		err := yaml.Unmarshal([]byte("interval: 30\nrealtime_interval: "+value), &cfg)
		if err == nil {
			err = cfg.validateProbesAndInterval()
		}
		if err == nil {
			t.Fatalf("accepted invalid realtime interval %s", value)
		}
	}
	var old Config
	if err := yaml.Unmarshal([]byte("interval: 60"), &old); err != nil || old.RealtimeInterval != 0 {
		t.Fatal("old config enabled realtime", err)
	}
}

func TestSeparateSchedulesDoNotWaitForHTTPOrPing(t *testing.T) {
	for _, pair := range [][2]int{{5, 30}, {10, 30}, {5, 60}, {7, 30}, {0, 30}} {
		t.Run(time.Duration(pair[0]*int(time.Second)).String()+"/"+time.Duration(pair[1]*int(time.Second)).String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			historyTicks := make(chan time.Time)
			realtimeTicks := make(chan time.Time)
			var realtimeChannel <-chan time.Time = realtimeTicks
			if pair[0] == 0 {
				realtimeChannel = nil
			}
			var historySamples, realtimeSamples atomic.Int64
			hSample := func() *agentpkg.ReportData { historySamples.Add(1); return &agentpkg.ReportData{} }
			rSample := func() *agentpkg.ReportData { realtimeSamples.Add(1); return &agentpkg.ReportData{} }
			started := make(chan struct{}, 1)
			blocked := func(ctx context.Context, _ *agentpkg.ReportData) error {
				select {
				case started <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return ctx.Err()
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				runSampling(ctx, historyTicks, realtimeChannel, hSample, rSample, blocked, blocked, func(string, ...any) {})
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("initial history worker not started")
			}
			for second := 1; second <= 120; second++ {
				if second%pair[1] == 0 {
					historyTicks <- time.Unix(int64(second), 0)
				}
				if pair[0] > 0 && second%pair[0] == 0 {
					realtimeTicks <- time.Unix(int64(second), 0)
				}
			}
			// A final tick handshake ensures all earlier samples were processed.
			historyTicks <- time.Unix(121, 0)
			cancel()
			<-done
			if got := historySamples.Load(); got != int64(2+120/pair[1]) {
				t.Fatalf("history samples=%d", got)
			}
			wantReal := int64(0)
			if pair[0] > 0 {
				wantReal = int64(1 + 120/pair[0])
			}
			if got := realtimeSamples.Load(); got != wantReal {
				t.Fatalf("realtime samples=%d want=%d", got, wantReal)
			}
		})
	}
}

func TestProbeSchedulingDoesNotWaitForHistoricalTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	historyTicks, realtimeTicks := make(chan time.Time), make(chan time.Time)
	probeReady := make(chan struct{}) // Slow probe batch; sender waits, sampler doesn't.
	var probes atomic.Int64
	realtimeSent := make(chan struct{}, 1)
	sample := func() *agentpkg.ReportData { return &agentpkg.ReportData{} }
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSampling(ctx, historyTicks, realtimeTicks, sample, sample,
			func(context.Context, *agentpkg.ReportData) error {
				t.Error("history sent before probe finished")
				return nil
			},
			func(context.Context, *agentpkg.ReportData) error { realtimeSent <- struct{}{}; return nil }, func(string, ...any) {},
			func(context.Context, *agentpkg.ReportData) (<-chan struct{}, error) {
				probes.Add(1)
				return probeReady, nil
			})
	}()
	historyTicks <- time.Now()
	historyTicks <- time.Now()
	realtimeTicks <- time.Now()
	select {
	case <-realtimeSent:
	case <-time.After(time.Second):
		t.Fatal("probes blocked realtime transport")
	}
	if probes.Load() != 3 {
		t.Fatal("probes were deferred until transport", probes.Load())
	}
	cancel()
	<-done
}

func TestHistoryBudgetIncludesInflightAndBoundsBytes(t *testing.T) {
	b := &historyBudget{}
	if !b.reserve(100, 1) || b.reserve(1, 1) {
		t.Fatal("in-flight capacity not enforced")
	}
	b.release(100)
	if b.reserve(historyReportByteLimit+1, 64) {
		t.Fatal("oversized report accepted")
	}
	for range 8 {
		if !b.reserve(historyReportByteLimit, 64) {
			t.Fatal("valid reservation refused")
		}
	}
	if b.reserve(1, 64) {
		t.Fatal("total byte budget exceeded")
	}
}
func TestHistoryAdvancedConfigValidation(t *testing.T) {
	for _, text := range []string{"history_queue_capacity: -1", "history_queue_capacity: 1025", "history_retry_max_age_seconds: -1", "history_retry_max_age_seconds: 86401", "history_queue_capacity: 1.5", "history_retry_max_age_seconds: '20'"} {
		var c Config
		err := yaml.Unmarshal([]byte(text), &c)
		if err == nil {
			err = c.validateProbesAndInterval()
		}
		if err == nil {
			t.Fatal(text)
		}
	}
	var old Config
	if old.queueCapacity() != 64 || old.retryMaxAge() != 1800 {
		t.Fatal("advanced defaults changed")
	}
}
