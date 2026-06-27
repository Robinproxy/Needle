package collector

import (
	"github.com/shirou/gopsutil/v4/cpu"
)

type CPUStats struct {
	Percent float64 `json:"percent"`
}

func CollectCPU() (*CPUStats, error) {
	p, err := cpu.Percent(0, false)
	if err != nil {
		return nil, err
	}
	if len(p) > 0 {
		return &CPUStats{Percent: p[0]}, nil
	}
	return &CPUStats{}, nil
}
