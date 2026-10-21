package main

import (
	"context"
	"fmt"
	"time"

	agentpkg "needle/internal/agent"
	"needle/internal/agent/collector"
)

// A batch takes at most the existing five-second TCP timeout. At the minimum
// one-second history interval at most eight batches may overlap (including
// scheduler jitter). HTTP cannot delay when a batch is launched.
func newProbeSampler(targets []collector.TCPingTarget) func(context.Context, *agentpkg.ReportData) (<-chan struct{}, error) {
	slots := make(chan struct{}, 8)
	return func(ctx context.Context, data *agentpkg.ReportData) (<-chan struct{}, error) {
		select {
		case slots <- struct{}{}:
		default:
			return nil, fmt.Errorf("TCP Ping batch capacity reached")
		}
		ready := make(chan struct{})
		go func() {
			defer func() { <-slots; close(ready) }()
			if ctx.Err() != nil {
				return
			}
			data.TCPingCreatedAt = time.Now().Unix()
			data.TCPing = collector.TCPing(targets)
		}()
		return ready, nil
	}
}
