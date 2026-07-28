package repl

import (
	"bufio"
	"bytes"
	"errors"
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

// editorWithOutput is editorWithInput but captures the editor's terminal
// output so raw-mode rendering (escape sequences) can be asserted.
func editorWithOutput(input string, out io.Writer, history ...string) *replLineEditor {
	reg := runtime.NewSlashRegistry()
	reg.AddBuiltin(runtime.SlashCommand{Name: "model", Action: func(string) string { return "" }})
	r := strings.NewReader(input)
	return newREPLLineEditor(r, bufio.NewReader(r), out, reg, history)
}

func TestVisibleWidthSkipsANSI(t *testing.T) {
	if w := visibleWidth("pigo> "); w != 6 {
		t.Fatalf("plain width = %d, want 6", w)
	}
	if w := visibleWidth("\033[2m·\033[0m "); w != 2 {
		t.Fatalf("dim marker width = %d, want 2 (marker + space)", w)
	}
	if w := visibleWidth("café> "); w != 6 {
		t.Fatalf("multi-byte width = %d, want 6 runes", w)
	}
}

func TestRuneAndDisplayWidthHandleWideRunes(t *testing.T) {
	// ASCII and Latin-1 accents are one cell; CJK ideographs and fullwidth
	// forms are two; combining marks are zero.
	if w := runeWidth('a'); w != 1 {
		t.Fatalf("width('a') = %d, want 1", w)
	}
	if w := runeWidth('é'); w != 1 {
		t.Fatalf("width('é') = %d, want 1", w)
	}
	if w := runeWidth('中'); w != 2 {
		t.Fatalf("width('中') = %d, want 2", w)
	}
	if w := runeWidth('́'); w != 0 { // combining acute accent
		t.Fatalf("width(combining) = %d, want 0", w)
	}
	// A mixed CJK/ASCII string sums per-cell.
	if w := displayWidth("你好a"); w != 5 {
		t.Fatalf("displayWidth(\"你好a\") = %d, want 5", w)
	}
	// visibleWidth (ANSI-stripping) agrees on wide runes.
	if w := visibleWidth("\033[2m中\033[0m"); w != 2 {
		t.Fatalf("visibleWidth wide = %d, want 2", w)
	}
}

func TestEditLoopCursorAccountsForWideRunes(t *testing.T) {
	// Typing a CJK char must move the cursor two cells, not one, so the final
	// reposition after "中" (prompt width 2 + 2 cells) lands at column 4.
	var out bytes.Buffer
	e := editorWithOutput("中", &out)
	// Drive one render by feeding the rune then EOF (no submit needed).
	if _, err := e.editLoop("> "); err == nil {
		t.Fatalf("expected EOF error from truncated input")
	}
	s := out.String()
	if !strings.Contains(s, "\033[4C") {
		t.Fatalf("cursor not repositioned to column 4 for wide rune:\n%q", s)
	}
	if strings.Contains(s, "\033[3C") {
		t.Fatalf("cursor used rune-count column 3 (wide rune miscounted):\n%q", s)
	}
}

func TestEditLoopRendersContinuationAndClears(t *testing.T) {
	// "foo" + Shift+Enter + "bar" builds a two-line buffer; the continuation
	// line is indented with the dim marker and each redraw clears the block.
	var out bytes.Buffer
	e := editorWithOutput("foo\x1b[13;2ubar\r", &out)
	got, err := e.editLoop("> ")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "foo\nbar" {
		t.Fatalf("submitted %q, want %q", got, "foo\nbar")
	}
	s := out.String()
	if !strings.Contains(s, "\033[2m·\033[0m bar") {
		t.Fatalf("output missing dim continuation prefix before %q:\n%q", "bar", s)
	}
	if !strings.Contains(s, "\033[J") {
		t.Fatalf("output never clears the block with \\033[J:\n%q", s)
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

func TestEditLoopCSIuCtrlDEmptyReturnsEOF(t *testing.T) {
	// Under the kitty keyboard protocol Ctrl+D on an empty line arrives as the
	// CSI-u report \x1b[100;5u (code 'd', ctrl modifier) rather than raw 0x04,
	// and must still exit with io.EOF.
	e := editorWithInput("\x1b[100;5u")
	got, err := e.editLoop("")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got err=%v got=%q", err, got)
	}
}

func TestEditLoopCSIuCtrlDNonEmptyIgnored(t *testing.T) {
	// Ctrl+D on a non-empty line is a no-op (matches the raw-byte behavior); the
	// following Enter submits the typed text.
	e := editorWithInput("ab\x1b[100;5u\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "ab" {
		t.Fatalf("submitted %q, want %q", got, "ab")
	}
}

func TestEditLoopCSIuCtrlCInterrupts(t *testing.T) {
	// Ctrl+C reported as CSI-u \x1b[99;5u must interrupt the line.
	e := editorWithInput("\x1b[99;5u")
	_, err := e.editLoop("")
	if !errors.Is(err, errLineInterrupted) {
		t.Fatalf("want errLineInterrupted, got %v", err)
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
	b.up()                       // move to "long line", col stays 2
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

func TestEditLoopBackslashContinues(t *testing.T) {
	// A line ending with a single unescaped "\" + Enter continues; a second
	// line + Enter submits the two-line block without the continuation "\".
	e := editorWithInput("foo\\\rbar\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "foo\nbar" {
		t.Fatalf("submitted %q, want %q", got, "foo\nbar")
	}
}

func TestEditLoopEscapedBackslashSubmits(t *testing.T) {
	// A line ending with "\\" (escaped) + Enter submits, keeping one literal
	// backslash — it does not continue.
	e := editorWithInput("foo\\\\\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "foo\\" {
		t.Fatalf("submitted %q, want %q", got, "foo\\")
	}
}

func TestEditLoopBackslashMultipleContinuations(t *testing.T) {
	// Three continued lines accumulate into a three-line block.
	e := editorWithInput("a\\\rb\\\rc\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "a\nb\nc" {
		t.Fatalf("submitted %q, want %q", got, "a\nb\nc")
	}
}

func TestMLBufferEnterContinuesTrailingRuns(t *testing.T) {
	// Odd run continues and collapses to k/2 backslashes; even run submits and
	// halves the run.
	b := newMLBuffer()
	b.setString("x\\") // one trailing backslash
	if !b.enterContinues() {
		t.Fatalf("single trailing backslash should continue")
	}
	if got := b.line(); got != "x" {
		t.Fatalf("line after continue = %q, want %q", got, "x")
	}
	b.setString("y\\\\") // two trailing backslashes
	if b.enterContinues() {
		t.Fatalf("escaped double backslash should not continue")
	}
	if got := b.line(); got != "y\\" {
		t.Fatalf("line after submit = %q, want %q", got, "y\\")
	}
	b.setString("z\\\\\\") // three trailing backslashes → continue, keep one
	if !b.enterContinues() {
		t.Fatalf("triple trailing backslash should continue")
	}
	if got := b.line(); got != "z\\" {
		t.Fatalf("line after triple continue = %q, want %q", got, "z\\")
	}
}

func TestRememberPreservesInternalNewlines(t *testing.T) {
	// A submitted multi-line block is stored as ONE history record; remember
	// trims only the outer whitespace, never the internal newlines.
	e := testLineEditor()
	e.remember("  foo\nbar  ")
	if len(e.history) != 1 {
		t.Fatalf("history = %v, want a single record", e.history)
	}
	if e.history[0] != "foo\nbar" {
		t.Fatalf("remembered %q, want %q (internal newline kept)", e.history[0], "foo\nbar")
	}
}

func TestEditLoopHistoryRestoresMultiLineAndSubmits(t *testing.T) {
	// Up-arrow on a blank line restores the newest history entry; a multi-line
	// record comes back as a multi-line buffer that submits intact with its
	// internal newline (i.e. the block is one message, not two).
	e := editorWithInput("\x1b[A\r", "foo\nbar")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "foo\nbar" {
		t.Fatalf("restored+submitted %q, want %q", got, "foo\nbar")
	}
}

func TestEditLoopMultiLineSubmitJoinsWithNewline(t *testing.T) {
	// Three Shift+Enter lines submit as one \n-joined message, so the full
	// multi-line string reaches the agent in a single turn.
	e := editorWithInput("a\x1b[13;2ub\x1b[13;2uc\r")
	got, err := e.editLoop("")
	if err != nil {
		t.Fatalf("editLoop error: %v", err)
	}
	if got != "a\nb\nc" {
		t.Fatalf("submitted %q, want %q", got, "a\nb\nc")
	}
}
