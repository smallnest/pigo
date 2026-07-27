package runtime

// Tests for the prompt-template expansion engine (US-002, #332). Covers the
// positional/default/slice syntax from the acceptance criteria, the
// ${...}-before-bare-$ ordering invariant, and the no-placeholder append
// behavior that preserves ParseUserCommand's existing semantics.

import "testing"

func TestExpandTemplateNoPlaceholder(t *testing.T) {
	// No args: verbatim.
	if got := ExpandTemplate("Take a note", nil); got != "Take a note" {
		t.Errorf("no args: got %q", got)
	}
	if got := ExpandTemplate("Take a note", []string{}); got != "Take a note" {
		t.Errorf("empty args: got %q", got)
	}
	// With args: appended after a blank line, joined by spaces.
	if got := ExpandTemplate("Take a note", []string{"buy", "milk"}); got != "Take a note\n\nbuy milk" {
		t.Errorf("with args: got %q", got)
	}
}

func TestExpandTemplatePositional(t *testing.T) {
	args := []string{"Button", "click", "handler"}
	if got := ExpandTemplate("$1", args); got != "Button" {
		t.Errorf("$1 = %q, want Button", got)
	}
	if got := ExpandTemplate("$3", args); got != "handler" {
		t.Errorf("$3 = %q, want handler", got)
	}
	// Out of range -> empty.
	if got := ExpandTemplate("$5", args); got != "" {
		t.Errorf("$5 = %q, want empty", got)
	}
	// Multi-digit.
	if got := ExpandTemplate("$10", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}); got != "j" {
		t.Errorf("$10 = %q, want j", got)
	}
}

func TestExpandTemplateAllArgs(t *testing.T) {
	args := []string{"Button", "click", "handler"}
	if got := ExpandTemplate("$@", args); got != "Button click handler" {
		t.Errorf("$@ = %q", got)
	}
	if got := ExpandTemplate("$ARGUMENTS", args); got != "Button click handler" {
		t.Errorf("$ARGUMENTS = %q", got)
	}
}

func TestExpandTemplateDefaultPositional(t *testing.T) {
	// Present and non-empty -> the arg.
	if got := ExpandTemplate("${1:-default}", []string{"a"}); got != "a" {
		t.Errorf("present: got %q", got)
	}
	// Absent -> default.
	if got := ExpandTemplate("${1:-default}", nil); got != "default" {
		t.Errorf("absent: got %q", got)
	}
	// Present but empty -> default.
	if got := ExpandTemplate("${1:-default}", []string{""}); got != "default" {
		t.Errorf("empty: got %q", got)
	}
	// Default containing a $ is not re-expanded.
	if got := ExpandTemplate("${1:-pay $5}", nil); got != "pay $5" {
		t.Errorf("default literal $: got %q", got)
	}
}

func TestExpandTemplateDefaultAllArgs(t *testing.T) {
	if got := ExpandTemplate("${@:-default}", []string{"a", "b"}); got != "a b" {
		t.Errorf("present: got %q", got)
	}
	if got := ExpandTemplate("${@:-default}", nil); got != "default" {
		t.Errorf("absent: got %q", got)
	}
	if got := ExpandTemplate("${ARGUMENTS:-default}", []string{"x"}); got != "x" {
		t.Errorf("ARGUMENTS present: got %q", got)
	}
	if got := ExpandTemplate("${ARGUMENTS:-default}", nil); got != "default" {
		t.Errorf("ARGUMENTS absent: got %q", got)
	}
}

