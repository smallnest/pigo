// This file implements the manual `/dream` REPL command (SPEC §4.1, US-007):
// it spawns the process-isolated memory-consolidation subprocess
// (`pigo --dream [--dream-dry-run] -C <projectDir>`), captures the single-line
// Report JSON the child writes to stdout (SPEC §4.2), and renders it as a
// full-table change report. `--dry-run` runs the same analysis without writing
// (the subprocess enforces that; the command only reflects report.DryRun).
//
// The report renderers (RenderReportTable / RenderReportLine) live here rather
// than in internal/dream/report.go so the presentation layer stays in the CLI
// package and to keep dream internals conflict-free. RenderReportLine is
// exported because the startup background trigger (#526) reuses it for its
// one-line summary.
package repl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/dream"
)

// dreamSubprocessResult is the parsed outcome of one spawn: the decoded Report
// plus whatever the child wrote to stderr (surfaced in error messages so a
// failing run is diagnosable). It is returned by the spawn seam so the
// parse-and-render path can be unit-tested against canned JSON without spawning
// a real LLM-backed dream.
type dreamSubprocessResult struct {
	report dream.Report
	stderr string
}

// spawnDream is the seam that launches the dream subprocess and decodes its
// stdout Report. It is a package var so tests can substitute a canned stdout
// (avoiding a real LLM-backed run — see acceptance criteria). The production
// implementation is spawnDreamSubprocess.
var spawnDream = spawnDreamSubprocess

// spawnDreamSubprocess runs `pigo --dream [--dream-dry-run] -C <projectDir>` to
// completion, capturing stdout (the single-line Report JSON, SPEC §4.2) and
// stderr (progress/diagnostics). A non-zero exit or unparseable stdout is
// returned as an error carrying the stderr tail so the caller can print a clear
// failure (SPEC §6.1). It never mutates the REPL's own state.
func spawnDreamSubprocess(ctx context.Context, projectDir string, dryRun bool) (dreamSubprocessResult, error) {
	exe, err := os.Executable()
	if err != nil {
		return dreamSubprocessResult{}, fmt.Errorf("resolve pigo executable: %w", err)
	}
	args := []string{"--dream"}
	if dryRun {
		args = append(args, "--dream-dry-run")
	}
	if projectDir != "" {
		args = append(args, "-C", projectDir)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	errTail := strings.TrimSpace(stderr.String())
	if runErr != nil {
		// Exit 1 (or a kill/timeout) → failed (SPEC §6.1). Surface the stderr
		// tail so the user sees why.
		if errTail != "" {
			return dreamSubprocessResult{stderr: errTail}, fmt.Errorf("%w: %s", runErr, errTail)
		}
		return dreamSubprocessResult{}, runErr
	}
	var report dream.Report
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &report); err != nil {
		// Unparseable stdout is treated as failed (SPEC §6.1).
		return dreamSubprocessResult{stderr: errTail}, fmt.Errorf("parse report: %w", err)
	}
	return dreamSubprocessResult{report: report, stderr: errTail}, nil
}

// runDream handles an intercepted `/dream` (or `/dream --dry-run`) line: it
// checks for a live lock (so a background dream already running yields the
// "已有 dream 在运行" notice rather than a confusing empty report), spawns the
// consolidation subprocess with a progress indication, and renders the returned
// Report as a full table. A failed or unparseable run prints a clear error and
// returns to the prompt without crashing the REPL (SPEC §6.1).
func runDream(out io.Writer, deps replDeps, line string) {
	dryRun := dreamHasDryRun(line)

	// Pre-spawn lock check: if a background (or other) dream already holds a live
	// lock, the subprocess would just skip and emit an all-zero report,
	// indistinguishable from "nothing changed". Detect it here so the manual
	// command can tell the user (SPEC §6.1 locked row: "已有 dream 在运行").
	if dreamLockHeld(deps.memoryRoot) {
		fmt.Fprintln(out, ui.Colorize(ui.Enabled(), ui.Yellow, "已有 dream 在运行 (a dream consolidation is already running)"))
		return
	}

	progress := "Dreaming… (consolidating memory)"
	if dryRun {
		progress = "Dreaming… (dry-run, analyzing memory — nothing will be written)"
	}
	fmt.Fprintln(out, ui.Colorize(ui.Enabled(), ui.Dim, progress))

	// Bound the subprocess so a hung LLM-backed run cannot wedge the REPL
	// indefinitely (SPEC §6.3/§11.2: parent context timeout, default 10min). On
	// timeout CommandContext kills the child and spawnDream surfaces a failed run.
	ctx, cancel := context.WithTimeout(context.Background(), dreamRunTimeout)
	defer cancel()
	res, err := spawnDream(ctx, deps.cwd, dryRun)
	if err != nil {
		fmt.Fprintf(out, "%s %v\n", ui.Colorize(ui.Enabled(), ui.Red, "dream failed:"), err)
		return
	}
	RenderReportTable(out, res.report)
}

