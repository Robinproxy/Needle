//go:build linux

package collector

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

func BootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(id) {
		return "", fmt.Errorf("invalid kernel boot ID")
	}
	return id, nil
}
