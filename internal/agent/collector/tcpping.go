package collector

import (
	"errors"
	"log"
	"net"
	"sync"
	"syscall"
	"time"

	"needle/internal/logging"
)

type TCPingTarget struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
}

type TCPingResult struct {
	Name      string  `json:"name"`
	Target    string  `json:"target"`
	LatencyMs float64 `json:"latency_ms"`
	Success   bool    `json:"success"`
}

var probeLogs = logging.NewTracker("tcpping", log.Printf)

func TCPing(targets []TCPingTarget) []TCPingResult {
	return tcping(targets, net.DialTimeout, probeLogs)
}

func tcping(targets []TCPingTarget, dial func(string, string, time.Duration) (net.Conn, error), logs *logging.Tracker) []TCPingResult {
	results := make([]TCPingResult, len(targets))
	var wg sync.WaitGroup

	for i, t := range targets {
		wg.Add(1)
		go func(idx int, target TCPingTarget) {
			defer wg.Done()
			start := time.Now()
			conn, err := dial("tcp", target.Target, 5*time.Second)
			if err != nil {
				results[idx] = TCPingResult{
					Name:      target.Name,
					Target:    target.Target,
					LatencyMs: 0,
					Success:   false,
				}
			} else {
				conn.Close()
				latency := time.Since(start).Seconds() * 1000
				results[idx] = TCPingResult{
					Name:      target.Name,
					Target:    target.Target,
					LatencyMs: latency,
					Success:   true,
				}
			}
			// Log after measuring latency. Only permission failures need logs;
			// clearing that issue does not claim the target is reachable.
			var permissionErr error
			if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
				permissionErr = err
			}
			logs.Check(target.Name+"/"+target.Target, "WARN", permissionErr, "check", "permission", "target", target.Name)
		}(i, t)
	}

	wg.Wait()
	return results
}
