package server

import (
	"testing"
	"time"
)

func TestGlobalLimiter(t *testing.T) {
	var limiter globalLimiter
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < globalReportLimit; i++ {
		if !limiter.Allow(now) {
			t.Fatalf("request %d unexpectedly limited", i+1)
		}
	}
	if limiter.Allow(now) {
		t.Fatal("request above global limit was allowed")
	}
	if !limiter.Allow(now.Add(time.Second)) {
		t.Fatal("limiter did not reset after one second")
	}
}

func TestTokenLimiter(t *testing.T) {
	limiter := newTokenLimiter()
	now := time.Unix(1_700_000_000, 0)
	if !limiter.Allow(1, now) || !limiter.Allow(1, now) {
		t.Fatal("initial burst should be allowed")
	}
	if limiter.Allow(1, now) {
		t.Fatal("request above per-token limit was allowed")
	}
	if !limiter.Allow(2, now) {
		t.Fatal("one token must not limit another token")
	}
	if !limiter.Allow(1, now.Add(time.Second)) {
		t.Fatal("token limiter did not reset after one second")
	}
	limiter.Delete(1)
	if !limiter.Allow(1, now) {
		t.Fatal("deleted limiter entry was not reset")
	}
}
