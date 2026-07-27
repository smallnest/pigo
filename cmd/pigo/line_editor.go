package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

var errLineInterrupted = errors.New("line input interrupted")

// mlBuffer models the readLine input as one or more lines with a cursor at
// (row, col), col counted in runes within the current line. A fresh buffer
// holds a single empty line with the cursor at the origin. It is the data
// model the multi-line REPL editor (continuation, Shift+Enter, cross-line
// movement/editing, rendering, history) is built on; single-line input behaves
// exactly as a plain string with the cursor at its end.
type mlBuffer struct {
	lines []string
	row   int
	col   int
}

func newMLBuffer() *mlBuffer { return &mlBuffer{lines: []string{""}} }

// String joins the lines with "\n" for submission. A single empty line yields
// "" and there is never a trailing newline.
func (b *mlBuffer) String() string { return strings.Join(b.lines, "\n") }

// isEmpty reports whether the buffer is a single empty line.
func (b *mlBuffer) isEmpty() bool { return len(b.lines) == 1 && b.lines[0] == "" }

// single reports whether the buffer holds exactly one line.
func (b *mlBuffer) single() bool { return len(b.lines) == 1 }

// line returns the text of the current cursor line.
func (b *mlBuffer) line() string { return b.lines[b.row] }

// setString replaces the whole buffer with s (which may contain "\n"), placing
// the cursor at the end of the last line. Used when the entire input is swapped
// wholesale — accepting a suggestion, browsing history, or restoring a
// multi-line entry.
func (b *mlBuffer) setString(s string) {
	b.lines = strings.Split(s, "\n")
	b.row = len(b.lines) - 1
	b.col = utf8.RuneCountInString(b.lines[b.row])
}

// insert adds s (which must not contain "\n") at the cursor on the current
// line and advances col past it.
func (b *mlBuffer) insert(s string) {
	line := b.lines[b.row]
	off := runeOffset(line, b.col)
	b.lines[b.row] = line[:off] + s + line[off:]
	b.col += utf8.RuneCountInString(s)
}

// backspace deletes the rune immediately left of the cursor on the current
// line. At column 0 it merges the current line into the previous one (the
// cursor landing at the merge point), unless already on the first line, where
// it is a no-op. Multi-byte runes are removed whole.
func (b *mlBuffer) backspace() {
	if b.col == 0 {
		if b.row == 0 {
			return
		}
		prev := b.lines[b.row-1]
		b.col = utf8.RuneCountInString(prev)
		b.lines[b.row-1] = prev + b.lines[b.row]
		b.lines = append(b.lines[:b.row], b.lines[b.row+1:]...)
		b.row--
		return
	}
	line := b.lines[b.row]
	start := runeOffset(line, b.col-1)
	end := runeOffset(line, b.col)
	b.lines[b.row] = line[:start] + line[end:]
	b.col--
}

// newline splits the current line at the cursor, moving the text right of the
// cursor onto a fresh line below and placing the cursor at its start. It is how
// Shift+Enter (and, later, backslash continuation) turn one line into two.
func (b *mlBuffer) newline() {
	line := b.lines[b.row]
	off := runeOffset(line, b.col)
	head, tail := line[:off], line[off:]
	rest := append([]string{}, b.lines[b.row+1:]...)
	b.lines = append(b.lines[:b.row], head, tail)
	b.lines = append(b.lines, rest...)
	b.row++
	b.col = 0
}

// left moves the cursor one rune left, crossing to the end of the previous
// line when already at column 0. It is a no-op at the buffer origin.
func (b *mlBuffer) left() {
	if b.col > 0 {
		b.col--
	} else if b.row > 0 {
		b.row--
		b.col = utf8.RuneCountInString(b.lines[b.row])
	}
}

// right moves the cursor one rune right, crossing to the start of the next
// line when already at the end of the current line. It is a no-op at the end
// of the last line.
func (b *mlBuffer) right() {
	if b.col < utf8.RuneCountInString(b.lines[b.row]) {
		b.col++
	} else if b.row < len(b.lines)-1 {
		b.row++
		b.col = 0
	}
}

