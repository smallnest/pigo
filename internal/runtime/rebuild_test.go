package runtime

// Tests for context rebuild (#482): with a checkpoint present, RebuildFromCheckpoint
// inserts the compression boundary at the watermark — collapsing the pre-watermark
// prefix into the checkpoint summary and preserving the recent tail verbatim; with
// no checkpoint it falls back to the lossy compaction path (compaction.Compact).
// Filesystem access uses t.TempDir(), matching checkpoint_test.go; the summary
// model is the shared summaryStream fake from compaction_test.go.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
)

// textUser builds a user message carrying body, used to seed a rebuildable history.
func textUser(body string) agentcore.UserMessage {
	return agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(body)},
	}
}

func TestRebuildFromCheckpoint_InsertsBoundary(t *testing.T) {
	root := t.TempDir()
	const sessionID = "sess-rebuild"

	// A 6-message history; the checkpoint collapses the first 4 into a summary and
	// keeps messages [4:] verbatim.
	msgs := agentcore.MessageList{
		textUser("m0"), textUser("m1"), textUser("m2"),
		textUser("m3"), textUser("keep-A"), textUser("keep-B"),
	}
	cp := Checkpoint{
		Watermark:       4,
		Summary:         "## Goal\ndistilled prefix",
		CreatedAt:       time.Now().UTC(),
		CoveredMessages: 4,
	}
	if err := WriteCheckpoint(sessionID, root, cp); err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}

	// No summarization stream is needed: the checkpoint path is pure and local.
	cfg := newRunCfg(nil)
	res, err := RebuildFromCheckpoint(context.Background(), msgs, sessionID, root, &cfg, nil)
	if err != nil {
		t.Fatalf("RebuildFromCheckpoint: %v", err)
	}
	if !res.FromCheckpoint {
		t.Fatalf("expected FromCheckpoint=true")
	}
	if res.NoOp {
		t.Fatalf("expected a real rebuild, got NoOp")
	}
	if res.Watermark != 4 || res.SummarizedCount != 4 {
		t.Fatalf("watermark/summarized: got %d/%d, want 4/4", res.Watermark, res.SummarizedCount)
	}
	// Rebuilt list = 1 compaction message + the retained tail (2 messages).
	if len(res.Messages) != 3 {
		t.Fatalf("rebuilt length: got %d, want 3: %+v", len(res.Messages), res.Messages)
	}
	if res.KeptCount != 2 {
		t.Fatalf("kept: got %d, want 2", res.KeptCount)
	}
	// The prefix must collapse into a single compaction message carrying the summary.
	head, ok := res.Messages[0].(agentcore.CompactionMessage)
	if !ok {
		t.Fatalf("message[0] should be a compaction checkpoint, got %T", res.Messages[0])
	}
	if !strings.Contains(head.Summary, "distilled prefix") {
		t.Fatalf("summary not carried through: %q", head.Summary)
	}
	// The recent tail is preserved verbatim, in order.
	for i, want := range []string{"keep-A", "keep-B"} {
		um, ok := res.Messages[i+1].(agentcore.UserMessage)
		if !ok {
			t.Fatalf("message[%d] should be preserved user message, got %T", i+1, res.Messages[i+1])
		}
		if got := agentcore.ContentToText(um.Content); got != want {
			t.Fatalf("tail[%d]: got %q, want %q", i, got, want)
		}
	}
}

func TestRebuildFromCheckpoint_ClampsStaleWatermark(t *testing.T) {
	root := t.TempDir()
	const sessionID = "sess-stale"

	msgs := agentcore.MessageList{textUser("a"), textUser("b")}
	// Watermark past the end (history was re-compacted since the checkpoint).
	cp := Checkpoint{Watermark: 99, Summary: "old summary", CreatedAt: time.Now().UTC()}
	if err := WriteCheckpoint(sessionID, root, cp); err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}

	cfg := newRunCfg(nil)
	res, err := RebuildFromCheckpoint(context.Background(), msgs, sessionID, root, &cfg, nil)
	if err != nil {
		t.Fatalf("RebuildFromCheckpoint: %v", err)
	}
	// Clamped to len(msgs)=2: everything collapses, no verbatim tail, only the head.
	if res.Watermark != 2 || res.KeptCount != 0 {
		t.Fatalf("clamp: watermark=%d kept=%d, want 2/0", res.Watermark, res.KeptCount)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("rebuilt length: got %d, want 1 (summary only)", len(res.Messages))
	}
}

