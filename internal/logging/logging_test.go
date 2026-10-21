package logging

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFailureSummariesAndRecovery(t *testing.T) {
	var lines []string
	logs := NewTracker("network", func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	now := time.Unix(1000, 0)
	logs.now = func() time.Time { return now }
	failure := errors.New("interface unavailable")
	logs.Check("eth1", "WARN", nil)
	logs.Check("eth1", "WARN", failure)
	for i := 0; i < 10; i++ {
		now = now.Add(5 * time.Second)
		logs.Check("eth1", "WARN", failure)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "count=1 suppressed=0") {
		t.Fatalf("repeated failures flooded output: %v", lines)
	}
	now = now.Add(10 * time.Second)
	logs.Check("eth1", "WARN", failure)
	if len(lines) != 2 || !strings.Contains(lines[1], "count=12 suppressed=10") {
		t.Fatalf("missing minute summary: %v", lines)
	}
	logs.Check("eth1", "WARN", failure)
	logs.Check("eth1", "WARN", nil)
	logs.Check("eth1", "WARN", nil)
	if len(lines) != 3 || !strings.Contains(lines[2], "INFO module=network event=recovered") || !strings.Contains(lines[2], "count=13 suppressed=1") {
		t.Fatalf("recovery lost suppressed failures or repeated: %v", lines)
	}
	logs.Check("eth1", "WARN", failure)
	if len(lines) != 4 || !strings.Contains(lines[3], "count=1 suppressed=0") {
		t.Fatalf("a new outage was suppressed: %v", lines)
	}
}

func TestDifferentOperationsAndBoundedOverflow(t *testing.T) {
	var lines []string
	logs := NewTracker("api", func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	failure := errors.New("storage unavailable")
	for i := 0; i < maxIssues+1000; i++ {
		logs.Check(fmt.Sprint(i), "ERROR", failure)
	}
	if len(logs.issues) != maxIssues || len(lines) != maxIssues+1 || logs.overflow.count != 1000 {
		t.Fatalf("unbounded distinct failures: states=%d output=%d overflow=%+v", len(logs.issues), len(lines), logs.overflow)
	}
	logs.Check("not tracked", "ERROR", nil)
	if len(lines) != maxIssues+1 {
		t.Fatal("an unrelated success reported recovery of the overflow bucket")
	}
	logs.Check("0", "ERROR", nil)
	logs.Check("new issue", "WARN", failure)
	if len(logs.issues) != maxIssues || len(lines) != maxIssues+3 {
		t.Fatal("recovery did not release tracked capacity")
	}
}

func TestConcurrentFailureCounts(t *testing.T) {
	var lines []string
	logs := NewTracker("report", func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	var workers sync.WaitGroup
	for i := 0; i < 1000; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			logs.Check("send", "ERROR", errors.New("failed"))
		}()
	}
	workers.Wait()
	logs.Check("send", "ERROR", nil)
	if len(lines) != 2 || !strings.Contains(lines[1], "count=1000 suppressed=999") {
		t.Fatalf("concurrent failures lost counts or flooded output: %v", lines)
	}
}

func TestTextIsBoundedAndCannotForgeLogLines(t *testing.T) {
	var line string
	Write(func(format string, args ...any) { line = fmt.Sprintf(format, args...) }, "WARN", "test", "failed",
		"error", errors.New("failure\nINFO forged\r\x1b[31m"), "hostname", strings.Repeat("路", 1000))
	if strings.ContainsAny(line, "\n\r\x1b") || !strings.Contains(line, `error="failure\nINFO forged\r\x1b[31m"`) || !strings.Contains(line, "…") || !utf8.ValidString(line) || len(line) > 2000 {
		t.Fatalf("unsafe or unbounded log text: %q", line)
	}
}
