package collector

import (
	"errors"
	"fmt"
	"math"
	"runtime"

	"github.com/shirou/gopsutil/v4/cpu"
)

var ErrBaseline = errors.New("waiting for a second sample to establish a measurement window")

// Each sampling channel owns a collector. cpu.Percent's package-wide baseline
// would make a five-second sample shorten the historical measurement window.
type CPUCollector struct {
	previous cpu.TimesStat
	ready    bool
}

func (c *CPUCollector) Collect() (*CPUStats, error) {
	values, err := cpu.Times(false)
	if err != nil {
		return nil, err
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("CPU collector returned no aggregate counter")
	}
	cores, err := cpu.Counts(true)
	if err != nil || cores < 1 {
		cores = runtime.NumCPU()
	}
	return c.collect(values[0], cores)
}

func cpuTimes(t cpu.TimesStat) (total, busy float64) {
	// Guest time is already included in user/nice on Linux.
	total = t.User + t.Nice + t.System + t.Idle + t.Iowait + t.Irq + t.Softirq + t.Steal
	return total, total - t.Idle - t.Iowait
}

func (c *CPUCollector) collect(current cpu.TimesStat, cores int) (*CPUStats, error) {
	previous, ready := c.previous, c.ready
	c.previous, c.ready = current, true
	if !ready {
		return nil, ErrBaseline
	}
	total, busy := cpuTimes(current)
	oldTotal, oldBusy := cpuTimes(previous)
	if total <= oldTotal || busy < oldBusy {
		return nil, fmt.Errorf("CPU counters did not advance monotonically")
	}
	return &CPUStats{Cores: cores, Percent: math.Max(0, math.Min(100, (busy-oldBusy)/(total-oldTotal)*100))}, nil
}

type CPUStats struct {
	Cores   int     `json:"cores"` // Logical CPUs, including vCPUs on virtual machines.
	Percent float64 `json:"percent"`
}
