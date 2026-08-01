package dream

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/memory"
)

// Consolidator is the LLM-driven apply step, injected so the deterministic
// Runner skeleton can run end-to-end without an LLM. It receives the plain-data
// plan (dedupe groups + invalid-path refs + near-dup candidate pairs) and
// returns the semantic decisions: which merged bodies to rewrite, which entries
// to delete (merged-away / pruned), and which new entries to distill.
//
// #523 will implement a real Consolidator backed by the main-session model and
// the dream system prompt (SPEC §5.1 step 5, §5.1.1). Until then the Runner uses
// nopConsolidator, so the deterministic half (exact dedup + path clean +
// Reconcile) is fully exercised and testable in isolation.
type Consolidator interface {
	Consolidate(ctx context.Context, in ConsolidateInput) (ConsolidateResult, error)
}

// ConsolidateInput is the plain-data view handed to the Consolidator. It carries
// the deterministic Plan (which already embeds the dedupe groups, invalid-path
// refs and near-dup pairs) plus the resolved roots so the implementation can
// compute in-scope write targets. It intentionally carries no behavior and no
// live handles (no *memory.Store, no *sql.DB) so it stays trivially serializable
// if #523 chooses to marshal it across a subprocess/RPC boundary.
type ConsolidateInput struct {
	Plan       Plan   `json:"plan"`
	MemoryRoot string `json:"memory_root"`
	ProjectDir string `json:"project_dir"`
}

// NewEntry is a distilled memory file the Consolidator wants created. Path must
// resolve within the memory root's global/project scope; the Runner rejects any
// out-of-scope target before writing (SPEC §5.2 / §7.1).
type NewEntry struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

// ConsolidateResult is the Consolidator's decision set. All paths are absolute
// and must lie within the memory root scope. Merged/Pruned/Distilled are the
// report counters the Runner surfaces verbatim; the deterministic Deduped and
// PathsCleaned counters are computed by the Runner itself, not here.
type ConsolidateResult struct {
	// MergedBodies maps an existing memory file path to its rewritten (merged /
	// compacted) body. The Runner overwrites each file in place.
	MergedBodies map[string]string `json:"merged_bodies,omitempty"`
	// Deletions are memory files to remove (entries merged away or pruned as
	// stale/contradictory).
	Deletions []string `json:"deletions,omitempty"`
	// NewEntries are freshly distilled memory files to create.
	NewEntries []NewEntry `json:"new_entries,omitempty"`

	Merged    int      `json:"merged"`
	Pruned    int      `json:"pruned"`
	Distilled int      `json:"distilled"`
	Notes     []string `json:"notes,omitempty"`
}

// nopConsolidator is the default no-op Consolidator used when none is injected.
// It makes no decisions, so a Runner with it performs only the deterministic
// dedup + path-clean + Reconcile pass — enough for the skeleton to run
// end-to-end and be unit-tested without an LLM.
type nopConsolidator struct{}

func (nopConsolidator) Consolidate(context.Context, ConsolidateInput) (ConsolidateResult, error) {
	return ConsolidateResult{}, nil
}

// Runner is the subprocess-side consolidation entry point (SPEC §2.2 / §5.1). It
// runs the deterministic plan, delegates semantic merge/prune/distill to an
// injected Consolidator, applies the results within the memory-root scope, and
// rebuilds the FTS index via memory.Reconcile. The zero value is usable: an
// empty MemoryRoot is resolved from the environment and a nil Consolidator falls
// back to nopConsolidator.
type Runner struct {
	// Consolidator is the injected LLM apply step; nil selects nopConsolidator.
	Consolidator Consolidator
	// MemoryRoot overrides the environment-resolved memory root. Empty means
	// resolve via ResolveMemoryRoot (PIGO_HOME / ~/.pigo). Tests set it to a temp
	// dir; production leaves it empty.
	MemoryRoot string
}

// RunOptions are the per-invocation parameters mirroring the CLI flags. ProjectDir
// selects the projects sub-scope (empty → global-only); DryRun analyzes without
// writing files or updating state (but still takes the lock — SPEC §5.5).
type RunOptions struct {
	DryRun     bool
	ProjectDir string
}

