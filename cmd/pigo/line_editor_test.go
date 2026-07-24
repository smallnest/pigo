package main

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
)

func testLineEditor(history ...string) *replLineEditor {
	reg := runtime.NewSlashRegistry()
	reg.AddBuiltin(runtime.SlashCommand{Name: "model", Action: func(string) string { return "" }})
	reg.AddBuiltin(runtime.SlashCommand{Name: "models", Action: func(string) string { return "" }})
	return newREPLLineEditor(strings.NewReader(""), bufio.NewReader(strings.NewReader("")), io.Discard, reg, history)
}

func TestLineEditorPrefersMostRecentMatchingInput(t *testing.T) {
	e := testLineEditor("explain old", "other", "explain recent")
	if got := e.suggestion("exp"); got != "explain recent" {
		t.Fatalf("suggestion = %q, want most recent match", got)
	}
}

func TestLineEditorCompletesSlashCommands(t *testing.T) {
	e := testLineEditor()
	if got := e.suggestion("/mod"); got != "/model" {
		t.Fatalf("suggestion = %q, want /model", got)
	}
}

func TestLineEditorCompletesModelsByRecentUseAndBasename(t *testing.T) {
	e := testLineEditor("/model openai/gpt-4o")
	if got := e.suggestion("/model "); got != "/model openai/gpt-4o" {
		t.Fatalf("empty model suggestion = %q", got)
	}
	if got := e.suggestion("/model gpt"); got != "/model openai/gpt-4o" {
		t.Fatalf("recent model suggestion = %q", got)
	}
	e = testLineEditor()
	got := e.suggestion("/model deepseek")
	if got == "" || !strings.HasPrefix(got, "/model ") {
		t.Fatalf("catalog model suggestion = %q", got)
	}
}

func TestLineEditorSuggestionsAreOrderedAndDeduped(t *testing.T) {
	// Two recent inputs plus a slash command all sharing a prefix: the caller
	// cycles this list with the arrow keys, so ordering (best first) and
	// dedup both matter.
	e := testLineEditor("explain old", "explain recent", "explain recent")
	cands := e.suggestions("exp")
	if len(cands) != 2 {
		t.Fatalf("suggestions = %v, want 2 unique candidates", cands)
	}
	if cands[0] != "explain recent" || cands[1] != "explain old" {
		t.Fatalf("suggestions = %v, want most-recent first", cands)
	}
	// The head of the list must match the single-suggestion helper.
	if e.suggestion("exp") != cands[0] {
		t.Fatalf("suggestion head %q != suggestions[0] %q", e.suggestion("exp"), cands[0])
	}
}

func TestLineEditorSlashCommandsCycleAllMatches(t *testing.T) {
	e := testLineEditor()
	cands := e.suggestions("/mode")
	// Both /model and /models share the prefix; cycling must expose both.
	if len(cands) != 2 || cands[0] != "/model" || cands[1] != "/models" {
		t.Fatalf("suggestions = %v, want [/model /models]", cands)
	}
}
