package tui

import "testing"

func TestParseGitStatusDirtyCount(t *testing.T) {
	// Sample `git status --porcelain -b` output: header + 4 entries (staged,
	// modified, deleted, untracked).
	out := "## master...origin/master [ahead 4, behind 1]\n" +
		"M  cmd/pigo/main.go\n" +
		" M internal/foo.go\n" +
		"D  cmd/pigo/run.go\n" +
		"?? new_file.go\n"

	info := parseGitStatus(out)
	if !info.ok {
		t.Fatal("parseGitStatus should report ok=true")
	}
	if info.branch != "master" {
		t.Errorf("branch = %q, want master", info.branch)
	}
	if info.dirty != 4 {
		t.Errorf("dirty = %d, want 4", info.dirty)
	}
	if info.ahead != 4 {
		t.Errorf("ahead = %d, want 4", info.ahead)
	}
}

func TestParseGitStatusCleanTree(t *testing.T) {
	out := "## main...origin/main\n"
	info := parseGitStatus(out)
	if !info.ok {
		t.Fatal("ok should be true")
	}
	if info.branch != "main" {
		t.Errorf("branch = %q, want main", info.branch)
	}
	if info.dirty != 0 {
		t.Errorf("dirty = %d, want 0", info.dirty)
	}
	if info.ahead != 0 {
		t.Errorf("ahead = %d, want 0", info.ahead)
	}
}

func TestParseGitStatusNoUpstream(t *testing.T) {
	out := "## feature-x\n M a.go\n"
	info := parseGitStatus(out)
	if info.branch != "feature-x" {
		t.Errorf("branch = %q, want feature-x", info.branch)
	}
	if info.ahead != 0 {
		t.Errorf("ahead = %d, want 0", info.ahead)
	}
	if info.dirty != 1 {
		t.Errorf("dirty = %d, want 1", info.dirty)
	}
}

func TestParseGitStatusDetached(t *testing.T) {
	out := "## HEAD (no branch)\n"
	info := parseGitStatus(out)
	if info.branch != "HEAD (no branch)" {
		t.Errorf("branch = %q, want %q", info.branch, "HEAD (no branch)")
	}
}

func TestParseBranchHeaderAhead(t *testing.T) {
	branch, ahead := parseBranchHeader("## dev...origin/dev [ahead 12]")
	if branch != "dev" {
		t.Errorf("branch = %q, want dev", branch)
	}
	if ahead != 12 {
		t.Errorf("ahead = %d, want 12", ahead)
	}
}

func TestParseBranchHeaderBehindOnly(t *testing.T) {
	branch, ahead := parseBranchHeader("## dev...origin/dev [behind 3]")
	if branch != "dev" {
		t.Errorf("branch = %q, want dev", branch)
	}
	if ahead != 0 {
		t.Errorf("ahead = %d, want 0 (behind only)", ahead)
	}
}

func TestFetchGitCmdNonRepo(t *testing.T) {
	// A path that is not a git repository should collapse to ok=false. /tmp is
	// (almost) never a repo; if it somehow is on this host, skip.
	msg := fetchGitCmd("/")()
	git, ok := msg.(gitInfoMsg)
	if !ok {
		t.Fatalf("expected gitInfoMsg, got %T", msg)
	}
	if git.ok {
		t.Skip("host root unexpectedly reports a git repo; skipping")
	}
}
