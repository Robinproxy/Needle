package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

type reportPayload struct {
	Token    string       `json:"token"`
	Hostname string       `json:"hostname"`
	Region   string       `json:"region"`
	CPU      *cpuStats    `json:"cpu"`
	Memory   *memStats    `json:"memory"`
	Disk     *diskStats   `json:"disk"`
	Network  *netStats    `json:"network"`
	Load     *loadStats   `json:"load"`
	Uptime   uint64       `json:"uptime"`
	TCPing   []tcpingRec  `json:"tcpping"`
}

type cpuStats struct {
	Percent float64 `json:"percent"`
}

type memStats struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type diskStats struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type netStats struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type loadStats struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

type tcpingRec struct {
	Name      string  `json:"name"`
	Target    string  `json:"target"`
	LatencyMs float64 `json:"latency_ms"`
	Success   bool    `json:"success"`
}

var agents = []struct {
	hostname string
	region   string
	country  string
}{
	{"demo-server", "CN", "China"},
	{"db-server", "CN", "China"},
	{"app-server", "US", "United States"},
}

var tcppingTargets = []struct {
	name   string
	target string
}{
	{"CMv4", "sh-cm-v4.ip.zstaticcdn.com:80"},
	{"CMv6", "sh-cm-v6.ip.zstaticcdn.com:80"},
	{"CUv4", "sh-cu-v4.ip.zstaticcdn.com:80"},
	{"CUv6", "sh-cu-v6.ip.zstaticcdn.com:80"},
	{"CTv4", "sh-ct-v4.ip.zstaticcdn.com:80"},
	{"CTv6", "sh-ct-v6.ip.zstaticcdn.com:80"},
}

func randRange(min, max float64) float64 {
	return min + rand.Float64()*(max-min)
}

func makeReport(hostname, region string, daysAgo int) reportPayload {
	now := time.Now().Add(-time.Duration(daysAgo) * 24 * time.Hour)
	load := randRange(0.1, 4.0)

	r := reportPayload{
		Token:    "your-token",
		Hostname: hostname,
		Region:   region,
		CPU:      &cpuStats{Percent: randRange(5, 85)},
		Memory:   &memStats{Total: 8 << 30, Used: uint64(randRange(1, 6) * (1 << 30))},
		Disk:     &diskStats{Total: 100 << 30, Used: uint64(randRange(10, 60) * (1 << 30))},
		Network:  &netStats{Up: int64(randRange(10, 500) * 1000), Down: int64(randRange(50, 900) * 1000)},
		Load:     &loadStats{Load1: load, Load5: load * 0.7, Load15: load * 0.5},
		Uptime:   uint64(rand.Intn(30)) * 86400,
	}

	for _, t := range tcppingTargets {
		success := rand.Float64() > 0.05
		var lat float64
		if success {
			if t.name[2] == '6' {
				lat = randRange(10, 80)
			} else {
				lat = randRange(5, 60)
			}
		}
		r.TCPing = append(r.TCPing, tcpingRec{
			Name:      t.name,
			Target:    t.target,
			LatencyMs: lat,
			Success:   success,
		})
	}

	_ = now
	return r
}

func sendReport(url string, p reportPayload) error {
	body, _ := json.Marshal(p)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func main() {
	rand.Seed(time.Now().UnixNano())
	baseURL := "http://127.0.0.1:8008/api/report"

	total := 0
	for _, a := range agents {
		for day := 7; day >= 0; day-- {
			for hour := 0; hour < 24; hour++ {
				_ = hour
				p := makeReport(a.hostname, a.region, day)
				if err := sendReport(baseURL, p); err != nil {
					fmt.Printf("ERROR [%s] day=%d: %v\n", a.hostname, day, err)
				} else {
					total++
				}
			}
		}
	}

	fmt.Printf("Done. %d reports sent.\n", total)
}
