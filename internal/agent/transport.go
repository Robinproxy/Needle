package agent

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

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
