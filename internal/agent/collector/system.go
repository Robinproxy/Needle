package collector

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"unicode"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
)

type SystemInfo struct {
	CPUModel string
	OSName   string
	Arch     string
}

// CollectSystemInfo runs once at startup. These optional details are separate
// from CPU utilization so a missing model cannot invalidate a live CPU sample.
func CollectSystemInfo() (SystemInfo, error) {
	info := SystemInfo{Arch: runtime.GOARCH}
	switch info.Arch {
	case "amd64":
		info.Arch = "x86_64"
	case "arm64":
		info.Arch = "aarch64"
	}
	var failures []error
	processors, err := cpu.Info()
	if err != nil {
		failures = append(failures, fmt.Errorf("CPU model: %w", err))
	} else {
		info.CPUModel = cpuModelNames(processors)
		if info.CPUModel == "" {
			failures = append(failures, fmt.Errorf("CPU model unavailable"))
		}
	}
	if runtime.GOOS == "linux" {
		info.OSName, err = linuxOSName(os.ReadFile)
	} else {
		var platform, version string
		platform, _, version, err = host.PlatformInformation()
		info.OSName = systemText(platform + " " + version)
		if err == nil && info.OSName == "" {
			err = fmt.Errorf("platform unavailable")
		}
	}
	if err != nil {
		failures = append(failures, fmt.Errorf("operating system: %w", err))
	}
	return info, errors.Join(failures...)
}

func systemText(value string) string {
	value = strings.Map(func(ch rune) rune {
		if unicode.IsControl(ch) && !unicode.IsSpace(ch) {
			return -1
		}
		return ch
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if text := []rune(value); len(text) > 256 {
		value = string(text[:256])
	}
	return value
}

func cpuModelNames(processors []cpu.InfoStat) string {
	var names []string
	seen := make(map[string]bool)
	for _, processor := range processors {
		name := systemText(processor.ModelName)
		if name == "" || strings.EqualFold(name, "undefined") || strings.EqualFold(name, "unknown") || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return systemText(strings.Join(names, " / "))
}

// Parse release files as data, never source them as shell scripts. ImmortalWrt
// has its own DISTRIB_ID; prefer it over a generic OpenWrt os-release entry.
func linuxOSName(readFile func(string) ([]byte, error)) (string, error) {
	var failures []error
	for _, path := range []string{"/etc/openwrt_release", "/etc/os-release", "/usr/lib/os-release"} {
		data, err := readFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("%s: %w", path, err))
			}
			continue
		}
		values := make(map[string]string)
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || strings.HasPrefix(key, "#") {
				continue
			}
			value = strings.TrimSpace(value)
			if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
				doubleQuoted := value[0] == '"'
				value = value[1 : len(value)-1]
				if doubleQuoted {
					value = strings.NewReplacer(`\"`, `"`, `\$`, `$`, "\\`", "`", `\\`, `\`).Replace(value)
				}
			}
			values[strings.TrimSpace(key)] = systemText(value)
		}
		name := values["PRETTY_NAME"]
		if path == "/etc/openwrt_release" {
			name = ""
			if values["DISTRIB_ID"] != "" {
				name = values["DISTRIB_ID"] + " " + values["DISTRIB_RELEASE"]
			} else {
				name = values["DISTRIB_DESCRIPTION"]
			}
		} else if name == "" && values["NAME"] != "" {
			name = values["NAME"] + " " + values["VERSION_ID"]
		}
		if name = systemText(name); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("release information unavailable: %w", errors.Join(append(failures, os.ErrNotExist)...))
}
