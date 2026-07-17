// This file (US-003) ties the compaction pieces together: given a message list
// and settings, it finds the cut point, extracts file operations from the
// summarized range, generates the structured summary, and returns a
// CompactionResult ready to be persisted as a session compaction entry and used
// to rebuild the agent context. Mirrors pi's prepareCompaction + compact.
package compaction

import (
	"context"
	"encoding/json"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

// CompactionDetails records the files touched in the compacted history, stored
// alongside a compaction entry so a later iterative compaction can seed its
// file lists. Mirrors pi's CompactionDetails.
type CompactionDetails struct {
	// ReadFiles are files read but not modified in the compacted range.
	ReadFiles []string `json:"readFiles"`
	// ModifiedFiles are files written or edited in the compacted range.
	ModifiedFiles []string `json:"modifiedFiles"`
}

// CompactionResult is the outcome of a compaction: the summary text (with file
// metadata appended), the index where retained history begins, the estimated
// tokens before compaction, and the extracted file details. Mirrors pi's
// CompactionResult, adapted to pigo's flat message list (index vs entry id).
type CompactionResult struct {
	// Summary is the structured summary that replaces the compacted history.
	Summary string
	// FirstKeptIndex is the index of the first retained message.
	FirstKeptIndex int
	// TokensBefore is the estimated context tokens before compaction.
	TokensBefore int
	// Details holds the file operations extracted from the compacted range.
	Details CompactionDetails
}

// Compact prepares and generates a compaction over msgs. It cuts at
// FindCutPoint(msgs, settings.KeepRecentTokens), summarizes messages in
// [prevCompactionIndex+1, firstKeptIndex) (seeding file ops from a prior
// compaction's details when provided), and appends <read-files>/<modified-files>
// metadata to the summary. previousSummary, when non-empty, switches to the
// iterative update template.
//
// prevCompactionIndex is the index of the last already-applied compaction point
// (-1 when none); summarization starts just after it so each compaction only
// covers the newly accumulated history. prevDetails carries that compaction's
// file lists so long-lived reads/edits survive across successive compactions.
//
// It returns (nil, nil) when there is nothing to compact (no valid cut point or
// an empty summarization range).
func Compact(
	ctx context.Context,
	stream provider.StreamFn,
	model provider.Model,
	msgs []agentcore.Message,
	settings CompactionSettings,
	prevCompactionIndex int,
	prevDetails *CompactionDetails,
	previousSummary string,
	cfg provider.StreamConfig,
) (*CompactionResult, error) {
	cut := FindCutPoint(msgs, settings.KeepRecentTokens)

	start := prevCompactionIndex + 1
	if start < 0 {
		start = 0
	}
	if start >= cut.FirstKeptIndex {
		// Nothing new to summarize.
		return nil, nil
	}
	toSummarize := msgs[start:cut.FirstKeptIndex]

	// Seed file ops from the previous compaction, then fold in this range.
	ops := NewFileOps()
	if prevDetails != nil {
		for _, f := range prevDetails.ReadFiles {
			ops.Read[f] = struct{}{}
		}
		for _, f := range prevDetails.ModifiedFiles {
			ops.Edited[f] = struct{}{}
		}
	}
	for _, m := range toSummarize {
		extractFileOpsFromMessage(m, ops)
	}
	readFiles, modifiedFiles := computeFileLists(ops)

	summary, err := GenerateSummary(ctx, stream, model, toSummarize, settings.ReserveTokens, previousSummary, cfg)
	if err != nil {
		return nil, err
	}
	summary += formatFileOperations(readFiles, modifiedFiles)

	return &CompactionResult{
		Summary:        summary,
		FirstKeptIndex: cut.FirstKeptIndex,
		TokensBefore:   EstimateContextTokens(msgs).Tokens,
		Details:        CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles},
	}, nil
}

// Message builds the CompactionMessage to persist for this result: the summary
// text plus the estimated tokens-before and the file details (as raw JSON).
func (r *CompactionResult) Message(now int64) agentcore.CompactionMessage {
	var details json.RawMessage
	if b, err := json.Marshal(r.Details); err == nil {
		details = b
	}
	return agentcore.CompactionMessage{
		RoleField:    agentcore.RoleCompaction,
		Summary:      r.Summary,
		TokensBefore: r.TokensBefore,
		Details:      details,
		Timestamp:    now,
	}
}

// RebuildContext returns the post-compaction message list: the compaction
// checkpoint followed by the retained recent messages (msgs[FirstKeptIndex:]).
// The summarized prefix is dropped and replaced by the single checkpoint,
// keeping context continuous while staying within the window. Mirrors pi's
// post-compact context reconstruction (summary entry + retained tail).
func (r *CompactionResult) RebuildContext(msgs []agentcore.Message, now int64) agentcore.MessageList {
	out := make(agentcore.MessageList, 0, len(msgs)-r.FirstKeptIndex+1)
	out = append(out, r.Message(now))
	out = append(out, msgs[r.FirstKeptIndex:]...)
	return out
}
