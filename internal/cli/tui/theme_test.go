package tui

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli/ui"
)

func TestDefaultThemeRenders(t *testing.T) {
	th := DefaultTheme()

	cases := map[string]string{
		"user":       th.User.Render("hi"),
		"assistant":  th.Assistant.Render("ok"),
		"system":     th.System.Render("note"),
		"toolHeader": th.ToolHeader.Render("Bash"),
		"toolBody":   th.ToolBody.Render("output"),
		"statusBar":  th.StatusBar.Render("status"),
		"accent":     th.Accent.Render("file.go"),
		"error":      th.Error.Render("boom"),
		"warn":       th.Warn.Render("careful"),
		"success":    th.Success.Render("done"),
	}
	for name, got := range cases {
		if got == "" {
			t.Errorf("style %s rendered empty output", name)
		}
	}
}

func TestWrapToWidthDisplayWidth(t *testing.T) {
	// Mix double-width CJK, an emoji, and ASCII.
	const input = "你好world世界🚀测试abc"
	const width = 6

	wrapped := WrapToWidth(input, width)

	// Reassembling the wrapped lines (minus the inserted newlines) must equal
	// the original: nothing is dropped or split inside a rune.
	if got := strings.ReplaceAll(wrapped, "\n", ""); got != input {
		t.Fatalf("wrap altered content: got %q want %q", got, input)
	}

	for _, line := range strings.Split(wrapped, "\n") {
		if w := ui.Width(line); w > width {
			t.Errorf("line %q has display width %d > %d", line, w, width)
		}
		// Guard against a mid-rune cut producing invalid UTF-8.
		if !isValidBoundary(line) {
			t.Errorf("line %q was cut inside a multibyte rune", line)
		}
	}
}

func TestWrapToWidthPreservesNewlines(t *testing.T) {
	out := WrapToWidth("ab\ncd", 10)
	if out != "ab\ncd" {
		t.Fatalf("wrap collapsed existing newlines: got %q", out)
	}
}

func TestWrapToWidthNonPositive(t *testing.T) {
	const s = "你好world"
	if got := WrapToWidth(s, 0); got != s {
		t.Errorf("width<=0 should return input unchanged, got %q", got)
	}
}

func TestTruncateToWidth(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
	}{
		{"cjk", "你好世界测试内容很长", 6},
		{"emoji", "🚀🚀🚀🚀🚀🚀", 5},
		{"mixed", "abc你好def世界🚀tail", 8},
		{"ascii", "helloworld", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateToWidth(tt.in, tt.width)
			if w := ui.Width(got); w > tt.width {
				t.Errorf("truncated %q to display width %d > %d (result %q)", tt.in, w, tt.width, got)
			}
			if !isValidBoundary(got) {
				t.Errorf("truncation cut inside a multibyte rune: %q", got)
			}
			// It must actually be a truncation: contain the ellipsis when the
			// input was wider than the budget.
			if ui.Width(tt.in) > tt.width && !strings.Contains(got, ellipsis) {
				t.Errorf("expected ellipsis in truncated result, got %q", got)
			}
		})
	}
}

func TestTruncateToWidthNoTruncationNeeded(t *testing.T) {
	const s = "你好"
	if got := TruncateToWidth(s, 10); got != s {
		t.Errorf("short string should be returned unchanged, got %q", got)
	}
}

func TestTruncateToWidthNonPositive(t *testing.T) {
	if got := TruncateToWidth("你好", 0); got != "" {
		t.Errorf("width<=0 should return empty string, got %q", got)
	}
}

// isValidBoundary reports whether s contains no invalid UTF-8, which would be
// the tell-tale of a cut inside a multibyte rune.
func isValidBoundary(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}
