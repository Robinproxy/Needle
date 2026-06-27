package collector

import (
	"github.com/shirou/gopsutil/v4/host"
)

func CollectUptime() (uint64, error) {
	return host.Uptime()
}