// up moves the cursor to the previous line, clamping the column to that line's
// length. It is a no-op on the first line.
func (b *mlBuffer) up() {
	if b.row > 0 {
		b.row--
		if n := utf8.RuneCountInString(b.lines[b.row]); b.col > n {
			b.col = n
		}
	}
}

// down moves the cursor to the next line, clamping the column to that line's
// length. It is a no-op on the last line.
func (b *mlBuffer) down() {
	if b.row < len(b.lines)-1 {
		b.row++
		if n := utf8.RuneCountInString(b.lines[b.row]); b.col > n {
			b.col = n
		}
	}
}

// home moves the cursor to the start of the current line.
func (b *mlBuffer) home() { b.col = 0 }

// end moves the cursor to the end of the current line.
func (b *mlBuffer) end() { b.col = utf8.RuneCountInString(b.lines[b.row]) }

// enterContinues collapses the current line's trailing backslash run and
// reports whether a pressed Enter should continue onto a new line instead of
// submitting. A run of k trailing backslashes pairs up as k/2 literal
// backslashes (each "\\" → one "\"); an odd run has one extra backslash that
// escapes the newline, so the caller inserts a continuation line. The run is
// always collapsed to k/2 backslashes, so the escaping "\" never survives into
// submitted text.
func (b *mlBuffer) enterContinues() bool {
	line := b.lines[b.row]
	k := 0
	for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
		k++
	}
	if k == 0 {
		return false
	}
	b.lines[b.row] = line[:len(line)-k] + strings.Repeat("\\", k/2)
	if n := utf8.RuneCountInString(b.lines[b.row]); b.col > n {
		b.col = n
	}
	return k%2 == 1
}

// visibleWidth returns the number of terminal columns s occupies, skipping ANSI
// CSI escape sequences so a colored prompt still aligns its continuation lines.
// Wide runes (CJK ideographs, fullwidth forms, most emoji) count as two columns.
func visibleWidth(s string) int {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
					i++
				}
			}
			if i < len(s) {
				i++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		w += runeWidth(r)
	}
	return w
}

// displayWidth returns the number of terminal columns the plain string s
// occupies, summing each rune's cell width. Unlike visibleWidth it does not
// strip ANSI escapes — callers pass already-plain buffer text.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// runeWidth reports how many terminal cells a rune occupies: 0 for combining /
// zero-width marks, 2 for East Asian wide and fullwidth characters (and most
// emoji), 1 otherwise. This is what keeps the cursor aligned when the line
// contains CJK text, where one rune spans two columns.
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case (r >= 0x0300 && r <= 0x036F), // combining diacritical marks
		(r >= 0x1AB0 && r <= 0x1AFF), // combining diacritical marks extended
		(r >= 0x1DC0 && r <= 0x1DFF), // combining diacritical marks supplement
		(r >= 0x20D0 && r <= 0x20FF), // combining marks for symbols
		(r >= 0xFE20 && r <= 0xFE2F), // combining half marks
		r == 0x200B:                  // zero width space
		return 0
	case (r >= 0x1100 && r <= 0x115F), // Hangul Jamo
		(r >= 0x2E80 && r <= 0x303E),   // CJK radicals, Kangxi, CJK symbols
		(r >= 0x3041 && r <= 0x33FF),   // Hiragana, Katakana, CJK compat
		(r >= 0x3400 && r <= 0x4DBF),   // CJK Ext A
		(r >= 0x4E00 && r <= 0x9FFF),   // CJK Unified Ideographs
		(r >= 0xA000 && r <= 0xA4CF),   // Yi
		(r >= 0xAC00 && r <= 0xD7A3),   // Hangul syllables
		(r >= 0xF900 && r <= 0xFAFF),   // CJK compat ideographs
		(r >= 0xFE10 && r <= 0xFE19),   // vertical forms
		(r >= 0xFE30 && r <= 0xFE6F),   // CJK compat forms
		(r >= 0xFF00 && r <= 0xFF60),   // fullwidth forms
		(r >= 0xFFE0 && r <= 0xFFE6),   // fullwidth signs
		(r >= 0x1F300 && r <= 0x1FAFF), // emoji & pictographs
		(r >= 0x20000 && r <= 0x3FFFD): // CJK Ext B and beyond
		return 2
	default:
		return 1
	}
}

