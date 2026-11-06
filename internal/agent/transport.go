package agent

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Every authenticated operation shares this client policy. Even explicit
// plaintext HTTP permission does not authorize a redirect to another origin.
func checkReportRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || len(via) >= 10 || req.URL.User != nil ||
		req.Method != via[0].Method || !sameReportOrigin(via[0].URL, req.URL) {
		// Return the 3xx response to the existing bounded status handling.
		// A custom redirect error would include the untrusted Location in logs.
		return http.ErrUseLastResponse
	}
	return nil
}

func sameReportOrigin(a, b *url.URL) bool {
	// DNS names and IPv6 hex digits are case-insensitive; interface zone names
	// can identify different interfaces and must be compared exactly.
	hostA, hostB := a.Hostname(), b.Hostname()
	sameHost := strings.EqualFold(hostA, hostB)
	if strings.Contains(hostA, "%") || strings.Contains(hostB, "%") {
		sameHost = hostA == hostB
	}
	return a.Scheme == b.Scheme && sameHost &&
		reportURLPort(a) == reportURLPort(b)
}

func reportURLPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// ValidateServerURL validates the Agent's reporting endpoint. HTTPS is always
// allowed. Plain HTTP is limited to loopback hosts unless explicitly enabled.
func ValidateServerURL(rawURL string, allowPlainHTTP bool) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("server URL is required")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("server URL must use http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("server URL must include a host")
	}
	if u.User != nil {
		return "", fmt.Errorf("server URL must not include credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("server URL must not include a query or fragment")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) && !allowPlainHTTP {
		return "", fmt.Errorf("refusing plaintext HTTP to non-loopback host %q; use HTTPS or explicitly set allow_plain_http: true", u.Hostname())
	}

	return strings.TrimRight(rawURL, "/"), nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
