package runtime

// Tests for shell-style argument tokenization (US-001, #331). Covers the cases
// called out in the acceptance criteria: empty input, a single bare argument,
// double-quoted argument with internal space, single-quoted argument, mixed
// quoting, an unterminated quote (error), and leading/trailing whitespace.

import "testing"

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{}},
		{"only whitespace", "   ", []string{}},
		{"single bare arg", "Button", []string{"Button"}},
		{"two bare args", "a b", []string{"a", "b"}},
		{"double quoted preserves internal space", `Button "click handler"`, []string{"Button", "click handler"}},
		{"single quoted preserves internal space", `'a b'`, []string{"a b"}},
		{"mixed single and double quotes", `x "y z" 'p q' r`, []string{"x", "y z", "p q", "r"}},
		{"leading and trailing whitespace trimmed", `   hello world   `, []string{"hello", "world"}},
		{"quoted arg at boundaries", `"a b" c "d e"`, []string{"a b", "c", "d e"}},
		{"empty double quotes yield empty arg", `""`, []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := SplitArgs(c.in)
			if err != nil {
				t.Fatalf("SplitArgs(%q) returned unexpected error: %v", c.in, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("SplitArgs(%q) = %v (len %d), want %v (len %d)", c.in, got, len(got), c.want, len(c.want))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("SplitArgs(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestSplitArgsEmptyReturnsNonNil(t *testing.T) {
	got, err := SplitArgs("")
	if err != nil {
		t.Fatalf("SplitArgs(\"\") returned unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("SplitArgs(\"\") returned nil slice, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("SplitArgs(\"\") = %v, want empty", got)
	}
}

func TestSplitArgsUnclosedQuoteErrors(t *testing.T) {
	cases := []string{
		`"unterminated`,
		`'unterminated`,
		`foo "bar baz`,
	}
	for _, in := range cases {
		if _, err := SplitArgs(in); err == nil {
			t.Errorf("SplitArgs(%q) expected an error for unterminated quote, got nil", in)
		}
	}
}
