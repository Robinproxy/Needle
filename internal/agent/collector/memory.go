package collector

import (
	"fmt"
	"github.com/shirou/gopsutil/v4/mem"
)

type MemoryStats struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	// Linux exposes these in the same unprivileged /proc/meminfo read.
	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`
}

func CollectMemory() (*MemoryStats, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}
	if v.Total == 0 || v.SwapFree > v.SwapTotal {
		return nil, fmt.Errorf("memory collector returned invalid memory/Swap totals")
	}
	return &MemoryStats{Total: v.Total, Used: v.Used, SwapTotal: v.SwapTotal, SwapUsed: v.SwapTotal - v.SwapFree}, nil
}
