package collector

import (
	"errors"
	"os"
	"testing"
)

func TestLinuxVirtualizationEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, system, role, wantSystem, wantRole string
		files                                    map[string]string
		container                                bool
	}{
		{name: "KVM guest", system: "kvm", role: "guest", wantSystem: "kvm", wantRole: "guest"},
		{name: "KVM host", system: "kvm", role: "host", wantSystem: "kvm", wantRole: "host"},
		{name: "Docker on KVM", system: "kvm", role: "guest", files: map[string]string{"/.dockerenv": ""}, wantSystem: "docker", wantRole: "guest", container: true},
		{name: "Podman cgroup v2", files: map[string]string{"/run/.containerenv": ""}, wantSystem: "podman", wantRole: "guest", container: true},
		{name: "LXC without access to PID 1 environment", files: map[string]string{"/run/systemd/container": "lxc-libvirt\n"}, wantSystem: "lxc", wantRole: "guest", container: true},
		{name: "nspawn", files: map[string]string{"/run/systemd/container": "systemd-nspawn\n"}, wantSystem: "systemd-nspawn", wantRole: "guest", container: true},
		{name: "passed-through CPU with nested KVM module", system: "kvm", role: "host", files: map[string]string{"/sys/class/dmi/id/sys_vendor": "QEMU\n", "/sys/class/dmi/id/product_name": "Standard PC (Q35 + ICH9, 2009)\n"}, wantSystem: "qemu", wantRole: "guest"},
		{name: "KVM DMI", files: map[string]string{"/sys/class/dmi/id/product_name": "KVM\n"}, wantSystem: "kvm", wantRole: "guest"},
		{name: "VMware", files: map[string]string{"/sys/class/dmi/id/product_name": "VMware Virtual Platform\n"}, wantSystem: "vmware", wantRole: "guest"},
		{name: "Hyper-V", files: map[string]string{"/sys/class/dmi/id/sys_vendor": "Microsoft Corporation\n", "/sys/class/dmi/id/product_name": "Virtual Machine\n"}, wantSystem: "hyperv", wantRole: "guest"},
		{name: "VirtualBox", files: map[string]string{"/sys/class/dmi/id/product_name": "VirtualBox\n"}, wantSystem: "vbox", wantRole: "guest"},
		{name: "Xen", files: map[string]string{"/sys/class/dmi/id/sys_vendor": "Xen\n"}, wantSystem: "xen", wantRole: "guest"},
		{name: "Xen control domain", system: "xen", role: "host", files: map[string]string{"/sys/class/dmi/id/sys_vendor": "Xen\n"}, wantSystem: "xen", wantRole: "host"},
		{name: "normal router", files: map[string]string{"/sys/class/dmi/id/sys_vendor": "Intel Corporation", "/sys/class/dmi/id/product_name": "Router"}},
		{name: "no accessible evidence"},
		{name: "virtio alone is not a hypervisor identity", role: "guest"},
		{name: "unknown system", system: "unknown", role: "guest"},
		{name: "unknown role", system: "linux-vserver", role: "unknown", wantSystem: "linux-vserver"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallbackCalls := 0
			system, role, err := linuxVirtualization(func(path string) ([]byte, error) {
				if data, ok := tc.files[path]; ok {
					return []byte(data), nil
				}
				return nil, os.ErrNotExist
			}, func() (string, string, error) {
				fallbackCalls++
				return tc.system, tc.role, nil
			})
			if err != nil || system != tc.wantSystem || role != tc.wantRole {
				t.Fatalf("got %q / %q / %v, want %q / %q", system, role, err, tc.wantSystem, tc.wantRole)
			}
			if (fallbackCalls == 0) != tc.container {
				t.Fatalf("container detection did not take priority: fallback calls=%d", fallbackCalls)
			}
		})
	}
}

func TestLinuxVirtualizationReadFailures(t *testing.T) {
	system, role, err := linuxVirtualization(func(string) ([]byte, error) {
		return nil, os.ErrPermission
	}, func() (string, string, error) {
		return "", "", os.ErrPermission
	})
	if system != "" || role != "" || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read failure became a successful bare-metal detection: %q / %q / %v", system, role, err)
	}
	// Losing one optional source must not discard another positive detection.
	system, role, err = linuxVirtualization(func(path string) ([]byte, error) {
		if path == "/.dockerenv" {
			return nil, nil
		}
		return nil, os.ErrPermission
	}, func() (string, string, error) { t.Fatal("container must skip VM detection"); return "", "", nil })
	if system != "docker" || role != "guest" || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("partial detection lost: %q / %q / %v", system, role, err)
	}
}
