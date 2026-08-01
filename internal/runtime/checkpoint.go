// Checkpoint persistence for the "infinite context" feature (#480). A checkpoint
// is a distilled summary of a conversation *prefix* — everything up to a
// watermark message index — persisted as a Markdown memory file so a later run
// can reload the collapsed context instead of replaying (and re-tokenizing) the
// whole transcript.
//
// The file lives at <memoryRoot>/sessions/<sessionID>/checkpoint.md and carries
// the repo's standard YAML frontmatter (name/description/metadata.type) with the
// checkpoint bookkeeping (watermark, createdAt, covered message count) under
// metadata; the Summary is the Markdown body. This mirrors the memory-file
// convention (see internal/memory, TypeCheckpoint = "checkpoint") so the memory
// indexer can pick these files up unchanged.
//
// This node provides only the persistence primitives plus the summarize→
// Checkpoint bridge. Wiring into the run loop (#481) is deliberately out of
// scope: a checkpoint write failure is returned to the caller, which is expected
// to log-and-continue rather than abort the turn.
package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"gopkg.in/yaml.v3"
)

// Checkpoint is a distilled summary of the conversation up to (and including)
// Watermark. It is the in-memory form written to / read from checkpoint.md.
type Checkpoint struct {
	// Watermark is the message index the checkpoint summarizes up to: messages
	// [0, Watermark) are collapsed into Summary. A resumed run replays only the
	// tail after Watermark, prepending Summary as the collapsed context.
	Watermark int
	// Summary is the distilled context (the Markdown body of checkpoint.md),
	// produced by the summarization LLM call (see compaction.GenerateSummary).
	Summary string
	// CreatedAt is when the checkpoint was distilled (RFC 3339, UTC).
	CreatedAt time.Time
	// CoveredMessages is how many messages the summary actually folded in. It
	// usually equals Watermark for a linear prefix but is recorded separately so
	// a caller that checkpoints a non-contiguous slice still keeps an honest count.
	CoveredMessages int
}

// SummarizeFunc distills a slice of conversation messages into a single summary
// string. It is the seam that lets BuildCheckpoint reuse compaction.GenerateSummary
// without this package depending on the provider stack: the caller supplies a
// closure over GenerateSummary (binding ctx/stream/model/cfg), and BuildCheckpoint
// invokes it. A summarization error is propagated, never swallowed.
type SummarizeFunc func(ctx context.Context, msgs []agentcore.Message) (string, error)

// checkpointFrontmatter is the YAML head of checkpoint.md. It matches the repo's
// name/description/metadata convention (mirrors SkillFrontmatter and the memory
// file layout) so the file is a well-formed memory document.
type checkpointFrontmatter struct {
	Name        string             `yaml:"name"`
	Description string             `yaml:"description"`
	Metadata    checkpointMetadata `yaml:"metadata"`
}

// checkpointMetadata carries the checkpoint bookkeeping under metadata. Type is
// fixed to "checkpoint" (memory.TypeCheckpoint) so the memory indexer classifies
// it correctly.
type checkpointMetadata struct {
	Type            string    `yaml:"type"`
	Watermark       int       `yaml:"watermark"`
	CreatedAt       time.Time `yaml:"createdAt"`
	CoveredMessages int       `yaml:"coveredMessages"`
}

// checkpointType is the metadata.type value for a checkpoint memory file. It is
// duplicated here (rather than importing internal/memory) to keep this package's
// dependency surface minimal; the two must stay in sync.
const checkpointType = "checkpoint"

// CheckpointPath returns the on-disk path of a session's checkpoint file:
// <memoryRoot>/sessions/<sessionID>/checkpoint.md.
func CheckpointPath(sessionID, memoryRoot string) string {
	return filepath.Join(memoryRoot, "sessions", sessionID, "checkpoint.md")
}

// BuildCheckpoint distills msgs into a Checkpoint by invoking summarize, tagging
// the result with watermark and now (coerced to UTC). It performs no I/O — the
// caller persists the result with WriteCheckpoint — so the (potentially slow,
// potentially failing) summarization call stays off the write path. A nil
// summarize or a summarization error is returned as an error.
func BuildCheckpoint(ctx context.Context, msgs []agentcore.Message, watermark int, now time.Time, summarize SummarizeFunc) (Checkpoint, error) {
	if summarize == nil {
		return Checkpoint{}, fmt.Errorf("runtime: BuildCheckpoint: nil summarize func")
	}
	summary, err := summarize(ctx, msgs)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("runtime: distill checkpoint: %w", err)
	}
	return Checkpoint{
		Watermark:       watermark,
		Summary:         summary,
		CreatedAt:       now.UTC(),
		CoveredMessages: len(msgs),
	}, nil
}

// WriteCheckpoint persists cp as <memoryRoot>/sessions/<sessionID>/checkpoint.md,
// creating parent directories (0o755). It writes to a temp file and atomically
// renames it into place so a concurrent reader never sees a half-written file.
// The write is intended to be non-fatal to callers: on error the on-disk file is
// left untouched and the error is returned for the caller to log-and-continue.
func WriteCheckpoint(sessionID, memoryRoot string, cp Checkpoint) error {
	if sessionID == "" {
		return fmt.Errorf("runtime: WriteCheckpoint: empty sessionID")
	}
	path := CheckpointPath(sessionID, memoryRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("runtime: create checkpoint dir: %w", err)
	}

	doc, err := renderCheckpoint(cp)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, doc, 0o644); err != nil {
		return fmt.Errorf("runtime: write checkpoint temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("runtime: commit checkpoint: %w", err)
	}
	return nil
}

// renderCheckpoint serializes cp into the checkpoint.md byte form: a "---"-fenced
// YAML frontmatter block followed by the Summary as the Markdown body.
func renderCheckpoint(cp Checkpoint) ([]byte, error) {
	fm := checkpointFrontmatter{
		Name:        "checkpoint",
		Description: fmt.Sprintf("Conversation checkpoint at watermark %d (%d messages).", cp.Watermark, cp.CoveredMessages),
		Metadata: checkpointMetadata{
			Type:            checkpointType,
			Watermark:       cp.Watermark,
			CreatedAt:       cp.CreatedAt.UTC(),
			CoveredMessages: cp.CoveredMessages,
		},
	}
	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("runtime: encode checkpoint frontmatter: %w", err)
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(fmBytes)
	b.WriteString("---\n\n")
	b.WriteString(cp.Summary)
	return b.Bytes(), nil
}

// LoadCheckpoint reads and parses the checkpoint for sessionID under memoryRoot.
// It returns (nil, false, nil) when the file does not exist — a missing
// checkpoint is a normal "no collapsed context yet" state, not an error. A
// present-but-malformed file yields a non-nil error.
func LoadCheckpoint(sessionID, memoryRoot string) (*Checkpoint, bool, error) {
	path := CheckpointPath(sessionID, memoryRoot)
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("runtime: read checkpoint: %w", err)
	}

	fmBytes, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, false, fmt.Errorf("runtime: parse checkpoint %s: %w", path, err)
	}
	var fm checkpointFrontmatter
	if err := yaml.Unmarshal(fmBytes, &fm); err != nil {
		return nil, false, fmt.Errorf("runtime: decode checkpoint frontmatter %s: %w", path, err)
	}

	cp := &Checkpoint{
		Watermark:       fm.Metadata.Watermark,
		Summary:         string(bytes.TrimLeft(body, "\r\n")),
		CreatedAt:       fm.Metadata.CreatedAt.UTC(),
		CoveredMessages: fm.Metadata.CoveredMessages,
	}
	return cp, true, nil
}
