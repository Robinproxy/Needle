package collector

import (
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

type NetworkStats struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type NetworkCollector struct {
	prevUp   uint64
	prevDown uint64
	prevTime time.Time
	first    bool
}

func NewNetworkCollector() *NetworkCollector {
	return &NetworkCollector{first: true}
}

func (nc *NetworkCollector) Collect() (*NetworkStats, error) {
	counters, err := net.IOCounters(false)
	if err != nil {
		return nil, err
	}

	now := time.Now()

	if nc.first {
		nc.prevUp = counters[0].BytesSent
		nc.prevDown = counters[0].BytesRecv
		nc.prevTime = now
		nc.first = false
		return &NetworkStats{}, nil
	}

	elapsed := now.Sub(nc.prevTime).Seconds()
	var up, down int64
	if elapsed > 0 {
		up = int64(float64(counters[0].BytesSent-nc.prevUp) / elapsed)
		down = int64(float64(counters[0].BytesRecv-nc.prevDown) / elapsed)
	}

	nc.prevUp = counters[0].BytesSent
	nc.prevDown = counters[0].BytesRecv
	nc.prevTime = now

	if up < 0 {
		up = 0
	}
	if down < 0 {
		down = 0
	}

	return &NetworkStats{Up: up, Down: down}, nil
}
