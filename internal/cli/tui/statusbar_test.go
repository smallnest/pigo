package tui

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/ui"
)

// newTestStatusBar builds a status bar with a known cwd (already ~-abbreviated
// by the caller's intent) so tests do not depend on the real $HOME.
func newTestStatusBar() statusBar {
	opts := Options{Model: "claude-opus", ThinkingLevel: agentcore.ThinkingHigh}
	s := newStatusBar(DefaultTheme(), opts, "/tmp/project")
	s.cwd = "~/project"
	return s
}

func TestStatusBarRendersAllFields(t *testing.T) {
	s := newTestStatusBar()
	s.SetGit(gitInfoMsg{branch: "master", dirty: 3, ahead: 4, ok: true})
	s.SetTelemetry(telemetryEventView{util: 0.42, window: 200000})
	s.SetTask("running: Read")

	const width = 200
	out := s.Render(width)

	for _, want := range []string{
		"claude-opus",   // model
		"think:high",    // thinking level
		"~/project",     // cwd
		"master",        // git branch
		"*3",            // dirty marker
		"+4",            // ahead marker
		"ctx:42%",       // context usage
		"running: Read", // task
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got %q", want, out)
		}
	}

	if w := ui.Width(out); w > width {
		t.Errorf("render width %d exceeds terminal width %d", w, width)
	}
}

func TestStatusBarHidesGitWhenNotRepo(t *testing.T) {
	s := newTestStatusBar()
	s.SetGit(gitInfoMsg{ok: false})

	out := s.Render(120)
	if strings.Contains(out, "master") || strings.Contains(out, "*") || strings.Contains(out, "+") {
		t.Errorf("git segment should be hidden when ok=false; got %q", out)
	}
}

func TestStatusBarHidesContextWhenUnknown(t *testing.T) {
	s := newTestStatusBar()
	// No telemetry set (window 0) → context segment hidden.
	s.SetTelemetry(telemetryEventView{util: 0.5, window: 0})

	out := s.Render(120)
	if strings.Contains(out, "ctx:") {
		t.Errorf("context segment should be hidden when window unknown; got %q", out)
	}
}

func TestStatusBarTruncationKeepsPriorityFields(t *testing.T) {
	s := newTestStatusBar()
	s.SetGit(gitInfoMsg{branch: "master", dirty: 3, ahead: 4, ok: true})
	s.SetTelemetry(telemetryEventView{util: 0.42, window: 200000})
	s.SetTask("TASK")

	// Narrow width: only the highest-priority fields (task > model > token)
	// should survive; cwd and git should drop first.
	const width = 24
	out := s.Render(width)

	if w := ui.Width(out); w > width {
		t.Fatalf("truncated render width %d exceeds %d: %q", w, width, out)
	}
	if !strings.Contains(out, "TASK") {
		t.Errorf("highest-priority task field dropped under truncation: %q", out)
	}
	// cwd (lowest priority) must be gone before task.
	if strings.Contains(out, "~/project") {
		t.Errorf("lowest-priority cwd should drop first under truncation: %q", out)
	}
}

func TestStatusBarVeryNarrowNeverOverflows(t *testing.T) {
	s := newTestStatusBar()
	s.SetTask("a-fairly-long-task-description-that-cannot-fit")

	for _, width := range []int{1, 2, 3, 5, 8} {
		out := s.Render(width)
		if w := ui.Width(out); w > width {
			t.Errorf("width %d: render width %d overflows: %q", width, w, out)
		}
	}
}

func TestStatusBarZeroWidthEmpty(t *testing.T) {
	s := newTestStatusBar()
	if out := s.Render(0); out != "" {
		t.Errorf("zero width should render empty, got %q", out)
	}
}

func TestAbbreviateHome(t *testing.T) {
	home := homeDir()
	if home == "" {
		t.Skip("no home dir available")
	}
	if got := abbreviateHome(home); got != "~" {
		t.Errorf("abbreviateHome(home) = %q, want ~", got)
	}
	if got := abbreviateHome(home + "/foo/bar"); got != "~/foo/bar" {
		t.Errorf("abbreviateHome(home/foo/bar) = %q, want ~/foo/bar", got)
	}
	if got := abbreviateHome("/etc/passwd"); got != "/etc/passwd" {
		t.Errorf("abbreviateHome(/etc/passwd) = %q, want unchanged", got)
	}
}

// TestStatusBarGitTextFormatting checks the "*N +N" markers appear only when
// non-zero.
func TestStatusBarGitTextFormatting(t *testing.T) {
	s := newTestStatusBar()
	s.SetGit(gitInfoMsg{branch: "main", ok: true})
	out := s.Render(120)
	if strings.Contains(out, "*") || strings.Contains(out, "+") {
		t.Errorf("clean tree should show no *N/+N markers: %q", out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("branch name missing: %q", out)
	}
}
