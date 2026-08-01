package repl

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/dream"
)

// sampleReport is a non-trivial Report used across the renderer tests: every
// counter is distinct so a mis-wired field is caught, and Notes/Reconciled are
// populated so their rendering is exercised too.
func sampleReport() dream.Report {
	r := dream.Report{
		Merged:       2,
		Deduped:      1,
		PathsCleaned: 3,
		Pruned:       4,
		Distilled:    5,
		BytesBefore:  4096,
		BytesAfter:   2048,
		FilesBefore:  9,
		FilesAfter:   7,
		Notes:        []string{"pruned stale entry X", "no new sessions"},
	}
	r.Reconciled.Indexed = 6
	r.Reconciled.Pruned = 1
	return r
}

func TestRenderReportTable(t *testing.T) {
	var buf bytes.Buffer
	RenderReportTable(&buf, sampleReport())
	got := buf.String()

	// Key counts must appear (label + value).
	for _, want := range []string{
		"dream report",
		"merged", "deduped", "paths-cleaned", "pruned", "distilled",
		"4.0KB → 2.0KB", // bytes before→after
		"9 → 7",         // files before→after
		"indexed 6, pruned 1",
		"pruned stale entry X",
		"no new sessions",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("full table missing %q\n---\n%s", want, got)
		}
	}
	// A non-dry-run report must NOT carry the DRY-RUN label.
	if strings.Contains(got, "DRY-RUN") {
		t.Errorf("non-dry-run table should not show DRY-RUN label:\n%s", got)
	}
}

func TestRenderReportTableDryRun(t *testing.T) {
	r := sampleReport()
	r.DryRun = true
	var buf bytes.Buffer
	RenderReportTable(&buf, r)
	got := buf.String()
	if !strings.Contains(got, "DRY-RUN") {
		t.Errorf("dry-run table must show DRY-RUN label:\n%s", got)
	}
	if !strings.Contains(got, "nothing written") {
		t.Errorf("dry-run table should state nothing was written:\n%s", got)
	}
}

func TestRenderReportLine(t *testing.T) {
	got := RenderReportLine(sampleReport())
	for _, want := range []string{
		"dream:",
		"merged 2", "deduped 1", "paths-cleaned 3", "pruned 4", "distilled 5",
		"4.0KB→2.0KB", "9→7 files",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("one-line summary missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "DRY-RUN") {
		t.Errorf("non-dry-run line should not show DRY-RUN: %q", got)
	}
}

func TestRenderReportLineDryRun(t *testing.T) {
	r := sampleReport()
	r.DryRun = true
	got := RenderReportLine(r)
	if !strings.Contains(got, "DRY-RUN") {
		t.Errorf("dry-run line must show DRY-RUN: %q", got)
	}
}

func TestRenderReportZeroValue(t *testing.T) {
	// The zero value is a valid "nothing changed" report and must render cleanly.
	var buf bytes.Buffer
	RenderReportTable(&buf, dream.Report{})
	if line := RenderReportLine(dream.Report{}); !strings.Contains(line, "merged 0") {
		t.Errorf("zero-value line should render zero counts: %q", line)
	}
	if !strings.Contains(buf.String(), "0B → 0B") {
		t.Errorf("zero-value table should render 0B → 0B:\n%s", buf.String())
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1048576, "1.0MB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDreamHasDryRun(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"/dream", false},
		{"/dream --dry-run", true},
		{"/dream  --dry-run", true},
		{"/dream --dryrun", false},
		{"/dream extra --dry-run", true},
	}
	for _, c := range cases {
		if got := dreamHasDryRun(c.line); got != c.want {
			t.Errorf("dreamHasDryRun(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

// TestRunDreamRendersCannedReport exercises the parse+render path via the spawn
// seam with a canned Report, without spawning a real LLM-backed dream.
func TestRunDreamRendersCannedReport(t *testing.T) {
	orig := spawnDream
	t.Cleanup(func() { spawnDream = orig })
	spawnDream = func(_ context.Context, _ string, dryRun bool) (dreamSubprocessResult, error) {
		r := sampleReport()
		r.DryRun = dryRun
		return dreamSubprocessResult{report: r}, nil
	}

	var buf bytes.Buffer
	runDream(&buf, replDeps{}, "/dream")
	got := buf.String()
	if !strings.Contains(got, "dream report") || !strings.Contains(got, "merged") {
		t.Errorf("runDream should render the full table:\n%s", got)
	}
	if strings.Contains(got, "DRY-RUN") {
		t.Errorf("non-dry-run runDream should not show DRY-RUN:\n%s", got)
	}
}

func TestRunDreamDryRunLabel(t *testing.T) {
	orig := spawnDream
	t.Cleanup(func() { spawnDream = orig })
	spawnDream = func(_ context.Context, _ string, dryRun bool) (dreamSubprocessResult, error) {
		r := sampleReport()
		r.DryRun = dryRun
		return dreamSubprocessResult{report: r}, nil
	}
	var buf bytes.Buffer
	runDream(&buf, replDeps{}, "/dream --dry-run")
	if !strings.Contains(buf.String(), "DRY-RUN") {
		t.Errorf("/dream --dry-run should render DRY-RUN label:\n%s", buf.String())
	}
}

// TestRunDreamFailure asserts a subprocess failure (exit 1 / unparseable stdout)
// prints a clear error and does not crash the REPL (SPEC §6.1).
func TestRunDreamFailure(t *testing.T) {
	orig := spawnDream
	t.Cleanup(func() { spawnDream = orig })
	spawnDream = func(_ context.Context, _ string, _ bool) (dreamSubprocessResult, error) {
		return dreamSubprocessResult{}, errFake
	}
	var buf bytes.Buffer
	runDream(&buf, replDeps{}, "/dream")
	if !strings.Contains(buf.String(), "dream failed") {
		t.Errorf("failed run should print an error:\n%s", buf.String())
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "boom" }

func TestDreamLockHeld(t *testing.T) {
	// No memory root → never held.
	if dreamLockHeld("") {
		t.Fatal("empty memoryRoot must read as not held")
	}
	root := t.TempDir()
	// No lock file yet → not held.
	if dreamLockHeld(root) {
		t.Fatal("missing lock must read as not held")
	}
	// Acquire a real lock via the dream package → held.
	lock, err := dream.AcquireLock(root)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if !dreamLockHeld(root) {
		t.Error("a freshly acquired lock must read as held")
	}
	// Release → not held.
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if dreamLockHeld(root) {
		t.Error("a released lock must read as not held")
	}
}

// TestRunDreamLockedNotice asserts the manual command surfaces the locked
// message and does NOT spawn when a live lock is present (SPEC §6.1).
func TestRunDreamLockedNotice(t *testing.T) {
	root := t.TempDir()
	lock, err := dream.AcquireLock(root)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	orig := spawnDream
	t.Cleanup(func() { spawnDream = orig })
	spawned := false
	spawnDream = func(_ context.Context, _ string, _ bool) (dreamSubprocessResult, error) {
		spawned = true
		return dreamSubprocessResult{}, nil
	}
	var buf bytes.Buffer
	runDream(&buf, replDeps{memoryRoot: root}, "/dream")
	if spawned {
		t.Error("runDream must not spawn while a live lock is held")
	}
	if !strings.Contains(buf.String(), "已有 dream 在运行") {
		t.Errorf("locked run should print the locked notice:\n%s", buf.String())
	}
}
