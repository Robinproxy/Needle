package main

import (
	"context"
	"errors"
	"sync"
	"time"

	agentpkg "needle/internal/agent"
)

const historyQueueSize = 64

type historyJob struct {
	data  *agentpkg.ReportData
	ready <-chan struct{}
}

// Two bounded workers keep HTTP and probes off the sampling loop. Historical
// samples are FIFO, never replaced by a newer sample. With no disk spool an
// indefinitely stalled transport cannot retain unlimited history: overflow is
// explicitly logged rather than blocking realtime sampling or growing memory.
func runSampling(ctx context.Context, historyTicks, realtimeTicks <-chan time.Time,
	historySample, realtimeSample func() *agentpkg.ReportData,
	sendHistory, sendRealtime func(context.Context, *agentpkg.ReportData) error,
	logf func(string, ...any), prepare ...func(context.Context, *agentpkg.ReportData) (<-chan struct{}, error)) {
	history := make(chan historyJob, historyQueueSize)
	realtime := make(chan *agentpkg.ReportData, 1)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-history:
				if ctx.Err() != nil {
					return
				}
				if job.ready != nil {
					select {
					case <-job.ready:
					case <-ctx.Done():
						return
					}
				}
				if err := sendHistory(ctx, job.data); err != nil && ctx.Err() == nil {
					logf("history report failed: %v", err)
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		unsupported := false
		lastError := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case data := <-realtime:
				if unsupported || ctx.Err() != nil {
					continue
				}
				requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := sendRealtime(requestCtx, data)
				cancel()
				var status *agentpkg.HTTPStatusError
				if errors.As(err, &status) && (status.Status == 404 || status.Status == 405 || status.Status == 501) {
					unsupported = true
					logf("Server does not support realtime reports; paused until Agent restart. Full history reports continue; upgrade Server first.")
				} else if err != nil && ctx.Err() == nil && time.Since(lastError) >= time.Minute {
					logf("realtime report failed (further errors limited to once per minute): %v", err)
					lastError = time.Now()
				}
			}
		}
	}()
	defer workers.Wait()
	makeJob := func(data *agentpkg.ReportData) (historyJob, error) {
		job := historyJob{data: data}
		if len(prepare) > 0 {
			ready, err := prepare[0](ctx, data)
			job.ready = ready
			return job, err
		}
		return job, nil
	}
	first, err := makeJob(historySample())
	if err == nil {
		history <- first
	} else {
		logf("startup probe scheduling failed: %v", err)
	}
	if realtimeTicks != nil {
		realtimeSample()
	} // Seed its independent CPU/NIC window.
	dropped := 0
	for {
		select {
		case <-ctx.Done():
			if len(history) > 0 {
				logf("shutdown: %d unsent historical samples remain in the bounded memory queue", len(history))
			}
			return
		case <-historyTicks:
			data := historySample()
			job, err := makeJob(data)
			if err != nil {
				logf("history probe scheduling failed; sample not queued: %v", err)
				continue
			}
			select {
			case history <- job:
				if dropped > 0 {
					logf("history queue recovered; %d samples could not be queued", dropped)
					dropped = 0
				}
			default:
				dropped++
				if dropped == 1 {
					logf("history queue full (%d samples); new historical samples cannot be retained until transport recovers", historyQueueSize)
				}
			}
		case <-realtimeTicks:
			data := realtimeSample()
			select {
			case realtime <- data:
			default:
				select {
				case <-realtime:
				default:
				}
				select {
				case realtime <- data:
				default:
				}
			}
		}
	}
}
