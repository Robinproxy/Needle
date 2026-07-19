package agent

import "testing"

func TestValidateServerURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		allowHTTP bool
		want      string
		wantErr   bool
	}{
		{name: "https", url: "https://needle.example.com", want: "https://needle.example.com"},
		{name: "https trailing slash", url: "https://needle.example.com/", want: "https://needle.example.com"},
		{name: "localhost", url: "http://localhost:8008", want: "http://localhost:8008"},
		{name: "localhost absolute", url: "http://localhost.:8008", want: "http://localhost.:8008"},
		{name: "IPv4 loopback", url: "http://127.0.0.2:8008", want: "http://127.0.0.2:8008"},
		{name: "IPv6 loopback", url: "http://[::1]:8008", want: "http://[::1]:8008"},
		{name: "remote HTTP rejected", url: "http://192.0.2.1:8008", wantErr: true},
		{name: "remote HTTP explicit", url: "http://192.0.2.1:8008", allowHTTP: true, want: "http://192.0.2.1:8008"},
		{name: "localhost suffix rejected", url: "http://localhost.example.com:8008", wantErr: true},
		{name: "userinfo rejected", url: "https://user:pass@needle.example.com", wantErr: true},
		{name: "query rejected", url: "https://needle.example.com?x=1", wantErr: true},
		{name: "fragment rejected", url: "https://needle.example.com#x", wantErr: true},
		{name: "missing scheme", url: "needle.example.com", wantErr: true},
		{name: "unknown scheme", url: "ftp://needle.example.com", wantErr: true},
		{name: "missing host", url: "https://", wantErr: true},
		{name: "bad port", url: "https://needle.example.com:bad", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateServerURL(tt.url, tt.allowHTTP)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateServerURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ValidateServerURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
