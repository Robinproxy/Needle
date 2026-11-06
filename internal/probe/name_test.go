package probe

import (
	"strings"
	"testing"
)

func TestNormalizeNamePreservesStoredLabels(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"CTv4", "CTv4"},
		{" CTv4 ", "CTv4"},
		{"\u00a0\u3000CTv4\u2029", "CTv4"},
		{"C\tT\x00v4\x7f", "CTv4"},
		{"CTv4 \x01", "CTv4 "}, // Preserve the legacy trim-before-strip order.
		{" \x00 ", ""},
		{"My VPS's route", "My VPS's route"},
		{"ctv4", "ctv4"}, // Names remain case-sensitive.
		{strings.Repeat("线", 65), strings.Repeat("线", 64)},
	} {
		if got := NormalizeName(tc.raw); got != tc.want {
			t.Fatalf("NormalizeName(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
