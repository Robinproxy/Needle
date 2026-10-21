package collector

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	stdnet "net"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

type NetworkStats struct {
	ScopeID string `json:"network_scope_id,omitempty"`

	Up        int64 `json:"up"`
	Down      int64 `json:"down"`
	TotalSent int64 `json:"total_sent"`
	TotalRecv int64 `json:"total_recv"`
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
	scope    string
	indexOf  func(string) int
	include  map[string]bool // nil: default virtual-interface filter
	prevUp   uint64
	prevDown uint64
	prevTime time.Time
}

// NewNetworkCollector counts traffic on physical interfaces only. A non-empty
// interfaces list replaces the default filter with an exact-name whitelist.
func NewNetworkCollector(interfaces []string) *NetworkCollector {
	nc := &NetworkCollector{indexOf: func(name string) int {
		if iface, err := stdnet.InterfaceByName(name); err == nil {
			return iface.Index
		}
		return 0
	}}
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
	return nc.collect(counters, time.Now())
}

func (nc *NetworkCollector) collect(counters []net.IOCountersStat, now time.Time) (*NetworkStats, error) {
	var curSent, curRecv uint64
	var identities []string
	matched := false
	for _, c := range counters {
		if nc.counted(c.Name) {
			matched = true
			index := nc.indexOf(c.Name)
			identities = append(identities, fmt.Sprintf("%d:%s:%d", len(c.Name), c.Name, index))
			if c.BytesSent > math.MaxInt64-curSent || c.BytesRecv > math.MaxInt64-curRecv {
				return nil, fmt.Errorf("network counters exceed supported range")
			}
			curSent += c.BytesSent
			curRecv += c.BytesRecv
		}
	}

	if !matched {
		return nil, fmt.Errorf("no network interfaces matched the configured filter")
	}

	sort.Strings(identities)
	digest := sha256.Sum256([]byte(strings.Join(identities, "\n")))
	scope := hex.EncodeToString(digest[:])
	if nc.scope != scope {
		nc.scope = scope
		nc.prevUp = curSent
		nc.prevDown = curRecv
		nc.prevTime = now
		return &NetworkStats{ScopeID: scope, TotalSent: int64(curSent), TotalRecv: int64(curRecv)}, nil
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

	return &NetworkStats{ScopeID: scope, Up: up, Down: down, TotalSent: int64(curSent), TotalRecv: int64(curRecv)}, nil
}