// runeOffset converts a rune column into a byte offset within s.
func runeOffset(s string, col int) int {
	off := 0
	for i := 0; i < col && off < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[off:])
		off += size
	}
	return off
}

// replLineEditor adds a small shell-style editing layer without turning the
// line-oriented REPL back into a full-screen TUI. On terminals it shows the
// best completion in dim text as the user types. Pipes and tests keep using the
// ordinary buffered reader.
type replLineEditor struct {
	in       *bufio.Reader
	terminal *os.File
	out      io.Writer
	slash    *runtime.SlashRegistry
	history  []string // oldest to newest
	models   []string
}

func newREPLLineEditor(in io.Reader, buffered *bufio.Reader, out io.Writer, slash *runtime.SlashRegistry, history []string) *replLineEditor {
	e := &replLineEditor{in: buffered, out: out, slash: slash}
	if f, ok := in.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			e.terminal = f
		}
	}
	for _, h := range history {
		e.remember(h)
	}
	seen := map[string]bool{}
	for _, m := range provider.PresetCatalog {
		if !seen[m.ID] {
			e.models = append(e.models, m.ID)
			seen[m.ID] = true
		}
	}
	return e
}

func (e *replLineEditor) remember(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	e.history = append(e.history, line)
	if len(e.history) > 200 {
		e.history = e.history[len(e.history)-200:]
	}
}

// formatSlashAutocompleteLabel renders a slash command for the Tab-completion
// hint as "name <argument-hint> - description" (对标 pi's autocomplete). The
// argument-hint is shown verbatim (frontmatter supplies its own <angle>/
// [square] brackets); it and the description are omitted when absent, so a
// bare command renders as just its name.
func formatSlashAutocompleteLabel(cmd runtime.SlashCommand) string {
	label := cmd.Name
	if cmd.ArgumentHint != "" {
		label += " " + cmd.ArgumentHint
	}
	if cmd.Description != "" {
		label += " - " + cmd.Description
	}
	return label
}

// suggestion returns the single best completion for input, or "" when there is
// none. It is the head of the ordered candidate list (see suggestions).
func (e *replLineEditor) suggestion(input string) string {
	if cands := e.suggestions(input); len(cands) > 0 {
		return cands[0]
	}
	return ""
}

// suggestions returns every completion candidate for input, best first, so the
// caller can cycle through them with the arrow keys. Candidates are gathered in
// priority order — slash commands, then recent inputs, then the /model catalog
// — deduplicated, with the raw input itself excluded.
func (e *replLineEditor) suggestions(input string) []string {
	if input == "" {
		return nil
	}
	lower := strings.ToLower(input)

	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || s == input || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	if strings.HasPrefix(input, "/") && !strings.ContainsAny(input, " \t") {
		var commands []string
		for _, cmd := range e.slash.List() {
			commands = append(commands, "/"+cmd.Name)
		}
		sort.Strings(commands)
		for _, cmd := range commands {
			if strings.HasPrefix(strings.ToLower(cmd), lower) {
				add(cmd)
			}
		}
	}
	for i := len(e.history) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.ToLower(e.history[i]), lower) {
			add(e.history[i])
		}
	}
	if strings.HasPrefix(lower, "/model ") {
		query := strings.TrimSpace(input[len("/model "):])
		for i := len(e.history) - 1; i >= 0; i-- {
			h := e.history[i]
			if !strings.HasPrefix(h, "/model ") {
				continue
			}
			id := strings.TrimSpace(h[len("/model "):])
			if query == "" || modelMatches(id, query) {
				add(h)
			}
		}
		for _, id := range e.models {
			if query == "" || modelMatches(id, query) {
				add("/model " + id)
			}
		}
	}
	return out
}

func modelMatches(id, query string) bool {
	id, query = strings.ToLower(id), strings.ToLower(query)
	if strings.HasPrefix(id, query) {
		return true
	}
	if slash := strings.LastIndexByte(id, '/'); slash >= 0 {
		return strings.HasPrefix(id[slash+1:], query)
	}
	return false
}