func TestExpandTemplateSlice(t *testing.T) {
	args := []string{"a", "b", "c", "d", "e"}
	// ${@:N}: from Nth onward.
	if got := ExpandTemplate("${@:2}", args); got != "b c d e" {
		t.Errorf("${@:2} = %q", got)
	}
	if got := ExpandTemplate("${@:1}", args); got != "a b c d e" {
		t.Errorf("${@:1} = %q", got)
	}
	// ${@:N:L}: L args starting at N.
	if got := ExpandTemplate("${@:2:2}", args); got != "b c" {
		t.Errorf("${@:2:2} = %q", got)
	}
	if got := ExpandTemplate("${@:1:3}", args); got != "a b c" {
		t.Errorf("${@:1:3} = %q", got)
	}
	// N beyond args -> empty.
	if got := ExpandTemplate("${@:9}", args); got != "" {
		t.Errorf("${@:9} = %q, want empty", got)
	}
	// L clamps to available.
	if got := ExpandTemplate("${@:3:100}", args); got != "c d e" {
		t.Errorf("${@:3:100} = %q, want c d e", got)
	}
	// N<1 -> empty.
	if got := ExpandTemplate("${@:0}", args); got != "" {
		t.Errorf("${@:0} = %q, want empty", got)
	}
}

func TestExpandTemplateOrderingNoDoubleMatch(t *testing.T) {
	// A bare $1 must NOT match inside ${1:-...}. With args present, ${1:-$2}
	// uses arg1 ("a"); the $2 default is never consulted nor expanded.
	if got := ExpandTemplate("${1:-$2}", []string{"a"}); got != "a" {
		t.Errorf("${1:-$2} with arg1=a: got %q, want a", got)
	}
	// With arg1 absent, the default "$2" is taken literally (not expanded to "").
	if got := ExpandTemplate("${1:-$2}", nil); got != "$2" {
		t.Errorf("${1:-$2} absent: got %q, want literal $2", got)
	}
	// Braced and bare coexist in one pass.
	if got := ExpandTemplate("${1:-x} and $2", []string{"a", "b"}); got != "a and b" {
		t.Errorf("coexist: got %q", got)
	}
}

func TestExpandTemplateExampleFromSpec(t *testing.T) {
	// $@ expands to ALL args joined (AC: "全部参数以单空格连接").
	if got := ExpandTemplate("echo $@", []string{"a", "b", "c"}); got != "echo a b c" {
		t.Errorf("$@ all-args: got %q", got)
	}
	// The pi "component" example: $1 for the name, features are the remaining
	// args expressed with the slice form ${@:2} (the right tool for "rest").
	tmpl := "Create a React component named $1 with features: ${@:2}"
	if got := ExpandTemplate(tmpl, []string{"Button", "click", "handler"}); got != "Create a React component named Button with features: click handler" {
		t.Errorf("component example: got %q", got)
	}
	// "Summarize the current state in ${1:-7} bullet points."
	bullets := "Summarize the current state in ${1:-7} bullet points."
	if got := ExpandTemplate(bullets, nil); got != "Summarize the current state in 7 bullet points." {
		t.Errorf("default bullets: got %q", got)
	}
	if got := ExpandTemplate(bullets, []string{"5"}); got != "Summarize the current state in 5 bullet points." {
		t.Errorf("explicit bullets: got %q", got)
	}
}

func TestExpandTemplateLiteralDollar(t *testing.T) {
	// A '$' not forming a placeholder is emitted literally. Such a template has
	// no placeholder, so args (if any) are appended per the no-placeholder rule.
	if got := ExpandTemplate("100$ off", nil); got != "100$ off" {
		t.Errorf("literal $ mid-string: got %q", got)
	}
	if got := ExpandTemplate("trailing$", nil); got != "trailing$" {
		t.Errorf("trailing $ no args: got %q", got)
	}
	// No placeholder + args -> append after a blank line.
	if got := ExpandTemplate("trailing$", []string{"x"}); got != "trailing$\n\nx" {
		t.Errorf("trailing $ with args: got %q", got)
	}
	// A literal '$' alongside a real placeholder: '$' before a space stays '$'.
	if got := ExpandTemplate("cost $ and $1", []string{"five"}); got != "cost $ and five" {
		t.Errorf("literal $ next to placeholder: got %q", got)
	}
}
