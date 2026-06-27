package collector

import (
	"github.com/shirou/gopsutil/v4/load"
)

type LoadStats struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

func CollectLoad() (*LoadStats, error) {
	l, err := load.Avg()
	if err != nil {
		return nil, err
	}
	return &LoadStats{Load1: l.Load1, Load5: l.Load5, Load15: l.Load15}, nil
}
