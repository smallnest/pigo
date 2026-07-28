package tui

import (
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// gitInfoMsg is the result of an async git probe (fetchGitCmd). It is defined
// here rather than in msgs.go to avoid conflicting with the event-bridge message
// set (#387). The status bar consumes it to render the branch + working-tree
// state segment.
//
//   - branch: the current branch name (empty when detached / unknown).
//   - ahead:  commits the branch is ahead of its upstream (0 when unknown or no
//     upstream). Derived cheaply from `git status --porcelain -b`.
//   - dirty:  number of changed/untracked entries reported by `git status
//     --porcelain` (staged, unstaged, and untracked all count).
//   - ok:     false when the cwd is not a git repository or any git command
//     failed; the status bar hides the git segment in that case.
type gitInfoMsg struct {
	branch string
	ahead  int
	dirty  int
	ok     bool
}

// fetchGitCmd returns a tea.Cmd that probes the git working tree rooted at cwd
// and reports a gitInfoMsg. It runs read-only git plumbing with fixed arguments
// (no user interpolation, so no command-injection surface) off the tea
// goroutine. Any error — not a repo, git missing, detached parse failure —
// collapses to gitInfoMsg{ok:false}, which the status bar renders as "no git".
func fetchGitCmd(cwd string) tea.Cmd {
	return func() tea.Msg {
		// One porcelain call with the branch header gives us branch name, ahead
		// count, and every dirty/untracked entry in a single stable, parseable
		// format (-z would drop the header line, so we keep the newline form).
		out, err := runGit(cwd, "status", "--porcelain", "-b")
		if err != nil {
			return gitInfoMsg{ok: false}
		}
		return parseGitStatus(out)
	}
}

// runGit executes a read-only git subcommand in dir and returns its stdout. The
// argument list is always caller-controlled constants (see fetchGitCmd), never
// user input.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// parseGitStatus parses the output of `git status --porcelain -b` into a
// gitInfoMsg. The first line is the branch header ("## branch...upstream [ahead N,
// behind M]"); every subsequent non-empty line is one changed or untracked entry.
// A parsed result always has ok=true, since reaching this point means git ran.
func parseGitStatus(out string) gitInfoMsg {
	info := gitInfoMsg{ok: true}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if i == 0 {
			info.branch, info.ahead = parseBranchHeader(line)
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		info.dirty++
	}
	return info
}

// parseBranchHeader extracts the branch name and ahead count from a porcelain
// branch header line, e.g.:
//
//	## master...origin/master [ahead 4, behind 1]
//	## feature-x
//	## HEAD (no branch)
//
// It returns the branch name and ahead count (0 when absent). A line that is not
// a branch header yields ("", 0).
func parseBranchHeader(line string) (string, int) {
	const prefix = "## "
	if !strings.HasPrefix(line, prefix) {
		return "", 0
	}
	rest := strings.TrimPrefix(line, prefix)

	// Split off the optional " [ahead N, behind M]" tracking suffix.
	branchPart := rest
	ahead := 0
	if idx := strings.Index(rest, " ["); idx >= 0 {
		branchPart = rest[:idx]
		ahead = parseAhead(rest[idx:])
	}

	// "## HEAD (no branch)" — detached; keep the raw token as the branch label.
	branchPart = strings.TrimSpace(branchPart)

	// Trim the "...upstream" tracking-branch tail if present.
	if idx := strings.Index(branchPart, "..."); idx >= 0 {
		branchPart = branchPart[:idx]
	}
	return branchPart, ahead
}

// parseAhead pulls the integer following "ahead " out of a tracking suffix such
// as "[ahead 4, behind 1]". It returns 0 when no ahead count is present.
func parseAhead(suffix string) int {
	const marker = "ahead "
	idx := strings.Index(suffix, marker)
	if idx < 0 {
		return 0
	}
	digits := suffix[idx+len(marker):]
	n := 0
	found := false
	for _, r := range digits {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		found = true
	}
	if !found {
		return 0
	}
	return n
}
