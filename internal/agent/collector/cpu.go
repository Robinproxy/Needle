package collector

import (
	"runtime"

	"github.com/shirou/gopsutil/v4/cpu"
)

type CPUStats struct {
	Cores   int     `json:"cores"` // Logical CPUs, including vCPUs on virtual machines.
	Percent float64 `json:"percent"`
}

func CollectCPU() (*CPUStats, error) {
	p, err := cpu.Percent(0, false)
	if err != nil {
		return nil, err
	}
	cores, countErr := cpu.Counts(true)
	if countErr != nil || cores < 1 {
		cores = runtime.NumCPU()
	}
	if len(p) > 0 {
		return &CPUStats{Percent: p[0], Cores: cores}, nil
	}
	return &CPUStats{Cores: cores}, nil
}
