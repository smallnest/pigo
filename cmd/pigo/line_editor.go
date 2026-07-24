package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

var errLineInterrupted = errors.New("line input interrupted")

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
	defer func() {
		restore := exec.Command("stty", strings.TrimSpace(string(state)))
		restore.Stdin = e.terminal
		_ = restore.Run()
	}()

	var input string
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
		cands := e.suggestions(input)
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
			return input, err
		}
		switch b {
		case '\r', '\n':
			fmt.Fprint(e.out, "\r\n")
			return input, nil
		case 3: // Ctrl+C
			fmt.Fprint(e.out, "^C\r\n")
			return "", errLineInterrupted
		case 4: // Ctrl+D
			if input == "" {
				fmt.Fprint(e.out, "\r\n")
				return "", io.EOF
			}
		case 9: // Tab accepts the visible suggestion.
			if s := visible(); s != "" {
				input = s
				selected = 0
				histNav = -1
			}
		case 8, 127:
			if input != "" {
				_, size := utf8.DecodeLastRuneInString(input)
				input = input[:len(input)-size]
				selected = 0
				histNav = -1
			}
		case 27:
			// Arrow keys drive suggestion selection: → accepts the visible
			// suggestion, ↑/↓ cycle to the previous/next candidate. On a blank
			// line ↑/↓ instead browse prior inputs (most recent first). Any
			// other escape sequence is consumed and ignored so it never leaks
			// into the submitted text.
			b2, _ := e.in.ReadByte()
			b3, _ := e.in.ReadByte()
			if b2 == '[' {
				switch b3 {
				case 'C': // right arrow accepts
					if s := visible(); s != "" {
						input = s
						selected = 0
						histNav = -1
					}
				case 'A': // up arrow
					if input == "" || histNav >= 0 {
						// Browse history: step toward older entries.
						if histNav < 0 {
							histNav = len(e.history)
						}
						if histNav > 0 {
							histNav--
							input = e.history[histNav]
							selected = 0
						}
					} else if n := len(e.suggestions(input)); n > 0 {
						selected = (selected - 1 + n) % n
					}
				case 'B': // down arrow
					if histNav >= 0 {
						// Browse history: step toward newer entries; past the
						// newest, return to a blank line.
						if histNav < len(e.history)-1 {
							histNav++
							input = e.history[histNav]
						} else {
							histNav = -1
							input = ""
						}
						selected = 0
					} else if n := len(e.suggestions(input)); n > 0 {
						selected = (selected + 1) % n
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
					return input, readErr
				}
				bytes = append(bytes, next)
			}
			input += string(bytes)
			selected = 0
			histNav = -1
		}
		render()
	}
}
