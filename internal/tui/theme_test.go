package tui

import (
	"os"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// withNoColor sets/unsets NO_COLOR for the duration of a test and restores it.
func withNoColor(t *testing.T, set bool) {
	t.Helper()
	prev, had := os.LookupEnv("NO_COLOR")
	if set {
		os.Setenv("NO_COLOR", "1")
	} else {
		os.Unsetenv("NO_COLOR")
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("NO_COLOR", prev)
		} else {
			os.Unsetenv("NO_COLOR")
		}
	})
}

// TestBuildThemeColored verifies that with color enabled, styled text carries
// ANSI escape codes (i.e. the styles actually colorize). In lipgloss v2,
// Style.Render emits ANSI directly whenever a foreground color is set, so no
// global color-profile setup is needed.
func TestBuildThemeColored(t *testing.T) {
	withNoColor(t, false)

	for _, mode := range []themeMode{themeDark, themeLight} {
		th := buildTheme(mode)
		got := th.user.Render("hi")
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("mode %d: expected ANSI escape in colored render, got %q", mode, got)
		}
	}
}

// TestBuildThemeNoColor verifies NO_COLOR strips all ANSI codes: the rendered
// text must equal the raw input for every style.
func TestBuildThemeNoColor(t *testing.T) {
	withNoColor(t, true)
	th := buildTheme(themeDark)

	styles := map[string]lipgloss.Style{
		"user":       th.user,
		"assistant":  th.assistant,
		"toolCall":   th.toolCall,
		"toolResult": th.toolResult,
		"system":     th.system,
		"accent":     th.accent,
		"errorStyle": th.errorStyle,
	}
	for name, s := range styles {
		got := s.Render("text中文")
		if got != "text中文" {
			t.Errorf("NO_COLOR style %q: expected plain %q, got %q", name, "text中文", got)
		}
		if strings.Contains(got, "\x1b[") {
			t.Errorf("NO_COLOR style %q: unexpected ANSI escape in %q", name, got)
		}
	}
}