// parseCSIParams splits a CSI-u parameter list ("<code>[;<mod>]") into the key
// code and modifier. A missing modifier defaults to 1 (no modifier).
func parseCSIParams(params []byte) (code, mod int) {
	parts := strings.Split(string(params), ";")
	code = atoiDefault(parts[0], 0)
	mod = 1
	if len(parts) > 1 {
		mod = atoiDefault(parts[1], 1)
	}
	return code, mod
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func (e *replLineEditor) readLine(prompt string) (string, error) {
	if e.terminal == nil {
		fmt.Fprint(e.out, prompt)
		return e.in.ReadString('\n')
	}
	probe := exec.Command("stty", "-g")
	probe.Stdin = e.terminal
	state, err := probe.CombinedOutput()
	if err != nil {
		fmt.Fprint(e.out, prompt)
		return e.in.ReadString('\n')
	}
	raw := exec.Command("stty", "raw", "-echo")
	raw.Stdin = e.terminal
	if err := raw.Run(); err != nil {
		fmt.Fprint(e.out, prompt)
		return e.in.ReadString('\n')
	}
	// Ask the terminal to report modified keys so Shift+Enter is distinguishable
	// from a bare Enter: enable xterm modifyOtherKeys level 1 and push a CSI-u
	// (fixterms/kitty) keyboard mode. Level 1 (not 2) reports only keys without
	// a standard encoding — so Shift+Enter is escaped while ordinary Tab/Enter
	// stay untouched. Terminals that ignore these simply never send the reports,
	// and the user falls back to backslash continuation.
	fmt.Fprint(e.out, "\x1b[>4;1m\x1b[>1u")
	defer func() {
		// Restore the terminal's key reporting before the stty state, so we
		// never leave it stuck in CSI-u/modifyOtherKeys mode on any exit path.
		fmt.Fprint(e.out, "\x1b[<u\x1b[>4;0m")
		restore := exec.Command("stty", strings.TrimSpace(string(state)))
		restore.Stdin = e.terminal
		_ = restore.Run()
	}()

	return e.editLoop(prompt)
}

// editLoop runs the raw-mode key-processing loop over e.in, kept separate from
// readLine's terminal setup so it can be driven by a programmable io.Reader in
// tests (no real TTY required). It returns the submitted text (lines joined by
// "\n") or an error.
func (e *replLineEditor) editLoop(prompt string) (string, error) {
	// buf models the input as a multi-line buffer with a cursor. For this
	// single-line editing layer the cursor stays at the end of the sole line,
	// so buf behaves exactly like the former input string; the buffer model is
	// what later cross-line editing/rendering is built on.
	buf := newMLBuffer()
	// selected indexes into the current candidate list. It advances with the
	// up/down arrows so the user can cycle through suggestions; it resets to 0
	// (the best match) whenever the input text changes, since the candidate list
	// is recomputed from scratch.
	selected := 0
	// histNav tracks the position while browsing prior inputs with the arrow
	// keys on a blank line: -1 means not browsing, otherwise it indexes into
	// e.history (oldest to newest). It resets to -1 whenever the user edits the
	// line, so history browsing is only active while stepping through entries.
	histNav := -1
	// visible returns the suggestion currently shown/accepted: the candidate at
	// the selected index, clamped to the available list.
	visible := func() string {
		cands := e.suggestions(buf.String())
		if len(cands) == 0 {
			return ""
		}
		if selected >= len(cands) {
			selected = len(cands) - 1
		}
		if selected < 0 {
			selected = 0
		}
		return cands[selected]
	}
	// promptW is the prompt's visible width; continuation lines are indented to
	// that column so every line's text starts at the same place, with a dim
	// marker standing in for the prompt.
	promptW := visibleWidth(prompt)
	contPrefix := strings.Repeat(" ", promptW)
	if promptW >= 2 {
		contPrefix = strings.Repeat(" ", promptW-2) + "\033[2m·\033[0m "
	}
	// prevCursorRow is the screen row (relative to the block's first line) the
	// cursor was left on by the previous render, so the next render can climb
	// back to the top of the block before clearing and redrawing it.
	prevCursorRow := 0
	render := func() {
		// Return to the top-left of the block drawn last time and clear it plus
		// anything below, so shrinking the buffer leaves no stale rows/chars.
		if prevCursorRow > 0 {
			fmt.Fprintf(e.out, "\033[%dA", prevCursorRow)
		}
		fmt.Fprint(e.out, "\r\033[J")
		for i, line := range buf.lines {
			if i == 0 {
				fmt.Fprintf(e.out, "%s%s", prompt, line)
			} else {
				fmt.Fprintf(e.out, "\r\n%s%s", contPrefix, line)
			}
		}
		// The dim completion hint only fits on a single line with the cursor at
		// its end, where it can't collide with continuation rows.
		if buf.single() && buf.col == utf8.RuneCountInString(buf.lines[0]) {
			if s := visible(); s != "" {
				input := buf.lines[0]
				if strings.HasPrefix(s, "/") {
					// Slash command: render the argument-hint + description label
					// (对标 pi autocomplete) instead of the bare name suffix.
					label := s
					if cmd, ok := e.slash.Lookup(s[1:]); ok {
						label = formatSlashAutocompleteLabel(cmd)
					}
					fmt.Fprintf(e.out, "\033[2m -> %s\033[0m", label)
				} else if strings.HasPrefix(s, input) {
					fmt.Fprintf(e.out, "\033[2m%s\033[0m", s[len(input):])
				} else {
					fmt.Fprintf(e.out, "\033[2m → %s\033[0m", s)
				}
			}
		}
		// Reposition to the logical (row, col): after the draw the cursor sits
		// at the end of the last line, so climb to the target row, then step
		// right past the prefix and the display width of the runes left of the
		// cursor (wide CJK runes span two columns, so count cells, not runes).
		if up := len(buf.lines) - 1 - buf.row; up > 0 {
			fmt.Fprintf(e.out, "\033[%dA", up)
		}
		fmt.Fprint(e.out, "\r")
		curLine := buf.lines[buf.row]
		cursorCells := displayWidth(curLine[:runeOffset(curLine, buf.col)])
		if col := promptW + cursorCells; col > 0 {
			fmt.Fprintf(e.out, "\033[%dC", col)
		}
		prevCursorRow = buf.row
	}
	// tryEnter handles a pressed Enter shared by the raw, CSI-u, and
	// modifyOtherKeys report paths: if the current line ends with an unescaped
	// backslash it continues onto a new line and reports submitted=false;
	// otherwise it emits the newline and reports the block as submitted.
	tryEnter := func() (string, bool) {
		if buf.enterContinues() {
			buf.end()
			buf.newline()
			selected = 0
			histNav = -1
			return "", false
		}
		// Move below the whole rendered block before the newline so the
		// submitted lines stay on screen and the next prompt starts clean.
		if down := len(buf.lines) - 1 - buf.row; down > 0 {
			fmt.Fprintf(e.out, "\033[%dB", down)
		}
		fmt.Fprint(e.out, "\r\n")
		return buf.String(), true
	}
	render()
	for {
		b, err := e.in.ReadByte()
		if err != nil {
			return buf.String(), err
		}
		switch b {
		case '\r', '\n':
			if res, done := tryEnter(); done {
				return res, nil
			}
		case 1: // Ctrl+A moves to line start.
			buf.home()
		case 5: // Ctrl+E moves to line end.
			buf.end()
		case 3: // Ctrl+C
			fmt.Fprint(e.out, "^C\r\n")
			return "", errLineInterrupted
		case 4: // Ctrl+D
			if buf.isEmpty() {
				fmt.Fprint(e.out, "\r\n")
				return "", io.EOF
			}
		case 9: // Tab accepts the visible suggestion.
			if s := visible(); s != "" {
				buf.setString(s)
				selected = 0
				histNav = -1
			}
		case 8, 127:
			buf.backspace()
			selected = 0
			histNav = -1
		case 27:
			// Parse a full CSI sequence so multi-parameter reports (CSI-u key
			// events like Shift+Enter's \x1b[13;2u) are handled, not just the
			// bare arrow sequences. → accepts the visible suggestion, ↑/↓ cycle
			// candidates or browse history on a blank line, and Enter reports
			// (code 13) either submit or insert a newline depending on the
			// modifier. Any other sequence is consumed and ignored so it never
			// leaks into the submitted text.
			b2, escErr := e.in.ReadByte()
			if escErr != nil {
				return buf.String(), escErr
			}
			if b2 == '[' {
				var params []byte
				var final byte
				for {
					c, cErr := e.in.ReadByte()
					if cErr != nil {
						return buf.String(), cErr
					}
					if c >= 0x40 && c <= 0x7e {
						final = c
						break
					}
					params = append(params, c)
				}
				switch final {
				case 'u': // CSI-u key report: "<code>[;<mod>]u".
					code, mod := parseCSIParams(params)
					ctrl := (mod-1)&4 != 0
					switch {
					case code == 13:
						if mod >= 2 {
							buf.newline()
							selected = 0
							histNav = -1
						} else if res, done := tryEnter(); done {
							return res, nil
						}
					case ctrl && code == 'd':
						// Ctrl+D: EOF on an empty line. Under the kitty keyboard
						// protocol (enabled via \x1b[>1u) the terminal reports it here
						// as a CSI-u event, not the raw 0x04 byte the case-4 arm handles.
						if buf.isEmpty() {
							fmt.Fprint(e.out, "\r\n")
							return "", io.EOF
						}
					case ctrl && code == 'c':
						// Ctrl+C: same story — delivered as a CSI-u report rather than
						// the raw 0x03 byte once the kitty keyboard mode is active.
						fmt.Fprint(e.out, "^C\r\n")
						return "", errLineInterrupted
					}
				case '~': // modifyOtherKeys ("27;<mod>;<code>~") or Home/End ("1~"/"4~").
					parts := strings.Split(string(params), ";")
					if len(parts) == 3 && atoiDefault(parts[0], -1) == 27 {
						mod := atoiDefault(parts[1], 1)
						code := atoiDefault(parts[2], 0)
						if code == 13 {
							if mod >= 2 {
								buf.newline()
								selected = 0
								histNav = -1
							} else if res, done := tryEnter(); done {
								return res, nil
							}
						}
					} else if len(parts) == 1 {
						switch atoiDefault(parts[0], -1) {
						case 1, 7: // Home
							buf.home()
						case 4, 8: // End
							buf.end()
						}
					}
				case 'C': // right arrow: accept a visible suggestion, else move the cursor
					if len(params) == 0 {
						if s := visible(); s != "" {
							buf.setString(s)
							selected = 0
							histNav = -1
						} else {
							buf.right()
						}
					}
				case 'D': // left arrow moves the cursor (cross-line at column 0)
					if len(params) == 0 {
						buf.left()
					}
				case 'H': // Home
					if len(params) == 0 {
						buf.home()
					}
				case 'F': // End
					if len(params) == 0 {
						buf.end()
					}
				case 'A': // up arrow
					if len(params) == 0 {
						if !buf.single() {
							buf.up()
						} else if buf.isEmpty() || histNav >= 0 {
							// Browse history: step toward older entries.
							if histNav < 0 {
								histNav = len(e.history)
							}
							if histNav > 0 {
								histNav--
								buf.setString(e.history[histNav])
								selected = 0
							}
						} else if n := len(e.suggestions(buf.String())); n > 0 {
							selected = (selected - 1 + n) % n
						}
					}
				case 'B': // down arrow
					if len(params) == 0 {
						if !buf.single() {
							buf.down()
						} else if histNav >= 0 {
							// Browse history: step toward newer entries; past the
							// newest, return to a blank line.
							if histNav < len(e.history)-1 {
								histNav++
								buf.setString(e.history[histNav])
							} else {
								histNav = -1
								buf.setString("")
							}
							selected = 0
						} else if n := len(e.suggestions(buf.String())); n > 0 {
							selected = (selected + 1) % n
						}
					}
				}
			}
		default:
			bytes := []byte{b}
			want := 1
			switch {
			case b&0xe0 == 0xc0:
				want = 2
			case b&0xf0 == 0xe0:
				want = 3
			case b&0xf8 == 0xf0:
				want = 4
			}
			for len(bytes) < want {
				next, readErr := e.in.ReadByte()
				if readErr != nil {
					return buf.String(), readErr
				}
				bytes = append(bytes, next)
			}
			buf.insert(string(bytes))
			selected = 0
			histNav = -1
		}
		render()
	}
}