func TestRebuildFromCheckpoint_WaitCallbackInvoked(t *testing.T) {
	root := t.TempDir()
	const sessionID = "sess-wait"
	cp := Checkpoint{Watermark: 0, Summary: "s", CreatedAt: time.Now().UTC()}
	if err := WriteCheckpoint(sessionID, root, cp); err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}

	waited := false
	cfg := newRunCfg(nil)
	_, err := RebuildFromCheckpoint(context.Background(), agentcore.MessageList{textUser("x")}, sessionID, root, &cfg, func() { waited = true })
	if err != nil {
		t.Fatalf("RebuildFromCheckpoint: %v", err)
	}
	if !waited {
		t.Fatalf("waitForCheckpoint callback was not invoked before reading")
	}
}

func TestRebuildFromCheckpoint_FallsBackToCompaction(t *testing.T) {
	root := t.TempDir() // empty: no checkpoint on disk
	const sessionID = "sess-nocp"

	// Seed a long history so FindCutPoint leaves a summarizable prefix.
	msgs := bigUserMessages(12, 800)

	cfg := newRunCfg(scriptedStream(nil))
	cfg.SummaryStream = summaryStream("## Goal\nfallback compaction summary")
	cfg.ContextWindow = 2000
	cfg.Compaction = compaction.CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100}

	res, err := RebuildFromCheckpoint(context.Background(), msgs, sessionID, root, &cfg, nil)
	if err != nil {
		t.Fatalf("RebuildFromCheckpoint: %v", err)
	}
	if res.FromCheckpoint {
		t.Fatalf("expected fallback (FromCheckpoint=false) when no checkpoint exists")
	}
	if res.NoOp {
		t.Fatalf("expected a real compaction fallback, got NoOp")
	}
	if res.SummarizedCount <= 0 {
		t.Fatalf("expected some messages summarized, got %d", res.SummarizedCount)
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Fatalf("fallback should reduce tokens: before=%d after=%d", res.TokensBefore, res.TokensAfter)
	}
	// The rebuilt context begins with a compaction checkpoint holding the summary.
	head, ok := res.Messages[0].(agentcore.CompactionMessage)
	if !ok {
		t.Fatalf("message[0] should be a compaction checkpoint, got %T", res.Messages[0])
	}
	if !strings.Contains(head.Summary, "fallback compaction summary") {
		t.Fatalf("fallback summary not carried through: %q", head.Summary)
	}
}

func TestRebuildFromCheckpoint_FallbackNoOpWhenNothingToCompact(t *testing.T) {
	root := t.TempDir() // no checkpoint
	const sessionID = "sess-empty"

	// A single short message: no valid cut point leaves a summarization range, so
	// Compact returns (nil, nil) and the rebuild is a no-op.
	msgs := agentcore.MessageList{textUser("only")}
	cfg := newRunCfg(scriptedStream(nil))
	cfg.SummaryStream = summaryStream("unused")
	cfg.ContextWindow = 2000
	cfg.Compaction = compaction.CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100}

	res, err := RebuildFromCheckpoint(context.Background(), msgs, sessionID, root, &cfg, nil)
	if err != nil {
		t.Fatalf("RebuildFromCheckpoint: %v", err)
	}
	if !res.NoOp {
		t.Fatalf("expected NoOp when there is nothing to compact")
	}
	if len(res.Messages) != 1 || res.TokensAfter != res.TokensBefore {
		t.Fatalf("no-op should return the original context unchanged: %+v", res)
	}
}
