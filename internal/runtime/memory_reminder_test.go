package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/memory"
)

// openMemoryStore opens a memory.Store over a temp DB + root and writes the
// given memory files (path segments relative to root -> body), returning the
// store. Reconcile is left to the provider's ReconcileFirst.
func openMemoryStore(t *testing.T, files map[string]string) *memory.Store {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "mimo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %q: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %q: %v", full, err)
		}
	}
	st, err := memory.Open(filepath.Join(base, "memory.db"), root, "")
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// userMsgs builds a MessageList with a single user text message.
func userMsgs(text string) agentcore.MessageList {
	return agentcore.MessageList{
		agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
		},
	}
}

func TestMemoryReminderInjectsMatchingSnippet(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("projects", "proj1", "notes", "auth.md"): "permission deadlock encountered during checkpoint save then retry succeeded",
		filepath.Join("global", "user", "u1.md"):               "unrelated grocery shopping list",
	})
	p := &MemoryReminderProvider{Store: st, MaxChars: 400}

	body, ok := p.Reminder(context.Background(), userMsgs("how do I handle the permission deadlock?"))
	if !ok {
		t.Fatalf("expected a memory reminder to fire, got ok=false")
	}
	if !strings.Contains(body, "Relevant memory:") {
		t.Errorf("body missing heading: %q", body)
	}
	if !strings.Contains(body, "permission") {
		t.Errorf("body missing the matching snippet text: %q", body)
	}
	if len(body) > 400 {
		t.Errorf("body exceeds MaxChars budget: len=%d body=%q", len(body), body)
	}

	// Second identical call dedupes.
	if _, ok := p.Reminder(context.Background(), userMsgs("how do I handle the permission deadlock?")); ok {
		t.Errorf("identical follow-up call should dedupe to ok=false")
	}
}

func TestMemoryReminderRespectsMaxChars(t *testing.T) {
	long := strings.Repeat("permission deadlock retry ", 40)
	st := openMemoryStore(t, map[string]string{
		filepath.Join("projects", "proj1", "notes", "big.md"): long,
	})
	p := &MemoryReminderProvider{Store: st, MaxChars: 120}

	body, ok := p.Reminder(context.Background(), userMsgs("permission deadlock"))
	if !ok {
		t.Fatalf("expected a memory reminder to fire")
	}
	if len(body) > 120 {
		t.Errorf("body exceeds MaxChars=120: len=%d", len(body))
	}
}

func TestMemoryReminderNoUserMessage(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("global", "user", "u1.md"): "permission deadlock note",
	})
	p := &MemoryReminderProvider{Store: st}

	// Empty message list.
	if _, ok := p.Reminder(context.Background(), nil); ok {
		t.Errorf("empty message list must not fire")
	}
	// User message with no text content.
	blank := agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}
	if _, ok := p.Reminder(context.Background(), blank); ok {
		t.Errorf("empty user text must not fire")
	}
}

func TestMemoryReminderNoMatch(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("global", "user", "u1.md"): "grocery shopping list milk eggs",
	})
	p := &MemoryReminderProvider{Store: st}
	if _, ok := p.Reminder(context.Background(), userMsgs("kubernetes ingress controller crash")); ok {
		t.Errorf("no matching memory should not fire")
	}
}

func TestMemoryReminderNilStore(t *testing.T) {
	p := &MemoryReminderProvider{}
	if _, ok := p.Reminder(context.Background(), userMsgs("anything")); ok {
		t.Errorf("nil store must not fire")
	}
	var np *MemoryReminderProvider
	if _, ok := np.Reminder(context.Background(), userMsgs("anything")); ok {
		t.Errorf("nil provider must not fire")
	}
}

func TestMemoryReminderMemoryMdFirst(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("projects", "proj1", "notes", "detail.md"): "permission deadlock detail note here",
		filepath.Join("projects", "proj1", "MEMORY.md"):          "permission deadlock index overview",
	})
	p := &MemoryReminderProvider{Store: st, MaxChars: 800}
	body, ok := p.Reminder(context.Background(), userMsgs("permission deadlock"))
	if !ok {
		t.Fatalf("expected reminder to fire")
	}
	idxMem := strings.Index(body, "MEMORY.md")
	idxDetail := strings.Index(body, "detail.md")
	if idxMem == -1 || idxDetail == -1 {
		t.Fatalf("expected both files in body: %q", body)
	}
	if idxMem > idxDetail {
		t.Errorf("MEMORY.md should sort before other results, got body: %q", body)
	}
}
