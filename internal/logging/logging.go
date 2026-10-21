// Package logging keeps runtime messages small and repeated failures bounded.
// Output remains on the caller's standard logger; no files or timers are added.
package logging

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	repeatInterval = time.Minute
	maxIssues      = 128
	maxTextRunes   = 512
)

func Print(level, module, event string, fields ...any) {
	Write(log.Printf, level, module, event, fields...)
}

func Fatal(module, event string, err error) {
	Print("ERROR", module, event, "error", err)
	os.Exit(1)
}

// Write quotes text, including errors, so a multiline upstream error cannot
// forge another log entry. Callers supply fixed field names, never credentials.
func Write(output func(string, ...any), level, module, event string, fields ...any) {
	var line strings.Builder
	fmt.Fprintf(&line, "%s module=%s event=%s", level, module, event)
	for i := 0; i+1 < len(fields); i += 2 {
		fmt.Fprintf(&line, " %s=", fields[i])
		switch value := fields[i+1].(type) {
		case string:
			line.WriteString(strconv.Quote(shortText(value)))
		case error:
			line.WriteString(strconv.Quote(shortText(value.Error())))
		default:
			fmt.Fprint(&line, value)
		}
	}
	output("%s", line.String())
}

func shortText(s string) string {
	count := 0
	for offset := range s {
		if count == maxTextRunes {
			return s[:offset] + "…"
		}
		count++
	}
	return s
}

type issue struct {
	first, last       time.Time
	count, suppressed uint64
}

// Tracker records only failure counters and timestamps, never error bodies or
// configuration. Keys identify operations, not error strings. An overflow bucket
// bounds memory and output even if a caller supplies many distinct keys.
type Tracker struct {
	mu       sync.Mutex
	module   string
	output   func(string, ...any)
	now      func() time.Time
	issues   map[string]issue
	overflow issue
}

func NewTracker(module string, output func(string, ...any)) *Tracker {
	return &Tracker{module: module, output: output, now: time.Now, issues: make(map[string]issue)}
}

// Check logs the first failure immediately, summarizes continued failures at
// most once per minute, and logs one recovery when this same operation succeeds.
// Nil errors on operations that have never failed produce no output.
func (t *Tracker) Check(key, level string, err error, fields ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	state, exists := t.issues[key]
	if err == nil {
		if exists {
			Write(t.output, "INFO", t.module, "recovered", append([]any{
				"issue", key, "count", state.count, "suppressed", state.suppressed,
				"duration", now.Sub(state.first).Round(time.Second),
			}, fields...)...)
			delete(t.issues, key)
		}
		return
	}
	overflow := !exists && len(t.issues) >= maxIssues
	if overflow {
		state = t.overflow
	}
	if state.count == 0 {
		state.first = now
	}
	state.count++
	if state.count == 1 || now.Sub(state.last) >= repeatInterval {
		event := "failed"
		if overflow {
			event = "additional_failures"
		}
		Write(t.output, level, t.module, event, append([]any{
			"issue", key, "count", state.count, "suppressed", state.suppressed, "error", err,
		}, fields...)...)
		state.last, state.suppressed = now, 0
	} else {
		state.suppressed++
	}
	if overflow {
		t.overflow = state
	} else {
		t.issues[key] = state
	}
}
