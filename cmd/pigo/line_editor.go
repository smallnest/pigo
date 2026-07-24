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

func (e *replLineEditor) suggestion(input string) string {
	if input == "" {
		return ""
	}
	lower := strings.ToLower(input)
	if strings.HasPrefix(input, "/") && !strings.ContainsAny(input, " \t") {
		var commands []string
		for _, cmd := range e.slash.List() {
			commands = append(commands, "/"+cmd.Name)
		}
		sort.Strings(commands)
		for _, cmd := range commands {
			if strings.HasPrefix(strings.ToLower(cmd), lower) && cmd != input {
				return cmd
			}
		}
	}
	for i := len(e.history) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.ToLower(e.history[i]), lower) && e.history[i] != input {
			return e.history[i]
		}
	}
	if strings.HasPrefix(lower, "/model ") {
		query := strings.TrimSpace(input[len("/model "):])
		if query == "" {
			for i := len(e.history) - 1; i >= 0; i-- {
				if strings.HasPrefix(e.history[i], "/model ") {
					return e.history[i]
				}
			}
			if len(e.models) > 0 {
				return "/model " + e.models[0]
			}
			return ""
		}
		for i := len(e.history) - 1; i >= 0; i-- {
			h := e.history[i]
			if strings.HasPrefix(h, "/model ") {
				id := strings.TrimSpace(h[len("/model "):])
				if modelMatches(id, query) {
					return "/model " + id
				}
			}
		}
		for _, id := range e.models {
			if modelMatches(id, query) {
				return "/model " + id
			}
		}
		return ""
	}
	return ""
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
	render := func() {
		s := e.suggestion(input)
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
			if s := e.suggestion(input); s != "" {
				input = s
			}
		case 8, 127:
			if input != "" {
				_, size := utf8.DecodeLastRuneInString(input)
				input = input[:len(input)-size]
			}
		case 27:
			// Right arrow accepts the suggestion. Other escape sequences are
			// consumed and ignored so they never leak into the submitted text.
			b2, _ := e.in.ReadByte()
			b3, _ := e.in.ReadByte()
			if b2 == '[' && b3 == 'C' {
				if s := e.suggestion(input); s != "" {
					input = s
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
		}
		render()
	}
}