// Run executes one consolidation pass and returns the change Report. The flow is
// the deterministic algorithm of SPEC §5.1:
//
//	open store → resolve memoryRoot → acquire lock (ErrLocked → skipped, no error)
//	→ BuildPlan → Consolidate → if !DryRun: apply dedup + path-clean + consolidation
//	→ Reconcile → recount → SaveState(ok); if DryRun: counts only, no writes/state.
//
// A held lock is not a failure: Run returns a zero-count Report and a nil error
// so the caller exits 0 ("skipped"). Genuine errors (I/O, plan, apply) are
// returned for the caller to map to exit 1 / status "failed".
func (r *Runner) Run(ctx context.Context, opts RunOptions) (Report, error) {
	memoryRoot := r.MemoryRoot
	if memoryRoot == "" {
		memoryRoot = ResolveMemoryRoot()
	}
	if memoryRoot == "" {
		return Report{}, fmt.Errorf("dream: cannot resolve memory root")
	}

	lock, err := AcquireLock(memoryRoot)
	if err != nil {
		if isLocked(err) {
			// Another dream is running: skip silently. Zero-count report, no error
			// → caller exits 0, leaves last_status unchanged (SPEC §5.5 / §6.1). We
			// have opened nothing and created no files, honoring the skip contract.
			return Report{DryRun: opts.DryRun}, nil
		}
		return Report{}, fmt.Errorf("dream: acquire lock: %w", err)
	}
	defer lock.Release()

	plan, err := BuildPlan(memoryRoot, opts.ProjectDir)
	if err != nil {
		return Report{}, fmt.Errorf("dream: build plan: %w", err)
	}

	rep := Report{
		DryRun:      opts.DryRun,
		BytesBefore: plan.BytesBefore,
		FilesBefore: plan.FilesBefore,
	}

	cons := r.Consolidator
	if cons == nil {
		cons = nopConsolidator{}
	}
	cres, err := cons.Consolidate(ctx, ConsolidateInput{
		Plan:       plan,
		MemoryRoot: memoryRoot,
		ProjectDir: opts.ProjectDir,
	})
	if err != nil {
		// The runner surfaces the error; the parent/scheduler (node #8) maps a
		// non-zero exit to state.LastStatus="failed" (SPEC §4.2/§6.1). The runner
		// itself only ever persists "ok", so it never opens/creates the store on a
		// failing or dry-run path.
		return Report{}, fmt.Errorf("dream: consolidate: %w", err)
	}

	// Surface the Consolidator's semantic counters verbatim (SPEC §5.1.1: merge /
	// prune / distill are LLM decisions).
	rep.Merged = cres.Merged
	rep.Pruned = cres.Pruned
	rep.Distilled = cres.Distilled
	rep.Notes = append(rep.Notes, cres.Notes...)

	if opts.DryRun {
		// Predict the deterministic counters without touching disk or state, and
		// without opening the store (which would create index.db). The after-sizes
		// stay zero: nothing was written, so there is no post-state to measure
		// (SPEC §5.5 dry-run row).
		rep.Deduped = plannedDedupeCount(plan)
		rep.PathsCleaned = plannedPathCleanCount(plan)
		return rep, nil
	}

	// --- write path (non dry-run) -------------------------------------------
	//
	// Open the store only here: a dry-run or a lock skip must not create index.db
	// (SPEC §5.5). The store is needed solely for the post-write Reconcile.
	store, err := memory.Open(filepath.Join(memoryRoot, "index.db"), memoryRoot, "")
	if err != nil {
		return Report{}, fmt.Errorf("dream: open memory store: %w", err)
	}
	defer store.Close()

	deleted, deduped, err := applyDedupe(memoryRoot, opts.ProjectDir, plan)
	if err != nil {
		return Report{}, fmt.Errorf("dream: apply dedupe: %w", err)
	}
	rep.Deduped = deduped

	cleaned, err := applyPathClean(memoryRoot, opts.ProjectDir, plan, deleted)
	if err != nil {
		return Report{}, fmt.Errorf("dream: apply path-clean: %w", err)
	}
	rep.PathsCleaned = cleaned

	if err := applyConsolidation(memoryRoot, opts.ProjectDir, cres, deleted); err != nil {
		return Report{}, fmt.Errorf("dream: apply consolidation: %w", err)
	}

	res, err := store.Reconcile()
	if err != nil {
		return Report{}, fmt.Errorf("dream: reconcile: %w", err)
	}
	rep.Reconciled.Indexed = res.Indexed
	rep.Reconciled.Pruned = res.Pruned

	// Recompute post-state sizes by re-enumerating the same scopes.
	after, err := BuildPlan(memoryRoot, opts.ProjectDir)
	if err != nil {
		return Report{}, fmt.Errorf("dream: recount: %w", err)
	}
	rep.BytesAfter = after.BytesBefore
	rep.FilesAfter = after.FilesBefore

	repCopy := rep
	if err := SaveState(memoryRoot, State{
		LastRunAt:  time.Now().UTC(),
		LastStatus: "ok",
		LastReport: &repCopy,
	}); err != nil {
		return Report{}, fmt.Errorf("dream: save state: %w", err)
	}

	return rep, nil
}

