package collector

import "testing"

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
