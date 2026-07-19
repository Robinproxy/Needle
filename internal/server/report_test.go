package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func decodeReportForTest(t *testing.T, body string) reportRequest {
	t.Helper()
	var req reportRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestValidateReport(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid", body: `{"hostname":"node-1","billing_period":"1m","cpu":{"percent":50},"memory":{"total":100,"used":50},"disk":{"total":200,"used":100},"network":{"up":10,"down":20,"total_sent":30,"total_recv":40},"load":{"load1":1,"load5":2,"load15":3},"uptime":100,"tcpping":[{"name":"CMv4","target":"example.com:80","latency_ms":12.5,"success":true}]}`},
		{name: "trim hostname", body: `{"hostname":" node-1 "}`},
		{name: "empty hostname", body: `{"hostname":" "}`, wantErr: true},
		{name: "hostname control", body: `{"hostname":"node\n1"}`, wantErr: true},
		{name: "invalid billing", body: `{"hostname":"node","billing_period":"2m"}`, wantErr: true},
		{name: "CPU negative", body: `{"hostname":"node","cpu":{"percent":-1}}`, wantErr: true},
		{name: "CPU above 100", body: `{"hostname":"node","cpu":{"percent":101}}`, wantErr: true},
		{name: "memory used above total", body: `{"hostname":"node","memory":{"total":10,"used":11}}`, wantErr: true},
		{name: "memory int64 overflow", body: `{"hostname":"node","memory":{"total":9223372036854775808,"used":1}}`, wantErr: true},
		{name: "disk used above total", body: `{"hostname":"node","disk":{"total":10,"used":11}}`, wantErr: true},
		{name: "negative network", body: `{"hostname":"node","network":{"up":-1}}`, wantErr: true},
		{name: "negative load", body: `{"hostname":"node","load":{"load1":-1}}`, wantErr: true},
		{name: "uptime int64 overflow", body: `{"hostname":"node","uptime":9223372036854775808}`, wantErr: true},
		{name: "empty TCPing target", body: `{"hostname":"node","tcpping":[{"name":"CMv4","target":"","latency_ms":1}]}`, wantErr: true},
		{name: "negative TCPing latency", body: `{"hostname":"node","tcpping":[{"name":"CMv4","target":"example.com:80","latency_ms":-1}]}`, wantErr: true},
		{name: "excessive TCPing latency", body: `{"hostname":"node","tcpping":[{"name":"CMv4","target":"example.com:80","latency_ms":60001}]}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := decodeReportForTest(t, tt.body)
			err := validateReport(&req, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateReport() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.name == "trim hostname" && req.Hostname != "node-1" {
				t.Fatalf("hostname was not trimmed: %q", req.Hostname)
			}
		})
	}
}
