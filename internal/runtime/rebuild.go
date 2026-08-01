// Context rebuild for the "infinite context" feature (#482). Where auto-
// compaction (loop.go) collapses history *lossily* on the fly, a rebuild
// reconstructs the working context deterministically from a persisted
// checkpoint: everything before the checkpoint watermark is replaced by the
// distilled checkpoint summary, and everything at/after the watermark is kept
// verbatim. This is what the /rebuild command (REPL + TUI) invokes, and what
// the run loop (#481) will call on resume to reload a collapsed prefix instead
// of replaying the whole transcript.
//
// When no checkpoint exists yet there is nothing to reload, so a rebuild falls
// back to the ordinary lossy compaction path (the same compaction.Compact flow
// runCompaction drives) so /rebuild still shrinks an overgrown context.
//
// This file is deliberately side-effect free with respect to the loop: it never
// mutates the caller's AgentContext. It returns the rebuilt MessageList (plus a
// RebuildResult describing what happened and an equivalent CompactionEvent) so
// the CLI handlers — and, later, #481 — decide when and how to apply it.
package runtime

import (
	"context"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
)

// RebuildResult describes the outcome of a context rebuild. Messages is the
// rebuilt list ready to replace the live context; the remaining fields mirror
// CompactionEvent so a front-end can report the same before/after summary as a
// compaction.
type RebuildResult struct {
	// Messages is the rebuilt context: a single summary/checkpoint message
	// followed by the retained recent tail. When NoOp is true it is the original
	// list, unchanged.
	Messages agentcore.MessageList
	// FromCheckpoint is true when the boundary came from a persisted checkpoint;
	// false when the no-checkpoint fallback ran a lossy compaction.
	FromCheckpoint bool
	// Watermark is the boundary index used: the checkpoint watermark, or the
	// compaction cut point in the fallback path.
	Watermark int
	// SummarizedCount is how many leading messages were collapsed into the summary.
	SummarizedCount int
	// KeptCount is how many recent messages were preserved verbatim.
	KeptCount int
	// TokensBefore / TokensAfter are the estimated context tokens before and after
	// the rebuild (equal when NoOp).
	TokensBefore int
	TokensAfter  int
	// NoOp is true when nothing changed: no checkpoint existed and there was
	// nothing to compact (an empty summarization range).
	NoOp bool
}

// Event renders the rebuild as a CompactionEvent so consumers that already
// handle compaction reporting (the REPL/TUI event surfaces) can present a
// rebuild with the same shape. Reason is "rebuild".
func (r *RebuildResult) Event() agentcore.CompactionEvent {
	return agentcore.CompactionEvent{
		Reason:          "rebuild",
		TokensBefore:    r.TokensBefore,
		TokensAfter:     r.TokensAfter,
		SummarizedCount: r.SummarizedCount,
		KeptCount:       r.KeptCount,
	}
}

// RebuildFromCheckpoint reconstructs the working context for sessionID.
//
// If a checkpoint exists under memoryRoot, the compression boundary is inserted
// at checkpoint.Watermark: messages before the watermark collapse to the
// checkpoint summary (rendered as a single compaction message), and messages
// at/after the watermark are preserved verbatim. The watermark is clamped to
// [0, len(msgs)] so a stale checkpoint recorded against a longer history (or one
// that has since been re-compacted) never slices out of range.
//
// If no checkpoint exists, it falls back to the lossy compaction path — the same
// compaction.Compact flow the loop's auto-compaction uses (runCompaction) — so
// /rebuild still shrinks the context. When there is nothing to compact the
// original list is returned with NoOp set.
//
// It performs no mutation of the caller's context and no checkpoint writes; the
// returned RebuildResult carries the rebuilt list for the caller to apply. When
// waitForCheckpoint is non-nil it is invoked before reading, so a caller that
// has an in-flight checkpoint write can block until it lands (kept as a callback
// so this stays decoupled from the loop's write path).
func RebuildFromCheckpoint(
	ctx context.Context,
	msgs agentcore.MessageList,
	sessionID, memoryRoot string,
	cfg *RunConfig,
	waitForCheckpoint func(),
) (*RebuildResult, error) {
	if waitForCheckpoint != nil {
		waitForCheckpoint()
	}
	now := nowMillis()
	tokensBefore := compaction.EstimateContextTokens(msgs).Tokens

	cp, ok, err := LoadCheckpoint(sessionID, memoryRoot)
	if err != nil {
		return nil, err
	}
	if ok {
		return rebuildFromLoadedCheckpoint(msgs, cp, tokensBefore, now), nil
	}

	// No checkpoint: fall back to the ordinary lossy compaction path.
	res, err := runCompaction(ctx, msgs, cfg)
	if err != nil {
		return nil, err
	}
	if res == nil {
		// Nothing to summarize (no valid cut point / empty range): leave as-is.
		return &RebuildResult{
			Messages:     msgs,
			TokensBefore: tokensBefore,
			TokensAfter:  tokensBefore,
			KeptCount:    len(msgs),
			NoOp:         true,
		}, nil
	}
	rebuilt := res.RebuildContext(msgs, now)
	kept := len(rebuilt) - 1
	return &RebuildResult{
		Messages:        rebuilt,
		FromCheckpoint:  false,
		Watermark:       res.FirstKeptIndex,
		SummarizedCount: len(msgs) - kept,
		KeptCount:       kept,
		TokensBefore:    tokensBefore,
		TokensAfter:     compaction.EstimateContextTokens(rebuilt).Tokens,
	}, nil
}

// rebuildFromLoadedCheckpoint builds the rebuilt context from a loaded
// checkpoint: the pre-watermark prefix collapses to a single compaction message
// carrying cp.Summary, and the tail from the (clamped) watermark on is preserved
// verbatim. It reuses compaction.CompactionResult.RebuildContext so the summary
// message is shaped exactly like a compaction checkpoint.
func rebuildFromLoadedCheckpoint(msgs agentcore.MessageList, cp *Checkpoint, tokensBefore int, now int64) *RebuildResult {
	w := cp.Watermark
	if w < 0 {
		w = 0
	}
	if w > len(msgs) {
		w = len(msgs)
	}
	res := &compaction.CompactionResult{
		Summary:        cp.Summary,
		FirstKeptIndex: w,
		TokensBefore:   tokensBefore,
	}
	rebuilt := res.RebuildContext(msgs, now)
	kept := len(rebuilt) - 1
	return &RebuildResult{
		Messages:        rebuilt,
		FromCheckpoint:  true,
		Watermark:       w,
		SummarizedCount: w,
		KeptCount:       kept,
		TokensBefore:    tokensBefore,
		TokensAfter:     compaction.EstimateContextTokens(rebuilt).Tokens,
	}
}