// dreamRunTimeout bounds a manual /dream subprocess (SPEC §6.3 default 10min).
var dreamRunTimeout = 10 * time.Minute

// dreamHasDryRun reports whether the /dream command line carries the --dry-run
// flag. It accepts "--dry-run" as a standalone token so "/dream --dry-run" and
// "/dream  --dry-run" both match, while a bare "/dream" does not.
func dreamHasDryRun(line string) bool {
	for _, f := range strings.Fields(line) {
		if f == "--dry-run" {
			return true
		}
	}
	return false
}

// dreamLockPayload mirrors the on-disk dream.lock body (SPEC §3.1:
// {"pid":..,"started_at":..}). It is decoded read-only in the parent to detect
// a running dream; the authoritative lock logic lives in internal/dream/lock.go.
type dreamLockPayload struct {
	StartedAt time.Time `json:"started_at"`
}

// dreamLockHeld reports whether a live (non-stale) dream lock exists under
// memoryRoot. A missing root/lock or a stale lock (older than
// dream.DefaultStaleAfter, matching the runner's takeover rule) reads as not
// held. Read-only: it never creates, removes, or takes over the lock — that is
// the subprocess Runner's job.
func dreamLockHeld(memoryRoot string) bool {
	if memoryRoot == "" {
		return false
	}
	path := filepath.Join(memoryRoot, "global", "dream", "dream.lock")
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var info dreamLockPayload
	if err := json.Unmarshal(data, &info); err != nil {
		// Malformed lock body: the runner treats it as stale/takeable, so it is
		// not a live lock from the user's perspective.
		return false
	}
	if info.StartedAt.IsZero() {
		return false
	}
	return time.Since(info.StartedAt) <= dream.DefaultStaleAfter
}

// RenderReportTable writes the full change report (SPEC §2.2/§6.1 manual row):
// one aligned row per counter, byte/file before→after, and any Notes. A dry-run
// report is clearly labeled DRY-RUN and states that nothing was written.
func RenderReportTable(out io.Writer, r dream.Report) {
	enabled := ui.Enabled()
	if r.DryRun {
		fmt.Fprintln(out, ui.Colorize(enabled, ui.Bold, "dream report [DRY-RUN — nothing written]"))
	} else {
		fmt.Fprintln(out, ui.Colorize(enabled, ui.Bold, "dream report"))
	}
	rows := []struct {
		label string
		value string
	}{
		{"merged", fmt.Sprintf("%d", r.Merged)},
		{"deduped", fmt.Sprintf("%d", r.Deduped)},
		{"paths-cleaned", fmt.Sprintf("%d", r.PathsCleaned)},
		{"pruned", fmt.Sprintf("%d", r.Pruned)},
		{"distilled", fmt.Sprintf("%d", r.Distilled)},
		{"bytes", fmt.Sprintf("%s → %s", formatBytes(r.BytesBefore), formatBytes(r.BytesAfter))},
		{"files", fmt.Sprintf("%d → %d", r.FilesBefore, r.FilesAfter)},
		{"reconciled", fmt.Sprintf("indexed %d, pruned %d", r.Reconciled.Indexed, r.Reconciled.Pruned)},
	}
	width := 0
	for _, row := range rows {
		if len(row.label) > width {
			width = len(row.label)
		}
	}
	for _, row := range rows {
		label := ui.Colorize(enabled, ui.Dim, fmt.Sprintf("  %-*s", width, row.label))
		fmt.Fprintf(out, "%s  %s\n", label, row.value)
	}
	if len(r.Notes) > 0 {
		fmt.Fprintln(out, ui.Colorize(enabled, ui.Dim, "  notes:"))
		for _, n := range r.Notes {
			fmt.Fprintf(out, "    - %s\n", n)
		}
	}
}

// RenderReportLine renders a compact one-line summary of a dream Report. It is
// exported so the startup background trigger (#526) can reuse it for its
// non-intrusive one-line notice (SPEC §6.1 background row). A dry-run report is
// prefixed [DRY-RUN].
func RenderReportLine(r dream.Report) string {
	prefix := "dream:"
	if r.DryRun {
		prefix = "dream [DRY-RUN]:"
	}
	return fmt.Sprintf("%s merged %d, deduped %d, paths-cleaned %d, pruned %d, distilled %d, %s→%s, %d→%d files",
		prefix, r.Merged, r.Deduped, r.PathsCleaned, r.Pruned, r.Distilled,
		formatBytes(r.BytesBefore), formatBytes(r.BytesAfter), r.FilesBefore, r.FilesAfter)
}

// formatBytes renders a byte count as a compact human-readable string (B/KB/MB).
// It uses 1024-based units and one decimal place above 1KB, matching the terse
// style of the rest of the REPL status output.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGT"[exp])
}
