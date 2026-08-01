package memstatus

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/memory"
)

// TestRunMemoryDisabled renders the disabled state when the store is nil and
// still prints the context and checkpoint sections.
func TestRunMemoryDisabled(t *testing.T) {
	var buf bytes.Buffer
	RunMemory(&buf, nil, "", "", nil, 0)
	out := buf.String()
	for _, want := range []string{"persistent memory:", "disabled", "context:", "checkpoint:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// TestRunMemoryEnabledCounts reconciles a store with entries and asserts the
// report shows enabled status and per-scope counts.
func TestRunMemoryEnabledCounts(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "mem")
	if err := os.MkdirAll(filepath.Join(root, "global", "reference"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "global", "reference", "a.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, err := memory.Open(filepath.Join(base, "index.db"), root, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	var buf bytes.Buffer
	msgs := agentcore.MessageList{agentcore.UserMessage{Content: agentcore.ContentList{agentcore.NewTextContent("hi")}}}
	RunMemory(&buf, st, root, "sess-1", msgs, 200000)
	out := buf.String()
	for _, want := range []string{"enabled", "entries:", "global:", "context:", "checkpoint:", "none yet"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
