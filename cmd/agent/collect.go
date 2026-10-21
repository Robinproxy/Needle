package main

import (
	"errors"
	"time"

	agentpkg "needle/internal/agent"
	"needle/internal/agent/collector"
	"needle/internal/logging"
)

type reportCollectors struct {
	cpu     func() (*collector.CPUStats, error)
	memory  func() (*collector.MemoryStats, error)
	disk    func() (*collector.DiskStats, error)
	network func() (*collector.NetworkStats, error)
	load    func() (*collector.LoadStats, error)
	uptime  func() (uint64, error)
}

// Discard failed results even if a collector returns a partial value. Nil
// metric pointers and uptime zero carry the existing unavailable semantics.
func collectValue[T any](name string, collect func() (T, error), logs *logging.Tracker) T {
	value, err := collect()
	if err != nil {
		if !errors.Is(err, collector.ErrBaseline) {
			logs.Check(name, "WARN", err)
		}
		var unavailable T
		return unavailable
	}
	logs.Check(name, "WARN", nil)
	return value
}

func newWindowCollectors(interfaces []string) reportCollectors {
	cpu := &collector.CPUCollector{}
	network := collector.NewNetworkCollector(interfaces)
	ready := false
	return reportCollectors{
		cpu: cpu.Collect, memory: collector.CollectMemory, disk: collector.CollectDisk,
		network: func() (*collector.NetworkStats, error) {
			value, err := network.Collect()
			if err != nil {
				return nil, err
			}
			if !ready {
				ready = true
				return nil, collector.ErrBaseline
			}
			return value, nil
		}, load: collector.CollectLoad, uptime: collector.CollectUptime,
	}
}

func collectReport(c reportCollectors, logs *logging.Tracker) *agentpkg.ReportData {
	return &agentpkg.ReportData{
		// Timestamp the sample before collection/probing and transport delay.
		// The Server can then reject old counter/config updates by sample time.
		CreatedAt: time.Now().Unix(),
		CPU:       collectValue("cpu", c.cpu, logs),
		Memory:    collectValue("memory", c.memory, logs),
		Disk:      collectValue("disk", c.disk, logs),
		Network:   collectValue("network", c.network, logs),
		Load:      collectValue("load", c.load, logs),
		Uptime:    collectValue("uptime", c.uptime, logs),
	}
}
