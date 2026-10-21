package main

import (
	"context"
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
		runSampling(ctx, ticks, nil, sample, sample, send, send, func(format string, _ ...any) {
			select {
			case logs <- format:
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
		if !strings.Contains(line, "queue full") {
			t.Fatal(line)
		}
	case <-time.After(time.Second):
		t.Fatal("queue overflow was silent")
	}
	close(release)
	for expected := int64(1); expected <= historyQueueSize+1; expected++ {
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
