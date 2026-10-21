package collector

import (
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"needle/internal/logging"
)

func TestProbePermissionFailuresAreBoundedAndRecoverIndependentlyOfReachability(t *testing.T) {
	var lines []string
	logs := logging.NewTracker("tcpping", func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	targets := []TCPingTarget{{Name: "CMv4", Target: "127.0.0.1:80"}}
	dialError := error(syscall.EACCES)
	dial := func(network, target string, timeout time.Duration) (net.Conn, error) {
		if network != "tcp" || target != targets[0].Target || timeout != 5*time.Second {
			t.Error("logging changed probe parameters")
		}
		return nil, dialError
	}
	for i := 0; i < 20; i++ {
		results := tcping(targets, dial, logs)
		if len(results) != 1 || results[0].Success || results[0].LatencyMs != 0 {
			t.Fatalf("logging changed failed probe results: %+v", results)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "WARN module=tcpping event=failed") {
		t.Fatalf("permission failures flooded logs: %v", lines)
	}
	// A refused connection proves the permission problem is gone, but must
	// remain an unsuccessful probe instead of being converted to success.
	dialError = syscall.ECONNREFUSED
	results := tcping(targets, dial, logs)
	tcping(targets, dial, logs)
	if results[0].Success || len(lines) != 2 || !strings.Contains(lines[1], "event=recovered") || !strings.Contains(lines[1], "count=20 suppressed=19") || !strings.Contains(lines[1], `check="permission"`) {
		t.Fatalf("permission recovery altered reachability or lost counts: result=%+v logs=%v", results, lines)
	}
}
