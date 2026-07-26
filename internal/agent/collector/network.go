package collector

import (
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

type NetworkStats struct {
	Up         int64 `json:"up"`
	Down       int64 `json:"down"`
	TotalSent  int64 `json:"total_sent"`
	TotalRecv  int64 `json:"total_recv"`
}

// Interface name prefixes excluded by default: loopback and virtual devices
// whose traffic is machine-local or already counted on a physical NIC
// (container bridges/veths, VPN tunnels, virtualization backends).
var virtualIfacePrefixes = []string{
	"lo", "docker", "veth", "br-", "bridge", "virbr", "vnet",
	"tun", "tap", "utun", "wg", "tailscale", "zt",
	"dummy", "kube", "cni", "flannel", "cali", "podman",
}

type NetworkCollector struct {
	include  map[string]bool // nil: default virtual-interface filter
	prevUp   uint64
	prevDown uint64
	prevTime time.Time
	first    bool
}

// NewNetworkCollector counts traffic on physical interfaces only. A non-empty
// interfaces list replaces the default filter with an exact-name whitelist.
func NewNetworkCollector(interfaces []string) *NetworkCollector {
	nc := &NetworkCollector{first: true}
	include := make(map[string]bool, len(interfaces))
	for _, name := range interfaces {
		if name = strings.TrimSpace(name); name != "" {
			include[name] = true
		}
	}
	if len(include) > 0 {
		nc.include = include
	}
	return nc
}

func (nc *NetworkCollector) counted(name string) bool {
	if nc.include != nil {
		return nc.include[name]
	}
	for _, prefix := range virtualIfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func (nc *NetworkCollector) Collect() (*NetworkStats, error) {
	counters, err := net.IOCounters(true)
	if err != nil {
		return nil, err
	}

	var curSent, curRecv uint64
	for _, c := range counters {
		if nc.counted(c.Name) {
			curSent += c.BytesSent
			curRecv += c.BytesRecv
		}
	}

	now := time.Now()

	if nc.first {
		nc.prevUp = curSent
		nc.prevDown = curRecv
		nc.prevTime = now
		nc.first = false
		return &NetworkStats{TotalSent: int64(curSent), TotalRecv: int64(curRecv)}, nil
	}

	elapsed := now.Sub(nc.prevTime).Seconds()

	// detect counter reset (e.g. system reboot, interface removal) and re-baseline
	if curSent < nc.prevUp {
		nc.prevUp = curSent
	}
	if curRecv < nc.prevDown {
		nc.prevDown = curRecv
	}

	var up, down int64
	if elapsed > 0 {
		up = int64(float64(curSent-nc.prevUp) / elapsed)
		down = int64(float64(curRecv-nc.prevDown) / elapsed)
	}

	nc.prevUp = curSent
	nc.prevDown = curRecv
	nc.prevTime = now

	if up < 0 {
		up = 0
	}
	if down < 0 {
		down = 0
	}

	return &NetworkStats{Up: up, Down: down, TotalSent: int64(curSent), TotalRecv: int64(curRecv)}, nil
}
