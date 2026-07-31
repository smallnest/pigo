package remotecontrol

import (
	"strings"
	"testing"
)

func TestRenderProducesBlockOutput(t *testing.T) {
	out, err := Render("http://192.168.1.42:8080/pair?t=deadbeef")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out == "" {
		t.Fatal("Render returned empty output")
	}
	// Output must consist only of the half-block glyphs, spaces, and newlines.
	for _, r := range out {
		switch r {
		case '█', '▀', '▄', ' ', '\n':
		default:
			t.Fatalf("unexpected rune %q in QR output", r)
		}
	}
	// Must contain at least one dark-bearing glyph (not all blanks).
	if !strings.ContainsAny(out, "▀▄ ") {
		t.Fatal("QR output has no dark modules")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	const url = "http://10.0.0.9:5000/pair?t=abc123"
	a, err := Render(url)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	b, err := Render(url)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if a != b {
		t.Fatal("Render is not deterministic for the same URL")
	}
}

func TestRenderSquareRows(t *testing.T) {
	out, err := Render("http://127.0.0.1:1/pair?t=x")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("no lines")
	}
	width := len([]rune(lines[0]))
	for i, ln := range lines {
		if got := len([]rune(ln)); got != width {
			t.Fatalf("line %d width = %d, want uniform %d", i, got, width)
		}
	}
}
