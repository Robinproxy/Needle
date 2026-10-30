package collector

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Prefer evidence of the Agent's container over the outer VM. All probes are
// read-only and work without commands, root privileges or extra capabilities.
func linuxVirtualization(readFile func(string) ([]byte, error), fallback func() (string, string, error)) (string, string, error) {
	var failures []error
	result := func(system, role string) (string, string, error) {
		system = strings.ToLower(systemText(system))
		if system == "" || system == "unknown" || system == "none" || len(system) > 64 {
			// No positive identification is not proof of bare metal.
			return "", "", errors.Join(failures...)
		}
		if role != "guest" && role != "host" {
			role = ""
		}
		return system, role, errors.Join(failures...)
	}
	read := func(path string) (string, bool) {
		data, err := readFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("%s: %w", path, err))
			}
			return "", false
		}
		return strings.ToLower(strings.TrimSpace(string(data))), true
	}
	if system, _ := read("/run/systemd/container"); system != "" {
		if system == "lxc-libvirt" {
			system = "lxc"
		}
		return result(system, "guest")
	}
	for _, marker := range []struct{ path, system string }{
		{"/.dockerenv", "docker"}, {"/run/.containerenv", "podman"},
	} {
		if _, exists := read(marker.path); exists {
			return result(marker.system, "guest")
		}
	}
	system, role, err := fallback()
	if err != nil {
		failures = append(failures, err)
	}
	// DMI also identifies guests with a passed-through CPU model. A KVM
	// module inside such a guest must not make it appear to be the outer host.
	if role != "guest" || system == "" {
		vendor, _ := read("/sys/class/dmi/id/sys_vendor")
		product, _ := read("/sys/class/dmi/id/product_name")
		switch {
		case strings.Contains(product, "kvm"):
			system, role = "kvm", "guest"
		case strings.Contains(vendor, "qemu"):
			system, role = "qemu", "guest"
		case strings.Contains(vendor+product, "vmware"):
			system, role = "vmware", "guest"
		case vendor == "microsoft corporation" && product == "virtual machine":
			system, role = "hyperv", "guest"
		case strings.Contains(product, "virtualbox"):
			system, role = "vbox", "guest"
		case vendor == "xen" && !(system == "xen" && role == "host"):
			system, role = "xen", "guest"
		}
	}
	return result(system, role)
}