// isLocked reports whether err is the ErrLocked contention signal. Kept as a
// helper so the skipped-vs-failed branch reads clearly.
func isLocked(err error) bool {
	return errors.Is(err, ErrLocked)
}

// ResolveMemoryRoot returns the persistent memory root directory the same way
// the CLI does (internal/cli/run.MemoryDir): $PIGO_HOME/memory, else
// ~/.pigo/memory. It is duplicated here rather than imported to keep the dream
// package free of a dependency on the CLI assembly layer (and any import cycle
// through it). Returns "" when neither PIGO_HOME nor the home dir is resolvable.
func ResolveMemoryRoot() string {
	dir := os.Getenv("PIGO_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".pigo")
	}
	return filepath.Join(dir, "memory")
}

// withinScope reports whether target is a write target permitted by this run's
// consolidation scope: it must live under <memoryRoot>/global or, when a
// projectDir is set, under that project's own <memoryRoot>/projects/<id>
// directory. Anything else — the sessions scope, an UNRELATED project's
// directory, or a path outside memoryRoot (e.g. user source) — is rejected. This
// is the SPEC §5.2 / §7.1 path-boundary guard that keeps an LLM-produced path
// from escaping the memory store or clobbering other projects' memories, and it
// mirrors BuildPlan, which only enumerates global + the active project.
//
// The check is symlink-aware: both the scope base and the target's longest
// existing ancestor are passed through filepath.EvalSymlinks before comparison,
// so a symlink planted inside an allowed scope cannot redirect a write/delete
// outside the allowed directory. A prefix match is done on path boundaries (not
// raw string prefixes) so "<root>/globalX" does not pass as "<root>/global".
func withinScope(memoryRoot, projectDir, target string) bool {
	if memoryRoot == "" || target == "" {
		return false
	}
	absRoot, err := filepath.Abs(memoryRoot)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	resolvedTarget := resolveExisting(filepath.Clean(absTarget))

	bases := []string{filepath.Join(absRoot, "global")}
	if projectDir != "" {
		bases = append(bases, filepath.Join(absRoot, "projects", projectID(projectDir)))
	}
	for _, base := range bases {
		base = resolveExisting(base)
		if resolvedTarget == base {
			return true
		}
		if strings.HasPrefix(resolvedTarget, base+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// resolveExisting returns path with symlinks resolved as far as the filesystem
// allows: it walks up to the longest existing ancestor, resolves that with
// filepath.EvalSymlinks, then rejoins the not-yet-existing tail. This lets the
// scope guard defeat symlink redirection (an existing symlink component is
// dereferenced to its real location) while still handling brand-new target
// paths whose leaf files do not exist yet. On any resolution error it falls back
// to the cleaned input so the guard fails closed via the lexical comparison.
func resolveExisting(path string) string {
	path = filepath.Clean(path)
	tail := ""
	cur := path
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if tail == "" {
				return resolved
			}
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the root without finding an existing component.
			return path
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

// plannedDedupeCount is the number of files a dedupe pass would remove: one per
// duplicate beyond the representative in each group (SPEC report.Deduped).
func plannedDedupeCount(plan Plan) int {
	n := 0
	for _, g := range plan.DedupeGroups {
		if len(g.Paths) > 1 {
			n += len(g.Paths) - 1
		}
	}
	return n
}

// plannedPathCleanCount is the number of distinct invalid local path references
// a path-clean pass would strip (SPEC report.PathsCleaned).
func plannedPathCleanCount(plan Plan) int {
	return len(plan.InvalidPathRefs)
}

// applyDedupe removes exact-duplicate memory files, keeping the first path in
// each group (paths are pre-sorted by BuildPlan) and deleting the rest. Every
// deletion target is guarded by withinScope. It returns the set of deleted paths
// (so later passes skip them) and the Deduped count.
func applyDedupe(memoryRoot, projectDir string, plan Plan) (map[string]struct{}, int, error) {
	deleted := make(map[string]struct{})
	count := 0
	for _, g := range plan.DedupeGroups {
		if len(g.Paths) < 2 {
			continue
		}
		// Keep g.Paths[0] as the representative; remove the duplicates.
		for _, p := range g.Paths[1:] {
			if !withinScope(memoryRoot, projectDir, p) {
				return nil, 0, fmt.Errorf("refusing out-of-scope dedupe target %q", p)
			}
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return nil, 0, err
			}
			deleted[filepath.Clean(p)] = struct{}{}
			count++
		}
	}
	return deleted, count, nil
}

// applyPathClean strips invalid local path references from the bodies of the
// files that still exist (skipping any removed by dedupe). Each distinct
// (file, ref) is removed by deleting the exact reference substring and is
// counted once. This is the deterministic half of FR-11; the Consolidator may
// later decide whole-entry pruning for refs that leave an entry meaningless.
// Every rewrite target is guarded by withinScope.
func applyPathClean(memoryRoot, projectDir string, plan Plan, deleted map[string]struct{}) (int, error) {
	// Group refs by file so each file is read/written at most once.
	byFile := make(map[string][]string)
	for _, r := range plan.InvalidPathRefs {
		clean := filepath.Clean(r.File)
		if _, gone := deleted[clean]; gone {
			continue
		}
		byFile[clean] = append(byFile[clean], r.Ref)
	}

	count := 0
	for file, refs := range byFile {
		if !withinScope(memoryRoot, projectDir, file) {
			return 0, fmt.Errorf("refusing out-of-scope path-clean target %q", file)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		body := string(raw)
		for _, ref := range refs {
			if strings.Contains(body, ref) {
				body = strings.ReplaceAll(body, ref, "")
				count++
			}
		}
		if body != string(raw) {
			if err := atomicWrite(file, []byte(body)); err != nil {
				return 0, err
			}
		}
	}
	return count, nil
}

// applyConsolidation writes the Consolidator's decisions: rewritten merged
// bodies, new distilled entries, and deletions. Every write/delete target is
// guarded by withinScope so an LLM cannot escape the memory store (SPEC §5.2 /
// §7.1). Deletions already performed by dedupe are skipped.
func applyConsolidation(memoryRoot, projectDir string, cres ConsolidateResult, deleted map[string]struct{}) error {
	for path, body := range cres.MergedBodies {
		if !withinScope(memoryRoot, projectDir, path) {
			return fmt.Errorf("refusing out-of-scope merged-body target %q", path)
		}
		if err := atomicWrite(path, []byte(body)); err != nil {
			return err
		}
	}
	for _, e := range cres.NewEntries {
		if !withinScope(memoryRoot, projectDir, e.Path) {
			return fmt.Errorf("refusing out-of-scope new-entry target %q", e.Path)
		}
		if err := atomicWrite(e.Path, []byte(e.Body)); err != nil {
			return err
		}
	}
	for _, path := range cres.Deletions {
		if _, gone := deleted[filepath.Clean(path)]; gone {
			continue
		}
		if !withinScope(memoryRoot, projectDir, path) {
			return fmt.Errorf("refusing out-of-scope deletion target %q", path)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// atomicWrite writes data to path via a temp file + rename in the same directory
// so a crash mid-write cannot leave a truncated memory file (SPEC §6.3). The
// parent directory is created lazily.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".dream-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
