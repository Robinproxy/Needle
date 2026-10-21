package collector

import (
	"math"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

func TestNetworkInterfaceFilter(t *testing.T) {
	def := NewNetworkCollector(nil)
	for _, name := range []string{"eth0", "ens3", "enp0s3", "wlan0", "em0", "en0", "bond0"} {
		if !def.counted(name) {
			t.Errorf("default filter: physical interface %q not counted", name)
		}
	}
	for _, name := range []string{
		"lo", "lo0", "docker0", "veth1a2b3c", "br-4d5e6f", "bridge0", "virbr0",
		"tun0", "tap0", "utun3", "wg0", "tailscale0", "zt7nnahdo3", "dummy0",
		"kube-bridge", "cni0", "flannel.1", "cali9f8e7d", "podman1", "vnet0",
	} {
		if def.counted(name) {
			t.Errorf("default filter: virtual interface %q counted", name)
		}
	}

	explicit := NewNetworkCollector([]string{"eth0", " eth1 "})
	for name, want := range map[string]bool{"eth0": true, "eth1": true, "eth2": false, "lo": false, "docker0": false} {
		if got := explicit.counted(name); got != want {
			t.Errorf("whitelist: counted(%q) = %v, want %v", name, got, want)
		}
	}

	// A whitelist of blanks falls back to the default filter.
	blank := NewNetworkCollector([]string{" ", ""})
	if !blank.counted("eth0") || blank.counted("lo") {
		t.Error("blank whitelist should behave like the default filter")
	}
}

func TestNetworkMissingInterfacesAreNotZeroCounters(t *testing.T) {
	c := NewNetworkCollector([]string{"eth0"})
	now := time.Unix(1700000000, 0)
	if _, err := c.collect([]net.IOCountersStat{{Name: "lo", BytesSent: 100}}, now); err == nil || c.scope != "" {
		t.Fatal("missing first interface must not create a zero baseline")
	}
	first, err := c.collect([]net.IOCountersStat{{Name: "eth0", BytesSent: 0, BytesRecv: 0}}, now)
	if err != nil || first.TotalSent != 0 || c.scope == "" {
		t.Fatalf("real zero counters must be accepted: %+v %v", first, err)
	}
	if _, err := c.collect(nil, now.Add(10*time.Second)); err == nil {
		t.Fatal("empty snapshot should be unavailable")
	}
	second, err := c.collect([]net.IOCountersStat{{Name: "eth0", BytesSent: 100, BytesRecv: 200}}, now.Add(20*time.Second))
	if err != nil || second.Up != 5 || second.Down != 10 {
		t.Fatalf("missing snapshot changed the rate baseline: %+v %v", second, err)
	}
}

func TestNetworkCounterOverflowDoesNotCorruptCollector(t *testing.T) {
	c := NewNetworkCollector(nil)
	now := time.Unix(1700000000, 0)
	for _, snapshot := range [][]net.IOCountersStat{
		{{Name: "eth0", BytesSent: math.MaxUint64}},
		{{Name: "eth0", BytesSent: math.MaxInt64}, {Name: "eth1", BytesSent: 1}},
	} {
		if _, err := c.collect(snapshot, now); err == nil || c.scope != "" {
			t.Fatal("overflow must not establish a baseline")
		}
	}
}
