package collector

import (
	"github.com/shirou/gopsutil/v4/disk"
)

type DiskStats struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

func CollectDisk() (*DiskStats, error) {
	p, err := disk.Usage("/")
	if err != nil {
		return nil, err
	}
	return &DiskStats{Total: p.Total, Used: p.Used}, nil
}
