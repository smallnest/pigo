package main

// Tests for formatSlashAutocompleteLabel (US-010, #340): the Tab-completion
// label renders as "name <argument-hint> - description", omitting the hint
// segment when absent and falling back to the first body line for description.

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
)

func TestFormatSlashAutocompleteLabel(t *testing.T) {
	cases := []struct {
		name string
		cmd  runtime.SlashCommand
		want string
	}{
		{"hint+desc", runtime.SlashCommand{Name: "review", ArgumentHint: "<PR-URL>", Description: "Review PRs"}, "review <PR-URL> - Review PRs"},
		{"desc only", runtime.SlashCommand{Name: "review", Description: "Review PRs"}, "review - Review PRs"},
		{"hint only", runtime.SlashCommand{Name: "wr", ArgumentHint: "[instructions]"}, "wr [instructions]"},
		{"neither", runtime.SlashCommand{Name: "model"}, "model"},
	}
	for _, c := range cases {
		if got := formatSlashAutocompleteLabel(c.cmd); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestFormatSlashAutocompleteLabelDescriptionFallback verifies the description
// fallback from #334 (first non-empty body line) flows through to the label.
func TestFormatSlashAutocompleteLabelDescriptionFallback(t *testing.T) {
	cmd, err := runtime.ParseUserCommand("bare", []byte("First line is the desc\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if got := formatSlashAutocompleteLabel(cmd); got != "bare - First line is the desc" {
		t.Errorf("fallback label = %q, want \"bare - First line is the desc\"", got)
	}
}

// TestSlashAutocompleteSuggestionStillCompletesName verifies that a slash
// command with an argument-hint is still completable by name (Tab inserts
// "/name"); the label is a display annotation, not the inserted text.
func TestSlashAutocompleteSuggestionStillCompletesName(t *testing.T) {
	reg := runtime.NewSlashRegistry()
	reg.AddBuiltin(runtime.SlashCommand{Name: "model", Action: func(string) string { return "" }})
	reg.AddUser(runtime.SlashCommand{
		Name:          "review",
		ArgumentHint:  "<PR-URL>",
		Description:   "Review PRs",
		Expand:        func(string) string { return "" },
	})
	e := newREPLLineEditor(strings.NewReader(""), bufio.NewReader(strings.NewReader("")), io.Discard, reg, nil)
	if got := e.suggestion("/rev"); got != "/review" {
		t.Errorf("suggestion(/rev) = %q, want /review (name, not the label)", got)
	}
}
