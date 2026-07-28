package tui

import (
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
)

// newTestStore opens a session store rooted at a temp dir so persistence/resume
// can be exercised without touching ~/.pigo.
func newTestStore(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// saveSession writes a linear session with the given messages and returns its id.
func saveSession(t *testing.T, store *session.Store, msgs agentcore.MessageList) string {
	t.Helper()
	now := time.Now().UTC()
	header := session.SessionHeader{
		ID:        session.NewID(now),
		CreatedAt: now,
		UpdatedAt: now,
		Model:     "test-model",
		Provider:  "test-provider",
	}
	if err := store.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return header.ID
}

// TestResumeSeedsTranscript constructs a session with a few messages, resumes it
// through newRunSessionWithStore, seeds a transcript with the returned history,
// and asserts the initial transcript blocks carry those messages (FR-16 resume).
func TestResumeSeedsTranscript(t *testing.T) {
	store := newTestStore(t)
	id := saveSession(t, store, agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("你好，世界")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("hello back")}},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("second question")}},
	})

	s, history, err := newRunSessionWithStore(store, Options{ResumeID: id})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
	// The persisted cursor must cover the full resumed history so the first new
	// turn appends only fresh messages, not a re-save of history.
	if s.persisted != 3 {
		t.Errorf("persisted = %d, want 3", s.persisted)
	}
	if s.curLeaf == "" {
		t.Error("curLeaf should be the resumed leaf, got empty")
	}

	tr := newTranscript(DefaultTheme())
	seedTranscript(&tr, history)

	wantTexts := []string{"你好，世界", "hello back", "second question"}
	if len(tr.blocks) != len(wantTexts) {
		t.Fatalf("transcript blocks = %d, want %d", len(tr.blocks), len(wantTexts))
	}
	for i, want := range wantTexts {
		if tr.blocks[i].text != want {
			t.Errorf("block[%d] = %q, want %q", i, tr.blocks[i].text, want)
		}
	}
}

// TestBuildConfigAssembly asserts the run-config assembly maps the live config
// onto RunConfig without a live provider: the model/provider/thinking/window
// fields flow through, compaction is enabled, and the tool registry is wired.
func TestBuildConfigAssembly(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{
		Model:         "opus-test",
		ProviderName:  "anthropic",
		ThinkingLevel: agentcore.ThinkingLevel("high"),
	})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}

	cfg := s.buildConfig()
	if cfg.Model != "opus-test" {
		t.Errorf("cfg.Model = %q, want opus-test", cfg.Model)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("cfg.Provider = %q, want anthropic", cfg.Provider)
	}
	if cfg.ThinkingLevel != agentcore.ThinkingLevel("high") {
		t.Errorf("cfg.ThinkingLevel = %q, want high", cfg.ThinkingLevel)
	}
	if cfg.ContextWindow <= 0 {
		t.Errorf("cfg.ContextWindow = %d, want a positive default", cfg.ContextWindow)
	}
	if !cfg.Compaction.Enabled {
		t.Error("cfg.Compaction.Enabled = false, want true (DefaultCompactionSettings)")
	}
	if cfg.Batch.Registry == nil {
		t.Error("cfg.Batch.Registry is nil, want the assembled tool registry")
	}
	if cfg.Stream == nil {
		t.Error("cfg.Stream is nil, want a stream fn derived from the provider")
	}
}

// TestFreshSessionPersists starts a fresh session, appends a turn to the context,
// persists it, and confirms it round-trips back through the store (FR-16 persist).
func TestFreshSessionPersists(t *testing.T) {
	store := newTestStore(t)
	s, history, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if history != nil {
		t.Fatalf("fresh session history = %v, want nil", history)
	}

	s.agentCtx.Messages = append(s.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("yo")}},
	)
	if err := s.persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if s.persisted != 2 {
		t.Errorf("persisted = %d, want 2", s.persisted)
	}

	_, msgs, err := store.Load(s.header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(msgs))
	}

	// A second persist with no new messages is a no-op.
	before := s.curLeaf
	if err := s.persist(); err != nil {
		t.Fatalf("persist (no-op): %v", err)
	}
	if s.curLeaf != before {
		t.Errorf("curLeaf changed on no-op persist: %q -> %q", before, s.curLeaf)
	}
}
