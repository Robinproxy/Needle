// Package probe defines the shared TCP Ping wire-name normalization rules.
package probe

import "strings"

// NormalizeName preserves the Server's existing stored-name representation.
// Do not rewrite report payloads with it: persistent receipt digests include
// the original wire names. Use it for validation keys and stored labels only.
func NormalizeName(raw string) string {
	var name strings.Builder
	count := 0
	for _, ch := range strings.TrimSpace(raw) {
		if ch < 32 || ch == 127 {
			continue
		}
		name.WriteRune(ch)
		count++
		if count == 64 {
			break
		}
	}
	return name.String()
}
