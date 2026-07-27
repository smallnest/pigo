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

func TestMLBufferEmptyBehavesLikeEmptyString(t *testing.T) {
	b := newMLBuffer()
	if !b.isEmpty() {
		t.Fatalf("fresh buffer should be empty")
	}
	if !b.single() {
		t.Fatalf("fresh buffer should be single-line")
	}
	if got := b.String(); got != "" {
		t.Fatalf("empty buffer String() = %q, want \"\"", got)
	}
}

func TestMLBufferSingleLineInsert(t *testing.T) {
	b := newMLBuffer()
	b.insert("hello")
	if got := b.String(); got != "hello" {
		t.Fatalf("String() = %q, want %q", got, "hello")
	}
	if b.col != 5 {
		t.Fatalf("col = %d, want 5", b.col)
	}
	if b.isEmpty() {
		t.Fatalf("buffer with text should not be empty")
	}
}

func TestMLBufferBackspaceRemovesLastRune(t *testing.T) {
	b := newMLBuffer()
	b.insert("héllo") // multi-byte rune to exercise UTF-8 handling
	b.backspace()
	if got := b.String(); got != "héll" {
		t.Fatalf("after backspace String() = %q, want %q", got, "héll")
	}
	// Remove the multi-byte rune specifically.
	b.setString("café")
	b.backspace()
	if got := b.String(); got != "caf" {
		t.Fatalf("multi-byte backspace String() = %q, want %q", got, "caf")
	}
}

func TestMLBufferBackspaceEmptyIsNoop(t *testing.T) {
	b := newMLBuffer()
	b.backspace()
	if got := b.String(); got != "" {
		t.Fatalf("backspace on empty buffer String() = %q, want \"\"", got)
	}
	if b.col != 0 || b.row != 0 {
		t.Fatalf("cursor moved on empty backspace: row=%d col=%d", b.row, b.col)
	}
}

func TestMLBufferSetStringJoinsWithNewline(t *testing.T) {
	b := newMLBuffer()
	b.setString("line1\nline2\nline3")
	if got := b.String(); got != "line1\nline2\nline3" {
		t.Fatalf("String() = %q, want round-trip", got)
	}
	if b.single() {
		t.Fatalf("multi-line buffer reported single()")
	}
	// Cursor lands at end of last line.
	if b.row != 2 || b.col != 5 {
		t.Fatalf("cursor = (%d,%d), want (2,5)", b.row, b.col)
	}
	// No trailing newline is introduced.
	if strings.HasSuffix(b.String(), "\n") {
		t.Fatalf("String() has trailing newline: %q", b.String())
	}
}

// editorWithInput builds an editor whose editLoop reads the given byte stream,
// so raw-mode key handling can be exercised without a real terminal.
func editorWithInput(input string, history ...string) *replLineEditor {
	reg := runtime.NewSlashRegistry()
	reg.AddBuiltin(runtime.SlashCommand{Name: "model", Action: func(string) string { return "" }})
	reg.AddBuiltin(runtime.SlashCommand{Name: "models", Action: func(string) string { return "" }})
	r := strings.NewReader(input)
	return newREPLLineEditor(r, bufio.NewReader(r), io.Discard, reg, history)
}

func TestMLBufferNewlineSplitsAtCursor(t *testing.T) {
	b := newMLBuffer()
	b.insert("abcdef")
	b.col = 3 // cursor between "abc" and "def"
	b.newline()
	if got := b.String(); got != "abc\ndef" {
		t.Fatalf("newline split = %q, want %q", got, "abc\ndef")
	}
	if b.row != 1 || b.col != 0 {
		t.Fatalf("cursor after newline = (%d,%d), want (1,0)", b.row, b.col)
	}
}

func TestEditLoopPlainEnterSubmits(t *testing.T) {
	e := editorWithInput("abc\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "abc" {
		t.Fatalf("submitted %q, want %q", got, "abc")
	}
}

func TestEditLoopShiftEnterInsertsNewline(t *testing.T) {
	// CSI-u Shift+Enter is \x1b[13;2u; a bare \r then submits.
	e := editorWithInput("abc\x1b[13;2udef\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "abc\ndef" {
		t.Fatalf("submitted %q, want %q", got, "abc\ndef")
	}
}

func TestEditLoopModifyOtherKeysEnterInsertsNewline(t *testing.T) {
	// modifyOtherKeys Shift+Enter is \x1b[27;2;13~.
	e := editorWithInput("x\x1b[27;2;13~y\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "x\ny" {
		t.Fatalf("submitted %q, want %q", got, "x\ny")
	}
}

func TestEditLoopCSIuPlainEnterSubmits(t *testing.T) {
	// Unmodified Enter reported as CSI-u \x1b[13u must submit, not newline.
	e := editorWithInput("hi\x1b[13u")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "hi" {
		t.Fatalf("submitted %q, want %q", got, "hi")
	}
}

