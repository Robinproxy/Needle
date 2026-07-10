package collector

import (
	"net"
	"sync"
	"time"
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

func TCPing(targets []TCPingTarget) []TCPingResult {
	results := make([]TCPingResult, len(targets))
	var wg sync.WaitGroup

	for i, t := range targets {
		wg.Add(1)
		go func(idx int, target TCPingTarget) {
			defer wg.Done()
			start := time.Now()
			conn, err := net.DialTimeout("tcp", target.Target, 5*time.Second)
			if err != nil {
				results[idx] = TCPingResult{
					Name:      target.Name,
					Target:    target.Target,
					LatencyMs: 0,
					Success:   false,
				}
				return
			}
			conn.Close()
			latency := time.Since(start).Seconds() * 1000
			results[idx] = TCPingResult{
				Name:      target.Name,
				Target:    target.Target,
				LatencyMs: latency,
				Success:   true,
			}
		}(i, t)
	}

	wg.Wait()
	return results
}