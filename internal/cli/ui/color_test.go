package ui

import "testing"

// TestColorizeGating verifies Colorize wraps text in SGR codes only when
// enabled, returns text unchanged when disabled, and treats an empty code as a
// no-op regardless of the enabled flag.
func TestColorizeGating(t *testing.T) {
	if got := Colorize(true, Cyan, "/help"); got != Cyan+"/help"+Reset {
		t.Errorf("enabled: got %q", got)
	}
	if got := Colorize(false, Cyan, "/help"); got != "/help" {
		t.Errorf("disabled should be plain, got %q", got)
	}
	if got := Colorize(true, "", "/help"); got != "/help" {
		t.Errorf("empty code should be plain, got %q", got)
	}
}

// TestColorEnabledRespectsNoColor verifies NO_COLOR forces color off even on a
// terminal (对标 https://no-color.org).
func TestColorEnabledRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if Enabled() {
		t.Error("NO_COLOR set: Enabled must be false")
	}
}
