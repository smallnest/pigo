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