func TestMLBufferLeftRightCrossLines(t *testing.T) {
	b := newMLBuffer()
	b.setString("ab\ncd") // cursor at end of "cd" → (1,2)
	b.left()              // (1,1)
	b.left()              // (1,0)
	if b.row != 1 || b.col != 0 {
		t.Fatalf("after two lefts = (%d,%d), want (1,0)", b.row, b.col)
	}
	b.left() // cross to end of "ab" → (0,2)
	if b.row != 0 || b.col != 2 {
		t.Fatalf("left at line start = (%d,%d), want (0,2)", b.row, b.col)
	}
	b.right() // cross to start of "cd" → (1,0)
	if b.row != 1 || b.col != 0 {
		t.Fatalf("right at line end = (%d,%d), want (1,0)", b.row, b.col)
	}
	// Left at the very origin is a no-op.
	b.setString("x")
	b.home()
	b.left()
	if b.row != 0 || b.col != 0 {
		t.Fatalf("left at origin moved cursor: (%d,%d)", b.row, b.col)
	}
	// Right at the very end is a no-op.
	b.end()
	b.right()
	if b.row != 0 || b.col != 1 {
		t.Fatalf("right at end moved cursor: (%d,%d)", b.row, b.col)
	}
}

func TestMLBufferUpDownClampColumn(t *testing.T) {
	b := newMLBuffer()
	b.setString("long line\nhi") // cursor at end of "hi" → (1,2)
	b.up()                        // move to "long line", col stays 2
	if b.row != 0 || b.col != 2 {
		t.Fatalf("up = (%d,%d), want (0,2)", b.row, b.col)
	}
	b.end() // col = 9
	b.down()
	// Down to "hi" (len 2) clamps col from 9 to 2.
	if b.row != 1 || b.col != 2 {
		t.Fatalf("down clamp = (%d,%d), want (1,2)", b.row, b.col)
	}
	// Up on the first line is a no-op; down on the last line is a no-op.
	b.setString("a\nb")
	b.up()
	b.up()
	if b.row != 0 {
		t.Fatalf("up past first line: row=%d", b.row)
	}
	b.down()
	b.down()
	if b.row != 1 {
		t.Fatalf("down past last line: row=%d", b.row)
	}
}

func TestMLBufferHomeEnd(t *testing.T) {
	b := newMLBuffer()
	b.setString("héllo") // multi-byte, 5 runes
	b.home()
	if b.col != 0 {
		t.Fatalf("home col = %d, want 0", b.col)
	}
	b.end()
	if b.col != 5 {
		t.Fatalf("end col = %d, want 5", b.col)
	}
}

func TestEditLoopLeftArrowThenInsert(t *testing.T) {
	// Type "abc", move left once (\x1b[D), insert "X", submit → "abXc".
	e := editorWithInput("abc\x1b[DX\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "abXc" {
		t.Fatalf("submitted %q, want %q", got, "abXc")
	}
}

func TestEditLoopUpArrowEditsPreviousLine(t *testing.T) {
	// "a" + Shift+Enter + "b", then up-arrow to line 0, End, insert "Z":
	// line 0 becomes "aZ", line 1 stays "b" → "aZ\nb".
	e := editorWithInput("a\x1b[13;2ub\x1b[A\x1b[FZ\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "aZ\nb" {
		t.Fatalf("submitted %q, want %q", got, "aZ\nb")
	}
}

func TestMLBufferInsertMidLine(t *testing.T) {
	b := newMLBuffer()
	b.setString("abc")
	b.home()
	b.right() // cursor between "a" and "bc"
	b.insert("X")
	if got := b.String(); got != "aXbc" {
		t.Fatalf("mid-line insert = %q, want %q", got, "aXbc")
	}
	if b.col != 2 {
		t.Fatalf("col after insert = %d, want 2", b.col)
	}
}

func TestMLBufferBackspaceMergesLines(t *testing.T) {
	b := newMLBuffer()
	b.setString("ab\ncd") // cursor at (1,2)
	b.home()              // cursor at (1,0)
	b.backspace()         // merge line 1 into line 0
	if got := b.String(); got != "abcd" {
		t.Fatalf("merge = %q, want %q", got, "abcd")
	}
	if b.row != 0 || b.col != 2 {
		t.Fatalf("cursor after merge = (%d,%d), want (0,2)", b.row, b.col)
	}
	// Merge with a multi-byte previous line lands the cursor by rune count.
	b.setString("café\nx")
	b.home()
	b.backspace()
	if got := b.String(); got != "caféx" {
		t.Fatalf("multi-byte merge = %q, want %q", got, "caféx")
	}
	if b.col != 4 {
		t.Fatalf("cursor col after multi-byte merge = %d, want 4", b.col)
	}
}

func TestMLBufferBackspaceFirstLineCol0IsNoop(t *testing.T) {
	b := newMLBuffer()
	b.setString("abc")
	b.home()
	b.backspace()
	if got := b.String(); got != "abc" {
		t.Fatalf("col-0 backspace on first line = %q, want unchanged", got)
	}
	if b.row != 0 || b.col != 0 {
		t.Fatalf("cursor moved: (%d,%d)", b.row, b.col)
	}
}

func TestEditLoopCrossLineBackspaceMerges(t *testing.T) {
	// "ab" + Shift+Enter + "cd" → two lines; Home to line-1 start, backspace
	// merges into "abcd", submit.
	e := editorWithInput("ab\x1b[13;2ucd\x1b[H\x7f\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "abcd" {
		t.Fatalf("submitted %q, want %q", got, "abcd")
	}
}
