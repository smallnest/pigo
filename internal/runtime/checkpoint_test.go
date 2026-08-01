package runtime

// Tests for checkpoint persistence (#480): the write→load round-trip, the
// missing-file sentinel, and the summarize→Checkpoint bridge. They drive the
// real filesystem via t.TempDir(), matching the session/memory test style.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestCheckpointRoundTrip is the core acceptance check: a written checkpoint
// loads back with watermark, summary, createdAt, and covered count preserved.
func TestCheckpointRoundTrip(t *testing.T) {
	root := t.TempDir()
	created := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)
	cp := Checkpoint{
		Watermark:       42,
		Summary:         "The user asked to refactor foo().\nWe extracted a helper and added tests.",
		CreatedAt:       created,
		CoveredMessages: 42,
	}
	if err := WriteCheckpoint("sess-abc", root, cp); err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}

	got, ok, err := LoadCheckpoint("sess-abc", root)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if !ok {
		t.Fatal("LoadCheckpoint: found = false, want true")
	}
	if got.Watermark != cp.Watermark {
		t.Errorf("Watermark = %d, want %d", got.Watermark, cp.Watermark)
	}
	if got.CoveredMessages != cp.CoveredMessages {
		t.Errorf("CoveredMessages = %d, want %d", got.CoveredMessages, cp.CoveredMessages)
	}
	if got.Summary != cp.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, cp.Summary)
	}
	if !got.CreatedAt.Equal(cp.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, cp.CreatedAt)
	}
}

// TestCheckpointFileLayoutAndFrontmatter verifies the file lands at the expected
// path and carries the repo's name/description/metadata.type=checkpoint convention.
func TestCheckpointFileLayoutAndFrontmatter(t *testing.T) {
	root := t.TempDir()
	cp := Checkpoint{Watermark: 3, Summary: "body text", CreatedAt: time.Now().UTC(), CoveredMessages: 3}
	if err := WriteCheckpoint("s1", root, cp); err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}
	want := filepath.Join(root, "sessions", "s1", "checkpoint.md")
	if want != CheckpointPath("s1", root) {
		t.Fatalf("CheckpointPath = %q, want %q", CheckpointPath("s1", root), want)
	}
	content, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read checkpoint file: %v", err)
	}
	fm, _, err := splitFrontmatter(content)
	if err != nil {
		t.Fatalf("splitFrontmatter: %v", err)
	}
	s := string(fm)
	for _, needle := range []string{"name: checkpoint", "type: checkpoint", "watermark: 3"} {
		if !strings.Contains(s, needle) {
			t.Errorf("frontmatter missing %q; got:\n%s", needle, s)
		}
	}
}

// TestLoadCheckpointMissing verifies a missing checkpoint is the (nil,false,nil)
// sentinel, not an error — callers treat "no checkpoint yet" as normal.
func TestLoadCheckpointMissing(t *testing.T) {
	root := t.TempDir()
	cp, ok, err := LoadCheckpoint("nope", root)
	if err != nil {
		t.Fatalf("LoadCheckpoint on missing file: err = %v, want nil", err)
	}
	if ok {
		t.Error("found = true, want false")
	}
	if cp != nil {
		t.Errorf("checkpoint = %+v, want nil", cp)
	}
}

// TestBuildCheckpoint verifies the summarize bridge tags the result with the
// watermark, covered count, and a UTC createdAt.
func TestBuildCheckpoint(t *testing.T) {
	msgs := []agentcore.Message{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}},
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.FixedZone("x", 3600))
	summarize := func(ctx context.Context, m []agentcore.Message) (string, error) {
		if len(m) != len(msgs) {
			t.Errorf("summarize got %d msgs, want %d", len(m), len(msgs))
		}
		return "distilled", nil
	}
	cp, err := BuildCheckpoint(context.Background(), msgs, 2, now, summarize)
	if err != nil {
		t.Fatalf("BuildCheckpoint: %v", err)
	}
	if cp.Summary != "distilled" {
		t.Errorf("Summary = %q, want %q", cp.Summary, "distilled")
	}
	if cp.Watermark != 2 || cp.CoveredMessages != 2 {
		t.Errorf("Watermark/Covered = %d/%d, want 2/2", cp.Watermark, cp.CoveredMessages)
	}
	if cp.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt not UTC: %v", cp.CreatedAt)
	}
}

// TestBuildCheckpointPropagatesError verifies a summarization failure surfaces
// as an error rather than a partial checkpoint (write path never runs).
func TestBuildCheckpointPropagatesError(t *testing.T) {
	boom := errors.New("summarize failed")
	_, err := BuildCheckpoint(context.Background(), nil, 0, time.Now(), func(context.Context, []agentcore.Message) (string, error) {
		return "", boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapping %v", err, boom)
	}
}
