// This file implements the /memory slash command (US-011, FR-20, #484) that
// prints a colored report describing the persistent memory store and the
// infinite-context state: entry counts by scope, the current context window and
// auto-compaction trigger point, current context usage, and checkpoint status.
//
// Unlike /status (which reads everything through cli.Host), /memory takes its
// inputs explicitly so both the REPL and the TUI can call it with the live
// memory store, which the Host contract does not expose.
package memstatus

import (
	"fmt"
	"io"
	"sort"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/memory"
	"github.com/smallnest/pigo/internal/runtime"
)

// scopeOrder is the display order for memory scopes. Scopes not listed here are
// appended afterwards in lexical order.
var scopeOrder = []memory.Scope{
	memory.ScopeGlobal,
	memory.ScopeProjects,
	memory.ScopeSessions,
	memory.ScopeCC,
}

// RunMemory prints a colored multi-section report about persistent memory and
// infinite context to out. store may be nil (persistent memory disabled), in
// which case the memory-store section reports that state and the checkpoint /
// context sections still render from msgs and the checkpoint file.
func RunMemory(out io.Writer, store *memory.Store, memoryRoot, sessionID string, msgs agentcore.MessageList, contextWindow int) {
	color := ui.Enabled()

	fmt.Fprintln(out)
	printStoreSection(out, color, store, memoryRoot)
	fmt.Fprintln(out)
	printContextSection(out, color, msgs, contextWindow)
	fmt.Fprintln(out)
	printCheckpointSection(out, color, memoryRoot, sessionID)
}

// printStoreSection prints the persistent memory store section: whether memory
// is enabled, the root directory, and entry counts per scope. It reconciles the
// on-disk files into the index first so the counts are fresh.
func printStoreSection(out io.Writer, color bool, store *memory.Store, memoryRoot string) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "persistent memory:"))
	if store == nil {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "status:"),
			ui.Colorize(color, ui.Yellow, "disabled"))
		return
	}
	fmt.Fprintf(out, "  %s %s\n",
		ui.Colorize(color, ui.Dim, "status:"),
		ui.Colorize(color, ui.Green, "enabled"))
	if memoryRoot != "" {
		fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "root:"), memoryRoot)
	}

	if _, err := store.Reconcile(); err != nil {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "entries:"),
			ui.Colorize(color, ui.Red, fmt.Sprintf("reconcile failed: %v", err)))
		return
	}
	counts, err := store.CountByScope()
	if err != nil {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "entries:"),
			ui.Colorize(color, ui.Red, fmt.Sprintf("count failed: %v", err)))
		return
	}

	total := 0
	for _, n := range counts {
		total += n
	}
	fmt.Fprintf(out, "  %s %d\n", ui.Colorize(color, ui.Dim, "entries:"), total)
	for _, scope := range orderedScopes(counts) {
		fmt.Fprintf(out, "    %s %d\n",
			ui.Colorize(color, ui.Dim, string(scope)+":"), counts[scope])
	}
}

// orderedScopes returns the scopes present in counts, listing the well-known
// scopes first (scopeOrder) and any others afterwards in lexical order.
func orderedScopes(counts map[memory.Scope]int) []memory.Scope {
	seen := make(map[memory.Scope]bool, len(counts))
	var out []memory.Scope
	for _, s := range scopeOrder {
		if _, ok := counts[s]; ok {
			out = append(out, s)
			seen[s] = true
		}
	}
	var extra []memory.Scope
	for s := range counts {
		if !seen[s] {
			extra = append(extra, s)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	return append(out, extra...)
}

// printContextSection prints the current context usage and the auto-compaction
// trigger point (window minus reserve), mirroring /status's context block so
// /memory is self-contained for infinite-context inspection.
func printContextSection(out io.Writer, color bool, msgs agentcore.MessageList, contextWindow int) {
	tokens := compaction.EstimateContextTokens(msgs).Tokens

	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "context:"))
	if contextWindow > 0 {
		fmt.Fprintf(out, "  %s %d / %d tokens\n",
			ui.Colorize(color, ui.Dim, "current:"), tokens, contextWindow)
		util := int(float64(tokens) / float64(contextWindow) * 100)
		utilColor := ui.Green
		if util >= 90 {
			utilColor = ui.Red
		} else if util >= 70 {
			utilColor = ui.Yellow
		}
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "utilization:"),
			ui.Colorize(color, utilColor, fmt.Sprintf("%d%%", util)))

		reserve := compaction.DefaultCompactionSettings.ReserveTokens
		threshold := contextWindow - reserve
		remaining := threshold - tokens
		if remaining < 0 {
			fmt.Fprintf(out, "  %s %s (window: %d, reserve: %d)\n",
				ui.Colorize(color, ui.Dim, "compaction trigger:"),
				ui.Colorize(color, ui.Red, fmt.Sprintf("%d tokens over trigger (%d)", -remaining, threshold)),
				contextWindow, reserve)
		} else {
			fmt.Fprintf(out, "  %s %s (window: %d, reserve: %d)\n",
				ui.Colorize(color, ui.Dim, "compaction trigger:"),
				ui.Colorize(color, ui.Green, fmt.Sprintf("%d tokens until trigger (%d)", remaining, threshold)),
				contextWindow, reserve)
		}
	} else {
		fmt.Fprintf(out, "  %s %d tokens\n", ui.Colorize(color, ui.Dim, "current:"), tokens)
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "compaction trigger:"),
			ui.Colorize(color, ui.Yellow, "auto-compaction disabled (unknown window)"))
	}
}

// printCheckpointSection prints the infinite-context checkpoint status for the
// session: whether a checkpoint.md exists and, if so, its watermark, covered
// message count, and creation time.
func printCheckpointSection(out io.Writer, color bool, memoryRoot, sessionID string) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "checkpoint:"))
	if memoryRoot == "" || sessionID == "" {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "status:"),
			ui.Colorize(color, ui.Yellow, "unavailable (no memory root or session id)"))
		return
	}
	cp, ok, err := runtime.LoadCheckpoint(sessionID, memoryRoot)
	if err != nil {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "status:"),
			ui.Colorize(color, ui.Red, fmt.Sprintf("load failed: %v", err)))
		return
	}
	if !ok {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "status:"),
			ui.Colorize(color, ui.Dim, "none yet"))
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "path:"),
			runtime.CheckpointPath(sessionID, memoryRoot))
		return
	}
	fmt.Fprintf(out, "  %s %s\n",
		ui.Colorize(color, ui.Dim, "status:"),
		ui.Colorize(color, ui.Green, "present"))
	fmt.Fprintf(out, "  %s %d\n", ui.Colorize(color, ui.Dim, "watermark:"), cp.Watermark)
	fmt.Fprintf(out, "  %s %d\n", ui.Colorize(color, ui.Dim, "covered messages:"), cp.CoveredMessages)
	if !cp.CreatedAt.IsZero() {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "created:"),
			cp.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	}
	fmt.Fprintf(out, "  %s %s\n",
		ui.Colorize(color, ui.Dim, "path:"),
		runtime.CheckpointPath(sessionID, memoryRoot))
}

