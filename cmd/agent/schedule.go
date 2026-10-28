package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	agentpkg "needle/internal/agent"
	"needle/internal/logging"
)

const historyQueueSize = 64

const historyQueueByteLimit = 8 << 20
const historyReportByteLimit = 1 << 20

type historyPolicy struct{ Capacity int }
type historyBudget struct {
	sync.Mutex
	count, bytes int
}

func (b *historyBudget) reserve(size, capacity int) bool {
	b.Lock()
	defer b.Unlock()
	if size > historyReportByteLimit || b.count >= capacity || b.bytes+size > historyQueueByteLimit {
		return false
	}
	b.count++
	b.bytes += size
	return true
}
func (b *historyBudget) release(size int)    { b.Lock(); defer b.Unlock(); b.count--; b.bytes -= size }
func (b *historyBudget) pending() (int, int) { b.Lock(); defer b.Unlock(); return b.count, b.bytes }

type historyJob struct {
	bytes int
	data  *agentpkg.ReportData
	ready <-chan struct{}
}

// Two bounded workers keep HTTP and probes off the sampling loop. Historical
// samples are FIFO, never replaced by a newer sample. With no disk spool an
// indefinitely stalled transport cannot retain unlimited history: overflow is
// explicitly logged rather than blocking realtime sampling or growing memory.
func runSamplingWithPolicy(policy historyPolicy, ctx context.Context, historyTicks, realtimeTicks <-chan time.Time,
	historySample, realtimeSample func() *agentpkg.ReportData,
	sendHistory, sendRealtime func(context.Context, *agentpkg.ReportData) error,
	logf func(string, ...any), prepare ...func(context.Context, *agentpkg.ReportData) (<-chan struct{}, error)) {
	if policy.Capacity <= 0 {
		policy.Capacity = historyQueueSize
	}
	instanceBytes := make([]byte, 16)
	if _, err := rand.Read(instanceBytes); err != nil {
		logging.Write(logf, "ERROR", "agent", "report_instance_failed", "error", err)
		return
	}
	instance := hex.EncodeToString(instanceBytes)
	var sequence uint64
	budget := &historyBudget{}
	history := make(chan historyJob, policy.Capacity)
	realtime := make(chan *agentpkg.ReportData, 1)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		logs := logging.NewTracker("history", logf)
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
				err := sendHistory(ctx, job.data)
				if ctx.Err() == nil {
					pending, bytes := budget.pending()
					logs.Check("discarded_samples", "ERROR", err, "queued_including_inflight", pending, "reserved_bytes", bytes,
						"sample_age", time.Since(time.Unix(job.data.CreatedAt, 0)).Round(time.Second))
				}
				budget.release(job.bytes)
			}
		}
	}()
	go func() {
		defer workers.Done()
		unsupported := false
		logs := logging.NewTracker("realtime", logf)
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
					logging.Write(logf, "WARN", "realtime", "unsupported", "status", status.Status,
						"action", "paused until Agent restart; full history continues; upgrade Server first")
				} else if ctx.Err() == nil {
					logs.Check("send", "WARN", err)
				}
			}
		}
	}()
	defer workers.Wait()
	makeJob := func(data *agentpkg.ReportData) (historyJob, error) {
		sequence++
		data.ReportID = fmt.Sprintf("%s:%d", instance, sequence)
		encoded, err := json.Marshal(data)
		if err != nil {
			return historyJob{}, err
		}
		// Reserve room for the bounded (32 target) asynchronous probe batch before it
		// mutates the report. Capacity includes the in-flight/retrying head.
		size := len(encoded) + 32*1024
		if !budget.reserve(size, policy.Capacity) {
			return historyJob{}, fmt.Errorf("history queue full: count/byte bound reached")
		}
		job := historyJob{data: data, bytes: size}
		if len(prepare) > 0 {
			job.ready, err = prepare[0](ctx, data)
			if err != nil {
				budget.release(size)
				return historyJob{}, err
			}
		}
		return job, nil
	}
	queueLogs := logging.NewTracker("history.queue", logf)
	first, err := makeJob(historySample())
	if err == nil {
		history <- first
	} else {
		queueLogs.Check("sample_not_queued", "WARN", err)
	}
	if realtimeTicks != nil {
		realtimeSample()
	} // Seed its independent CPU/NIC window.
	for {
		select {
		case <-ctx.Done():
			if pending, bytes := budget.pending(); pending > 0 {
				logging.Write(logf, "WARN", "history.queue", "shutdown_discard", "unsent_inflight", pending, "reserved_bytes", bytes, "persisted", false)
			}
			return
		case <-historyTicks:
			data := historySample()
			job, err := makeJob(data)
			if err != nil {
				n, bytes := budget.pending()
				queueLogs.Check("sample_not_queued", "WARN", err, "queued_including_inflight", n, "reserved_bytes", bytes)
				continue
			}
			history <- job // Reservation guarantees a free FIFO slot.
			queueLogs.Check("sample_not_queued", "WARN", nil)
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
