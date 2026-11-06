package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	maxMetricHistoryRows        = 4096
	maxTCPingHistoryRows        = 32768
	historyTargetBuckets        = 720
	maxConcurrentHistoryQueries = 4
	historyQueryTimeout         = 10 * time.Second
)

var errHistoryTooLarge = errors.New("history result exceeds point limit")

type historyWindow struct {
	since, until, bucketSeconds int64
	useHourly                   bool
}

// Both URL forms use the same sampling and retention policy. Bounds are
// half-open, including the current second for relative windows.
func parseHistoryWindow(q url.Values, now time.Time) (historyWindow, error) {
	invalid := errors.New("invalid time window")
	for _, key := range []string{"range", "since", "until"} {
		if values, ok := q[key]; ok && (len(values) != 1 || values[0] == "") {
			return historyWindow{}, invalid
		}
	}
	w := historyWindow{since: now.Add(-time.Hour).Unix(), until: now.Unix() + 1}
	if q.Has("since") || q.Has("until") {
		if !q.Has("since") || !q.Has("until") || q.Has("range") {
			return historyWindow{}, invalid
		}
		var err error
		w.since, err = strconv.ParseInt(q.Get("since"), 10, 64)
		if err != nil {
			return historyWindow{}, invalid
		}
		w.until, err = strconv.ParseInt(q.Get("until"), 10, 64)
		// Check absolute bounds before subtracting untrusted signed integers.
		if err != nil || w.since < now.Add(-hourlyRetention).Unix() || w.since < 0 || w.until <= w.since || w.until > now.Add(5*time.Minute).Unix() {
			return historyWindow{}, invalid
		}
		if w.until-w.since > int64((8*24*time.Hour)/time.Second) {
			return historyWindow{}, invalid
		}
	} else if q.Has("range") {
		d, err := time.ParseDuration(q.Get("range"))
		if err != nil || d < time.Second {
			return historyWindow{}, invalid
		}
		if d > 30*24*time.Hour {
			d = 30 * 24 * time.Hour
		}
		w.since = now.Add(-d).Unix()
	}
	w.useHourly = w.since < now.Add(-rawRetention).Unix()
	if w.until-w.since > int64((25*time.Hour)/time.Second) {
		w.bucketSeconds = historyBucketSeconds(w.until - w.since)
	}
	return w, nil
}

func historyBucketSeconds(seconds int64) int64 {
	// Round up to whole minutes; a misaligned window can add one edge bucket.
	bucket := (seconds + historyTargetBuckets - 1) / historyTargetBuckets
	return max(int64(60), (bucket+59)/60*60)
}

func writeHistoryError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	switch {
	case errors.Is(err, errHistoryTooLarge):
		http.Error(w, "history result too large; request a shorter window", http.StatusUnprocessableEntity)
	case errors.Is(err, context.DeadlineExceeded):
		w.Header().Set("Retry-After", "1")
		http.Error(w, "history query timed out", http.StatusGatewayTimeout)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
