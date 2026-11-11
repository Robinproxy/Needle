package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"needle/internal/logging"
)

type measuredResponseBody struct {
	reads  int
	closed bool
	reader io.Reader
}

func TestUpstreamErrorBodyNeverAppearsInLogs(t *testing.T) {
	r := NewReporter("http://localhost", "private-token", false)
	r.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		return response(502, "Authorization: Bearer private-token\ntoken: private-token\nINFO forged\n<proxy HTML>"), nil
	})
	err := r.SendRealtime(context.Background(), &RealtimeData{Hostname: "node"})
	var line string
	logs := logging.NewTracker("realtime", func(format string, args ...any) { line = fmt.Sprintf(format, args...) })
	logs.Check("send", "WARN", err)
	if err == nil || !strings.Contains(line, "server 502: Bad Gateway") {
		t.Fatalf("missing actionable status: %v / %s", err, line)
	}
	for _, forbidden := range []string{"private-token", "Authorization", "forged", "proxy HTML", "\n"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("upstream content leaked into a runtime log: %q", line)
		}
	}
}

func (b *measuredResponseBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.reads += n
	return n, err
}
func (b *measuredResponseBody) Close() error { b.closed = true; return nil }

func TestResponseReadsAreBoundedAndBodiesClosed(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		for _, size := range []int{32, 10 * maxResponseBytes} {
			body := &measuredResponseBody{reader: strings.NewReader(strings.Repeat("x", size))}
			err := readReportResponse(&http.Response{StatusCode: status, Body: body})
			if (err == nil) != (status == http.StatusOK) {
				t.Fatalf("status %d: %v", status, err)
			}
			if body.reads != min(size, maxResponseBytes) || !body.closed {
				t.Fatalf("read=%d,closed=%v", body.reads, body.closed)
			}
			if err != nil && len(err.Error()) > maxResponseBytes+32 {
				t.Fatal("unbounded error log")
			}
		}
	}
}
