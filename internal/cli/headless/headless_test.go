package headless

// Tests for the headless run driver's flag parsing. The run lifecycle (Run) is
// exercised via session/subagent tests here and provider-backed tests in
// internal/runtime; ParseOutputMode is pure and pinned directly.

import (
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
)

// TestParseOutputMode covers the three accepted spellings and one rejection,
// pinning the flag contract the headless driver depends on.
func TestParseOutputMode(t *testing.T) {
	cases := []struct {
		in      string
		want    runtime.HeadlessMode
		wantErr bool
	}{
		{"text", runtime.PrintMode, false},
		{"", runtime.PrintMode, false},
		{"stream-json", runtime.StreamJSONMode, false},
		{"yaml", 0, true},
	}
	for _, c := range cases {
		got, err := ParseOutputMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseOutputMode(%q): want error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseOutputMode(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseOutputMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
