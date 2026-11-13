package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanCutsOnRuneBoundary(t *testing.T) {
	// 'ж' is two bytes: a cut at an odd limit lands inside a rune.
	got := clean(strings.Repeat("ж", 40), 63)
	if !utf8.ValidString(got) || got != strings.Repeat("ж", 31) {
		t.Fatalf("clean = %q (%d bytes)", got, len(got))
	}
	if got := clean("a\x00b\xffc\n", 10); got != "abc" {
		t.Fatalf("control and invalid bytes: %q", got)
	}
	if got := clean(strings.Repeat("a", 70), 64); len(got) != 64 {
		t.Fatalf("ascii cut: %d", len(got))
	}
}
