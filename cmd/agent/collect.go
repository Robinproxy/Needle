package main

import (
	agentpkg "needle/internal/agent"
	"needle/internal/agent/collector"
	"time"
)

type reportCollectors struct {
	cpu     func() (*collector.CPUStats, error)
	memory  func() (*collector.MemoryStats, error)
	disk    func() (*collector.DiskStats, error)
	network func() (*collector.NetworkStats, error)
	load    func() (*collector.LoadStats, error)
	uptime  func() (uint64, error)
	tcpping func([]collector.TCPingTarget) []collector.TCPingResult
}

// Discard failed results even if a collector returns a partial value. Nil
// metric pointers and uptime zero carry the existing unavailable semantics.
func collectValue[T any](name string, collect func() (T, error), logf func(string, ...any)) T {
	value, err := collect()
	if err != nil {
		logf("collect %s: %v", name, err)
		var unavailable T
		return unavailable
	}
	return value
}

func collectReport(c reportCollectors, targets []collector.TCPingTarget, logf func(string, ...any)) *agentpkg.ReportData {
	return &agentpkg.ReportData{
		// Timestamp the sample before collection/probing and transport delay.
		// The Server can then reject old counter/config updates by sample time.
		CreatedAt: time.Now().Unix(),
		CPU:       collectValue("cpu", c.cpu, logf),
		Memory:    collectValue("memory", c.memory, logf),
		Disk:      collectValue("disk", c.disk, logf),
		Network:   collectValue("network", c.network, logf),
		Load:      collectValue("load", c.load, logf),
		Uptime:    collectValue("uptime", c.uptime, logf),
		TCPing:    c.tcpping(targets),
	}
}
