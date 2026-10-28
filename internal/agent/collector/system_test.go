package collector

import (
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shirou/gopsutil/v4/cpu"
)

func TestReleaseNamesAndFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"debian", map[string]string{"/etc/os-release": "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nNAME=Debian\nVERSION_ID=12"}, "Debian GNU/Linux 12 (bookworm)"},
		{"ubuntu", map[string]string{"/etc/os-release": "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\""}, "Ubuntu 24.04"},
		{"immortal", map[string]string{"/etc/openwrt_release": "DISTRIB_ID='ImmortalWrt'\nDISTRIB_RELEASE='24.10.4'\nDISTRIB_DESCRIPTION='ImmortalWrt 24.10.4 r33602-test'", "/etc/os-release": "PRETTY_NAME='OpenWrt'"}, "ImmortalWrt 24.10.4"},
		{"openwrt", map[string]string{"/etc/openwrt_release": "DISTRIB_DESCRIPTION='OpenWrt 24.10.4'"}, "OpenWrt 24.10.4"},
		{"usr fallback", map[string]string{"/usr/lib/os-release": "PRETTY_NAME=Alpine\n"}, "Alpine"},
		{"incomplete first file", map[string]string{"/etc/os-release": "ID=linux\n", "/usr/lib/os-release": "PRETTY_NAME='Alpine Linux v3.21'"}, "Alpine Linux v3.21"},
		{"literal shell syntax", map[string]string{"/etc/os-release": "PRETTY_NAME='$(hostname) `uname` ${TOKEN}'\nexit 1"}, "$(hostname) `uname` ${TOKEN}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, err := linuxOSName(func(path string) ([]byte, error) {
				if data, ok := tc.files[path]; ok {
					return []byte(data), nil
				}
				return nil, os.ErrNotExist
			})
			if err != nil || name != tc.want {
				t.Fatalf("got %q / %v, want %q", name, err, tc.want)
			}
		})
	}
}

func TestMissingSystemInfoRemainsUnavailable(t *testing.T) {
	for _, failure := range []error{os.ErrNotExist, os.ErrPermission} {
		name, err := linuxOSName(func(string) ([]byte, error) { return nil, failure })
		if name != "" || err == nil || !errors.Is(err, failure) {
			t.Fatalf("missing metadata became valid: %q / %v", name, err)
		}
	}
	if got := cpuModelNames([]cpu.InfoStat{{ModelName: "Undefined"}, {ModelName: "unknown"}, {}}); got != "" {
		t.Fatalf("unknown CPU model became a placeholder: %q", got)
	}
}

func TestCPUModelsAreUniqueAndTextIsBounded(t *testing.T) {
	got := cpuModelNames([]cpu.InfoStat{
		{ModelName: "  AMD EPYC\t7B13  "}, {ModelName: "AMD EPYC 7B13"},
		{ModelName: "Cortex-A76"}, {ModelName: "Cortex-A76"},
	})
	if got != "AMD EPYC 7B13 / Cortex-A76" {
		t.Fatalf("duplicate threads or whitespace leaked into CPU model: %q", got)
	}
	bounded := systemText(strings.Repeat("型", 300) + "\x00\n")
	if !utf8.ValidString(bounded) || utf8.RuneCountInString(bounded) != 256 || strings.ContainsAny(bounded, "\x00\n") {
		t.Fatalf("unbounded system metadata: %q", bounded)
	}
}
