package agent

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type measuredResponseBody struct {
	reads  int
	closed bool
	reader io.Reader
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
