package dream

import (
	"context"
	"time"
)

// BackgroundSpawn launches one dream consolidation subprocess for projectDir and
// returns its decoded Report. Implementations live in the CLI layer (they shell
// out to `pigo --dream -C <projectDir>` and parse the stdout Report), so this
// package stays free of os/exec concerns and there is no import cycle back
// through the CLI. A non-nil error means the run failed; background failures are
// silent to the user (the subprocess itself records last_status="failed"), so
// MaybeRunBackground swallows the error rather than surfacing it.
type BackgroundSpawn func(ctx context.Context, projectDir string) (Report, error)

// BackgroundDeps carries everything MaybeRunBackground needs to decide on and run
// a startup auto-consolidation without pulling process/exec or presentation
// concerns into this package.
type BackgroundDeps struct {
	// MemoryRoot is the dream state root read to decide whether a run is due. It
	// must match the root the subprocess consolidates (dream.ResolveMemoryRoot),
	// so the parent's Due check sees the same last_run_at the child updates.
	MemoryRoot string
	// ProjectDir is the working directory attributed to the run (project scope).
	ProjectDir string
	// Config is the resolved [dream] configuration (enabled / interval).
	Config Config
	// Now supplies the current time for the due check; nil uses time.Now. It is a
	// seam so tests can drive Due deterministically.
	Now func() time.Time
	// Spawn launches the subprocess. When nil, MaybeRunBackground does nothing.
	Spawn BackgroundSpawn
	// OnReport is invoked (from the background goroutine) with the completed
	// report only when the run produced actual changes — worth a one-line notice.
	// A skipped run (another dream held the lock → all-zero report) or a no-op run
	// yields no call, keeping the trigger non-intrusive. Nil disables the notice.
	OnReport func(Report)
}

// Scheduler owns the startup auto-trigger decision (SPEC §2.1 Scheduler
// component). It is stateless: Due reads state.json on demand and
// MaybeRunBackground spawns at most one background run per call. The
// single-instance guarantee is enforced by the subprocess's O_EXCL lock, not
// here — a second trigger simply results in a skipped child.
type Scheduler struct{}

// Due reports whether an auto-triggered consolidation is warranted now. It is
// cheap by design (SPEC §8.2 zero-startup-overhead): when dream is disabled it
// returns immediately without touching the filesystem; otherwise it reads
// state.json once and defers to State.Due (which also returns false for a
// never-run zero LastRunAt, so the first-ever run is never auto-triggered).
func (Scheduler) Due(memoryRoot string, cfg Config, now time.Time) bool {
	if !cfg.Enabled {
		return false
	}
	st, _ := LoadState(memoryRoot)
	return st.Due(cfg, now)
}

// MaybeRunBackground checks (cheaply) whether a consolidation is due and, if so,
// spawns it in a detached goroutine and returns immediately — it never blocks
// the caller, so the first interactive response is never delayed (SPEC FR-4 /
// §8.2). It returns true when a background run was launched. When dream is
// disabled or not due it returns false after at most a single state.json read
// (no goroutine, no subprocess).
//
// On completion the goroutine surfaces a one-line notice via OnReport only for a
// run that changed something; a skipped run (lock held elsewhere → zero report),
// a no-op run, or a failed run is silent (SPEC §6.1 background row).
func (s Scheduler) MaybeRunBackground(ctx context.Context, deps BackgroundDeps) bool {
	if deps.Spawn == nil {
		return false
	}
	now := time.Now
	if deps.Now != nil {
		now = deps.Now
	}
	if !s.Due(deps.MemoryRoot, deps.Config, now()) {
		return false
	}
	go func() {
		rep, err := deps.Spawn(ctx, deps.ProjectDir)
		if err != nil {
			// Background failure: silent. The subprocess already recorded
			// last_status="failed"; we do not interrupt the user with an error.
			return
		}
		if deps.OnReport != nil && reportHasChanges(rep) {
			deps.OnReport(rep)
		}
	}()
	return true
}

// reportHasChanges reports whether r reflects any actual mutation. A background
// run that skipped (lock contention) or found nothing to do produces an all-zero
// report, which is not worth a startup notice.
func reportHasChanges(r Report) bool {
	return r.Merged > 0 || r.Deduped > 0 || r.PathsCleaned > 0 ||
		r.Pruned > 0 || r.Distilled > 0 ||
		r.Reconciled.Indexed > 0 || r.Reconciled.Pruned > 0
}
