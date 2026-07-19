package server

import (
	"sync"
	"time"
)

const (
	globalReportLimit   = 200
	perTokenReportLimit = 2
	maxLimiterEntries   = 4096
)

type globalLimiter struct {
	mu          sync.Mutex
	windowStart time.Time
	count       int
}

func (l *globalLimiter) Allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windowStart.IsZero() || now.Sub(l.windowStart) >= time.Second {
		l.windowStart = now
		l.count = 0
	}
	if l.count >= globalReportLimit {
		return false
	}
	l.count++
	return true
}

type limiterEntry struct {
	windowStart time.Time
	lastSeen    time.Time
	count       int
}

type tokenLimiter struct {
	mu      sync.Mutex
	entries map[int64]limiterEntry
}

func newTokenLimiter() *tokenLimiter {
	return &tokenLimiter{entries: make(map[int64]limiterEntry)}
}

func (l *tokenLimiter) Allow(tokenID int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[tokenID]
	if !ok && len(l.entries) >= maxLimiterEntries {
		cutoff := now.Add(-10 * time.Minute)
		for id, candidate := range l.entries {
			if candidate.lastSeen.Before(cutoff) {
				delete(l.entries, id)
			}
		}
		if len(l.entries) >= maxLimiterEntries {
			return false
		}
	}

	if !ok || now.Sub(e.windowStart) >= time.Second {
		e.windowStart = now
		e.count = 0
	}
	if e.count >= perTokenReportLimit {
		e.lastSeen = now
		l.entries[tokenID] = e
		return false
	}
	e.count++
	e.lastSeen = now
	l.entries[tokenID] = e
	return true
}

func (l *tokenLimiter) Delete(tokenID int64) {
	l.mu.Lock()
	delete(l.entries, tokenID)
	l.mu.Unlock()
}
