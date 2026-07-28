package ui

import "testing"

// TestWidthASCII pins that plain ASCII counts one cell per rune.
func TestWidthASCII(t *testing.T) {
	if got := Width("hello"); got != 5 {
		t.Errorf("Width(\"hello\") = %d, want 5", got)
	}
}

// TestWidthCJK pins that East Asian wide runes count as two cells, which is the
// whole reason we route width through lipgloss instead of len/RuneCount.
func TestWidthCJK(t *testing.T) {
	// Three CJK ideographs = 6 cells.
	if got := Width("达克克"); got != 6 {
		t.Errorf("Width(CJK x3) = %d, want 6", got)
	}
	// Mixed: "a达" = 1 + 2 = 3 cells.
	if got := Width("a达"); got != 3 {
		t.Errorf("Width(\"a达\") = %d, want 3", got)
	}
}

// TestWidthStripsANSI pins that SGR escape sequences do not add to the width, so
// a colored string aligns the same as its plain form.
func TestWidthStripsANSI(t *testing.T) {
	plain := "error"
	colored := Cyan + plain + Reset
	if got, want := Width(colored), Width(plain); got != want {
		t.Errorf("Width(colored) = %d, want %d (ANSI must not count)", got, want)
	}
}
