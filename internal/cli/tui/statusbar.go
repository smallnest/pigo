package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// statusBar renders the persistent bottom line described in the SPEC (US-003,
// Section 5.1): model name, thinking level, cwd (with $HOME abbreviated to ~),
// git branch + dirty/ahead markers, context-usage %, and the current task text.
// It holds no styling of its own beyond the Theme's StatusBar style; all width
// fitting is done against ui.Width so CJK/emoji count as two columns.
//
// The component is a plain value: Update-side code copies it into the Model,
// mutates the exported-to-package snapshot fields via the setters, and calls
// Render(width) from View. It never performs I/O — the git probe lives in
// gitinfo.go and feeds it via SetGit.
type statusBar struct {
	theme Theme

	// Static-ish config sourced from Options.
	model    string
	thinking string

	// cwd is the launch directory with $HOME already abbreviated to "~".
	cwd string

	// git is the latest probe result; rendered only when git.ok is true.
	git gitInfoMsg

	// contextPct is the latest context-window utilization in percent [0,100],
	// derived from telemetryMsg (ContextUtilization * 100). -1 means unknown, so
	// the segment is hidden until the first telemetry arrives.
	contextPct int

	// task is the current activity text (e.g. the running tool or turn state).
	task string
}

// newStatusBar builds a status bar from the theme, resolved Options, and the
// launch directory. contextPct starts at -1 (unknown) so the token segment stays
// hidden until telemetry arrives.
func newStatusBar(theme Theme, opts Options, cwd string) statusBar {
	return statusBar{
		theme:      theme,
		model:      opts.Model,
		thinking:   string(opts.ThinkingLevel),
		cwd:        abbreviateHome(cwd),
		contextPct: -1,
	}
}

// SetGit stores the latest git probe result.
func (s *statusBar) SetGit(g gitInfoMsg) { s.git = g }

// SetTelemetry updates the context-usage percentage from a telemetry event.
// A zero/unknown window (ContextWindow == 0) leaves the segment hidden.
func (s *statusBar) SetTelemetry(ev telemetryEventView) {
	if ev.window <= 0 {
		s.contextPct = -1
		return
	}
	pct := int(ev.util*100 + 0.5)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	s.contextPct = pct
}

// SetTask records the current activity text shown at the far right / high
// priority slot of the bar.
func (s *statusBar) SetTask(task string) { s.task = task }

// telemetryEventView is the minimal projection of agentcore.TelemetryEvent the
// status bar needs, so the caller (model.go) adapts the event rather than this
// file depending on agentcore directly for a two-field read.
type telemetryEventView struct {
	util   float64
	window int
}

// segment is one labelled field of the bar together with its truncation
// priority. Higher priority survives longer when the terminal is too narrow.
type segment struct {
	text     string
	priority int // larger = kept longer under truncation
}

// Priority order (SPEC: task > model > token > git > cwd). Higher is more
// important and dropped/truncated last.
const (
	prioCwd   = 0
	prioGit   = 1
	prioToken = 2
	prioModel = 3
	prioTask  = 4
)

// Render lays the bar out to exactly the configured width. It joins the visible
// segments with " · " separators and, when the result would exceed the width,
// drops whole segments from lowest to highest priority, then truncates the
// remaining joined string with TruncateToWidth as a final guard. The returned
// string's display width (ui.Width) is always <= width. A non-positive width
// yields the empty string.
func (s statusBar) Render(width int) string {
	if width <= 0 {
		return ""
	}

	segs := s.segments()

	// Drop lowest-priority segments until the joined content fits, so that under
	// pressure we keep task > model > token > git > cwd.
	for len(segs) > 0 {
		joined := joinSegments(segs)
		if ui.Width(joined) <= width {
			return s.theme.StatusBar.Render(joined)
		}
		segs = dropLowest(segs)
	}

	// Even a single highest-priority segment overflows: hard-truncate it.
	return s.theme.StatusBar.Render(TruncateToWidth(s.highestText(), width))
}

// segments builds the ordered list of visible segments. Order in the slice is
// the left-to-right display order; priority governs truncation, not position.
func (s statusBar) segments() []segment {
	var segs []segment

	if s.model != "" {
		segs = append(segs, segment{text: s.model, priority: prioModel})
	}
	if s.thinking != "" {
		// Thinking rides with the model priority — it is cheap and contextual.
		segs = append(segs, segment{text: "think:" + s.thinking, priority: prioModel})
	}
	if s.cwd != "" {
		segs = append(segs, segment{text: s.cwd, priority: prioCwd})
	}
	if s.git.ok {
		segs = append(segs, segment{text: s.gitText(), priority: prioGit})
	}
	if s.contextPct >= 0 {
		segs = append(segs, segment{text: fmt.Sprintf("ctx:%d%%", s.contextPct), priority: prioToken})
	}
	if s.task != "" {
		segs = append(segs, segment{text: s.task, priority: prioTask})
	}
	return segs
}

// gitText formats the git segment, e.g. "master *3 +4": branch, then "*N" for N
// dirty entries and "+N" for N commits ahead, each shown only when non-zero.
func (s statusBar) gitText() string {
	var b strings.Builder
	b.WriteString(s.git.branch)
	if s.git.dirty > 0 {
		fmt.Fprintf(&b, " *%d", s.git.dirty)
	}
	if s.git.ahead > 0 {
		fmt.Fprintf(&b, " +%d", s.git.ahead)
	}
	return b.String()
}

// highestText returns the text of the highest-priority segment, used as the last
// thing standing when the terminal cannot even fit one full segment.
func (s statusBar) highestText() string {
	segs := s.segments()
	if len(segs) == 0 {
		return ""
	}
	best := segs[0]
	for _, seg := range segs[1:] {
		if seg.priority > best.priority {
			best = seg
		}
	}
	return best.text
}

// joinSegments renders the segments left-to-right joined by " · ".
func joinSegments(segs []segment) string {
	parts := make([]string, len(segs))
	for i, seg := range segs {
		parts[i] = seg.text
	}
	return strings.Join(parts, " · ")
}

// dropLowest removes one occurrence of the lowest-priority segment, preserving
// display order among the rest. It returns the shortened slice.
func dropLowest(segs []segment) []segment {
	if len(segs) == 0 {
		return segs
	}
	lowIdx := 0
	for i, seg := range segs {
		if seg.priority < segs[lowIdx].priority {
			lowIdx = i
		}
	}
	out := make([]segment, 0, len(segs)-1)
	out = append(out, segs[:lowIdx]...)
	out = append(out, segs[lowIdx+1:]...)
	return out
}

// abbreviateHome replaces a leading $HOME in path with "~" so the status bar
// stays compact. It leaves paths outside $HOME untouched and never fails.
func abbreviateHome(path string) string {
	home := homeDir()
	if home == "" || path == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// homeDir returns the user's home directory, or "" when it cannot be
// determined. Kept as a tiny wrapper so abbreviateHome stays testable.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
