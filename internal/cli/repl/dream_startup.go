// This file wires the startup background trigger for /dream memory
// consolidation (US-008, FR-4/FR-17): when an interactive REPL session starts,
// if [dream].enabled and a consolidation is due (per dream.State.Due), the dream
// subprocess is spawned in the BACKGROUND so it never delays the first user
// response, and a non-intrusive one-line summary is printed on completion.
//
// The decision + goroutine live in dream.Scheduler; this file only supplies the
// CLI-side spawn seam (reusing spawnDream from dream_repl.go) and the one-line
// notice renderer (RenderReportLine). The subprocess's O_EXCL lock enforces
// single-instance, so a second trigger just yields a skipped child (silent).
package repl

import (
	"context"
	"fmt"
	"io"

	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/dream"
)

// dreamStartupScheduler owns the startup auto-trigger decision. It is stateless
// (dream.Scheduler is a zero-size type), so a package value is enough; tests
// exercise this path through the spawnDream seam rather than replacing it.
var dreamStartupScheduler dream.Scheduler

// maybeStartBackgroundDream launches an auto-consolidation in the background at
// interactive session startup when dream is enabled and due. It never blocks:
// the due check is a single state.json read and any spawn runs in a goroutine,
// so the first prompt is served immediately (SPEC FR-4 / §8.2). The completion
// notice is a single dim line via RenderReportLine; a skipped, no-op, or failed
// run prints nothing (SPEC §6.1).
//
// out is written to from the background goroutine, so callers must pass a writer
// safe for a late async line (the REPL uses os.Stdout, where a one-line notice
// simply appears in scrollback). It is a no-op when memoryRoot is empty (dream
// state has nowhere to live) or dream is disabled/not due.
func maybeStartBackgroundDream(out io.Writer, memoryRoot, projectDir string, cfg dream.Config) bool {
	if memoryRoot == "" {
		return false
	}
	return dreamStartupScheduler.MaybeRunBackground(context.Background(), dream.BackgroundDeps{
		MemoryRoot: memoryRoot,
		ProjectDir: projectDir,
		Config:     cfg,
		Spawn: func(ctx context.Context, dir string) (dream.Report, error) {
			// Bound the background run like a manual /dream (SPEC §6.3): a hung
			// LLM-backed pass is killed rather than leaking a goroutine forever.
			ctx, cancel := context.WithTimeout(ctx, dreamRunTimeout)
			defer cancel()
			res, err := spawnDream(ctx, dir, false)
			return res.report, err
		},
		OnReport: func(r dream.Report) {
			fmt.Fprintln(out, ui.Colorize(ui.Enabled(), ui.Dim, RenderReportLine(r)))
		},
	})
}
