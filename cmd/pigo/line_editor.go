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
// line. It is a no-op at column 0 (line-merge across rows is handled by the
// cross-line editing story). Multi-byte runes are removed whole.
func (b *mlBuffer) backspace() {
	if b.col == 0 {
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
	render := func() {
		input := buf.String()
		s := visible()
		fmt.Fprintf(e.out, "\r\033[2K%s%s", prompt, input)
		if s != "" {
			if strings.HasPrefix(s, input) {
				suffix := s[len(input):]
				fmt.Fprintf(e.out, "\033[2m%s\033[0m\033[%dD", suffix, utf8.RuneCountInString(suffix))
			} else {
				fmt.Fprintf(e.out, "\033[2m → %s\033[0m\033[%dD", s, utf8.RuneCountInString(s)+3)
			}
		}
	}
	render()
	for {
		b, err := e.in.ReadByte()
		if err != nil {
			return buf.String(), err
		}
		switch b {
		case '\r', '\n':
			fmt.Fprint(e.out, "\r\n")
			return buf.String(), nil
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
					if code == 13 {
						if mod >= 2 {
							buf.newline()
							selected = 0
							histNav = -1
						} else {
							fmt.Fprint(e.out, "\r\n")
							return buf.String(), nil
						}
					}
				case '~': // modifyOtherKeys form: "27;<mod>;<code>~".
					parts := strings.Split(string(params), ";")
					if len(parts) == 3 && atoiDefault(parts[0], -1) == 27 {
						mod := atoiDefault(parts[1], 1)
						code := atoiDefault(parts[2], 0)
						if code == 13 {
							if mod >= 2 {
								buf.newline()
								selected = 0
								histNav = -1
							} else {
								fmt.Fprint(e.out, "\r\n")
								return buf.String(), nil
							}
						}
					}
				case 'C': // right arrow accepts
					if len(params) == 0 {
						if s := visible(); s != "" {
							buf.setString(s)
							selected = 0
							histNav = -1
						}
					}
				case 'A': // up arrow
					if len(params) == 0 {
						if buf.isEmpty() || histNav >= 0 {
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
						if histNav >= 0 {
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
