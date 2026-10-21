package collector

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	gn "github.com/shirou/gopsutil/v4/net"
)

func TestIndependentCPUAndNetworkWindows(t *testing.T) {
	history, realtime := &CPUCollector{}, &CPUCollector{}
	if _, err := history.collect(cpu.TimesStat{}, 4); !errors.Is(err, ErrBaseline) {
		t.Fatal(err)
	}
	realtime.collect(cpu.TimesStat{}, 4)
	// Five seconds fully busy, then 25 seconds idle. The historical reading
	// must cover the whole 30 seconds rather than the last realtime window.
	for second := 5; second <= 30; second += 5 {
		busy := float64(5)
		idle := float64(second - 5)
		realtime.collect(cpu.TimesStat{User: busy, Idle: idle}, 4)
	}
	value, err := history.collect(cpu.TimesStat{User: 5, Idle: 25}, 4)
	if err != nil || math.Abs(value.Percent-100.0/6) > 0.001 {
		t.Fatalf("history=%+v err=%v", value, err)
	}
	base := time.Unix(1000, 0)
	h, r := NewNetworkCollector([]string{"eth0"}), NewNetworkCollector([]string{"eth0"})
	counters := func(bytes uint64) []gn.IOCountersStat {
		return []gn.IOCountersStat{{Name: "eth0", BytesSent: bytes, BytesRecv: bytes}}
	}
	h.collect(counters(0), base)
	r.collect(counters(0), base)
	r.collect(counters(3000), base.Add(5*time.Second))
	r.collect(counters(3000), base.Add(30*time.Second))
	network, err := h.collect(counters(3000), base.Add(30*time.Second))
	if err != nil || network.Up != 100 || network.Down != 100 {
		t.Fatalf("historical rates=%+v %v", network, err)
	}
}
