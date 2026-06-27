package collector

import (
	"net"
	"time"
)

type TCPingTarget struct {
	Name     string `yaml:"name"`
	Target   string `yaml:"target"`
	Interval int    `yaml:"interval"`
}

type TCPingResult struct {
	Name      string  `json:"name"`
	Target    string  `json:"target"`
	LatencyMs float64 `json:"latency_ms"`
	Success   bool    `json:"success"`
}

func TCPing(targets []TCPingTarget) []TCPingResult {
	results := make([]TCPingResult, 0, len(targets))
	for _, t := range targets {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", t.Target, 5*time.Second)
		if err != nil {
			results = append(results, TCPingResult{
				Name:    t.Name,
				Target:  t.Target,
				LatencyMs: 0,
				Success: false,
			})
			continue
		}
		conn.Close()
		latency := time.Since(start).Seconds() * 1000
		results = append(results, TCPingResult{
			Name:      t.Name,
			Target:    t.Target,
			LatencyMs: latency,
			Success:   true,
		})
	}
	return results
}
