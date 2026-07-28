package repl

// Tests for the /models preset listing. presetListing renders the curated
// catalog for the REPL /models command. Provider-resolution tests moved to
// internal/provider with the resolution logic itself (US-004, #361).

import (
	"strings"
	"testing"
)

// TestPresetListingGroupsAndFilters verifies /models lists all providers by
// default and filters to one provider when given an argument.
func TestPresetListingGroupsAndFilters(t *testing.T) {
	all := presetListing("")
	for _, want := range []string{"openrouter", "nvidia", "ollama"} {
		if !strings.Contains(all, want) {
			t.Errorf("full listing missing provider %q:\n%s", want, all)
		}
	}
	// Filter to nvidia only: openrouter must not appear.
	nv := presetListing("nvidia")
	if !strings.Contains(nv, "nvidia") {
		t.Errorf("filtered listing missing nvidia:\n%s", nv)
	}
	if strings.Contains(nv, "openrouter") {
		t.Errorf("nvidia filter must not include openrouter:\n%s", nv)
	}
	// Unknown filter yields a helpful message, not a crash.
	if got := presetListing("bogus"); !strings.Contains(got, "no preset provider") {
		t.Errorf("unknown filter = %q, want a not-found message", got)
	}
}
