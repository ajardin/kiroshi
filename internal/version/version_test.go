package version

import (
	"strings"
	"testing"
)

// Mutating package-level variables means this test cannot run in parallel
// with anything else in the package, but it's the only test here so the
// constraint is moot. If a second test gets added, restore the defaults
// in a t.Cleanup.
func TestString_LdflagsOverride(t *testing.T) {
	origV, origC, origD := version, commit, date
	t.Cleanup(func() {
		version, commit, date = origV, origC, origD
	})

	version = "1.2.3"
	commit = "abcdef0"
	date = "2026-05-25T12:00:00Z"

	got := String()
	want := "kiroshi 1.2.3 (commit abcdef0, built 2026-05-25T12:00:00Z)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// When the binary is built with the default vars (no -ldflags), String
// must still return a recognizable identifier. We can't predict the
// commit/date fallback in `go test` since it depends on whether the test
// binary carries vcs.* settings, but the prefix and structure must hold.
func TestString_DefaultsHaveKiroshiPrefix(t *testing.T) {
	got := String()
	if !strings.HasPrefix(got, "kiroshi ") {
		t.Errorf("String() = %q, want prefix \"kiroshi \"", got)
	}
	if !strings.Contains(got, "(commit ") || !strings.Contains(got, ", built ") {
		t.Errorf("String() = %q, want shape \"kiroshi <v> (commit <c>, built <d>)\"", got)
	}
}
