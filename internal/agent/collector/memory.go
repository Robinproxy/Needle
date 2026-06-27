package collector

import (
	"github.com/shirou/gopsutil/v4/mem"
)

type MemoryStats struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

func CollectMemory() (*MemoryStats, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}
	return &MemoryStats{Total: v.Total, Used: v.Used}, nil
}
