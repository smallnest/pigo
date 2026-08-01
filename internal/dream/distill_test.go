package dream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
)

// stubSessions is an in-memory SessionSource for distill tests: it lists the
// headers it was given and returns canned messages per id, so no real session
// files (or LLM) are touched.
type stubSessions struct {
	headers []session.SessionHeader
	msgs    map[string]agentcore.MessageList
}

func (s *stubSessions) List() ([]session.SessionHeader, error) { return s.headers, nil }

func (s *stubSessions) Load(id string) (session.SessionHeader, agentcore.MessageList, error) {
	for _, h := range s.headers {
		if h.ID == id {
			return h, s.msgs[id], nil
		}
	}
	return session.SessionHeader{}, nil, os.ErrNotExist
}

func userMsg(text string) agentcore.UserMessage {
	return agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(text)}}
}

func asstMsg(text string) agentcore.AssistantMessage {
	return agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent(text)}}
}

// TestCollectRecentSessionsFirstRunWindow: never-run (zero LastRunAt) selects the
// most-recent recentN matching sessions, ordered most-recent-first, filtered to
// the active project.
func TestCollectRecentSessionsFirstRunWindow(t *testing.T) {
	proj := t.TempDir()
	other := t.TempDir()
	base := time.Now().UTC()
	headers := []session.SessionHeader{
		{ID: "s1", Cwd: proj, UpdatedAt: base.Add(-3 * time.Hour)},
		{ID: "s2", Cwd: proj, UpdatedAt: base.Add(-1 * time.Hour)},
		{ID: "s3", Cwd: other, UpdatedAt: base}, // different project, excluded
		{ID: "s4", Cwd: proj, UpdatedAt: base.Add(-2 * time.Hour)},
		{ID: "s5", Cwd: "", UpdatedAt: base}, // unattributed, excluded
	}
	got := collectRecentSessions(headers, State{}, proj, 2)
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2 (recentN cap)", len(got))
	}
	if got[0].ID != "s2" || got[1].ID != "s4" {
		t.Fatalf("wrong window/order: %s, %s (want s2, s4 most-recent-first)", got[0].ID, got[1].ID)
	}
}

// TestCollectRecentSessionsIncremental: a non-zero LastRunAt selects only
// matching sessions updated strictly after it (incremental distillation).
func TestCollectRecentSessionsIncremental(t *testing.T) {
	proj := t.TempDir()
	base := time.Now().UTC()
	last := base.Add(-2 * time.Hour)
	headers := []session.SessionHeader{
		{ID: "old", Cwd: proj, UpdatedAt: base.Add(-3 * time.Hour)}, // before last run
		{ID: "new1", Cwd: proj, UpdatedAt: base.Add(-1 * time.Hour)},
		{ID: "new2", Cwd: proj, UpdatedAt: base},
	}
	got := collectRecentSessions(headers, State{LastRunAt: last}, proj, 20)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (only after LastRunAt)", len(got))
	}
	for _, h := range got {
		if !h.UpdatedAt.After(last) {
			t.Fatalf("session %s not after LastRunAt", h.ID)
		}
	}
}

// TestCollectRecentSessionsGlobalOnlyNoMatch: an empty projectDir (global-only
// run) matches nothing, so the window is empty (no-op path).
func TestCollectRecentSessionsGlobalOnlyNoMatch(t *testing.T) {
	proj := t.TempDir()
	headers := []session.SessionHeader{{ID: "s1", Cwd: proj, UpdatedAt: time.Now().UTC()}}
	if got := collectRecentSessions(headers, State{}, "", 20); len(got) != 0 {
		t.Fatalf("global-only run must match no sessions, got %d", len(got))
	}
}

// TestCollectTranscriptsNoSourceOrNoMatch: nil source or no matching session
// yields "" so the caller records Distilled=0 with a "无新增" note.
func TestCollectTranscriptsNoSourceOrNoMatch(t *testing.T) {
	if got := collectTranscripts(nil, State{}, t.TempDir(), 20, 0); got != "" {
		t.Fatalf("nil source must yield empty transcript, got %q", got)
	}
	src := &stubSessions{headers: []session.SessionHeader{{ID: "s1", Cwd: t.TempDir(), UpdatedAt: time.Now().UTC()}}}
	// Ask for a different (empty) project → no match.
	if got := collectTranscripts(src, State{}, "", 20, 0); got != "" {
		t.Fatalf("no matching session must yield empty transcript, got %q", got)
	}
}

// TestCollectTranscriptsRendersRoleTagged: matching sessions are rendered into a
// role-tagged transcript containing the session id and message text.
func TestCollectTranscriptsRendersRoleTagged(t *testing.T) {
	proj := t.TempDir()
	src := &stubSessions{
		headers: []session.SessionHeader{{ID: "sess-abc", Cwd: proj, UpdatedAt: time.Now().UTC()}},
		msgs: map[string]agentcore.MessageList{
			"sess-abc": {userMsg("I always use tabs not spaces"), asstMsg("noted")},
		},
	}
	got := collectTranscripts(src, State{}, proj, 20, 0)
	for _, want := range []string{"sess-abc", "user: I always use tabs", "assistant: noted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript missing %q\n---\n%s", want, got)
		}
	}
}

// TestParseDistillResponseNewEntries: a canned JSON response yields NewEntry
// writes under the right scope/type dirs, and ephemeral/unknown types are
// dropped.
func TestParseDistillResponseNewEntries(t *testing.T) {
	root := "/mem"
	proj := t.TempDir()
	pid := projectID(proj)
	raw := "```json\n" + `{
	  "entries": [
	    {"type": "user", "scope": "global", "title": "Tabs preference", "body": "Developer prefers tabs over spaces."},
	    {"type": "project", "scope": "project", "title": "Arch", "body": "The runner is deterministic; the consolidator is the LLM half."},
	    {"type": "todo", "scope": "global", "title": "bad", "body": "ephemeral one-shot task"},
	    {"type": "user", "scope": "global", "title": "empty", "body": "   "}
	  ],
	  "notes": ["distilled 2"]
	}` + "\n```"

	entries, notes := parseDistillResponse(raw, nil, root, proj)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (todo + empty dropped): %+v", len(entries), entries)
	}
	byPath := map[string]string{}
	for _, e := range entries {
		byPath[e.Path] = e.Body
	}
	wantGlobal := filepath.Join(root, "global", "user", "tabs-preference.md")
	wantProj := filepath.Join(root, "projects", pid, "project", "arch.md")
	if _, ok := byPath[wantGlobal]; !ok {
		t.Fatalf("missing global user entry at %s; got %v", wantGlobal, byPath)
	}
	if body, ok := byPath[wantProj]; !ok {
		t.Fatalf("missing project entry at %s; got %v", wantProj, byPath)
	} else if !strings.HasSuffix(body, "\n") {
		t.Fatalf("body must end with newline: %q", body)
	}
	if strings.Join(notes, "|") == "" {
		t.Fatal("expected distill notes surfaced")
	}
}

// TestParseDistillResponseProjectScopeNeedsProjectDir: a "project"-scoped entry on
// a global-only run (empty projectDir) has nowhere valid to live and is skipped.
func TestParseDistillResponseProjectScopeNeedsProjectDir(t *testing.T) {
	raw := `{"entries":[{"type":"project","scope":"project","title":"x","body":"y"}]}`
	entries, _ := parseDistillResponse(raw, nil, "/mem", "")
	if len(entries) != 0 {
		t.Fatalf("project entry on global-only run must be skipped, got %+v", entries)
	}
}

// TestParseDistillResponseDedupAgainstExisting: an entry that is a near-duplicate
// of an existing memory is dropped (FR-13).
func TestParseDistillResponseDedupAgainstExisting(t *testing.T) {
	existing := []MemoryFile{{Body: "Developer prefers tabs over spaces in all files"}}
	raw := `{"entries":[{"type":"user","scope":"global","title":"tabs","body":"Developer prefers tabs over spaces in all files"}]}`
	entries, notes := parseDistillResponse(raw, existing, "/mem", "")
	if len(entries) != 0 {
		t.Fatalf("near-duplicate of existing memory must be dropped, got %+v", entries)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, "|"), "near-duplicate") {
		t.Fatalf("expected a near-duplicate skip note, got %v", notes)
	}
}

// TestParseDistillResponseUnparseable: an unparseable response adds nothing and
// is not an error (conservative KEEP).
func TestParseDistillResponseUnparseable(t *testing.T) {
	entries, notes := parseDistillResponse("the model rambled with no json", nil, "/mem", "")
	if entries != nil || notes != nil {
		t.Fatalf("unparseable response must yield nothing, got %+v / %v", entries, notes)
	}
}

// TestRunDistillsThroughRunner: an integration test with a stub session source
// and a stub completer-backed llmConsolidator distills a durable fact into a new
// on-disk memory entry, counts it, and reports it.
func TestRunDistillsThroughRunner(t *testing.T) {
	root := t.TempDir()
	proj := t.TempDir()

	src := &stubSessions{
		headers: []session.SessionHeader{{ID: "s1", Cwd: proj, UpdatedAt: time.Now().UTC()}},
		msgs:    map[string]agentcore.MessageList{"s1": {userMsg("always run tests with gotestsum")}},
	}
	// llmConsolidator whose distill completion returns one durable fact. The
	// merge/prune completion is not reached because there are no eligible files.
	cons := &llmConsolidator{complete: func(_ context.Context, _, _ string) (string, error) {
		return `{"entries":[{"type":"user","scope":"global","title":"Test runner","body":"Always run tests with gotestsum."}],"notes":["one fact"]}`, nil
	}}
	r := &Runner{MemoryRoot: root, Consolidator: cons, Sessions: src}

	rep, err := r.Run(context.Background(), RunOptions{ProjectDir: proj})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Distilled != 1 {
		t.Fatalf("Distilled = %d, want 1", rep.Distilled)
	}
	newPath := filepath.Join(root, "global", "user", "test-runner.md")
	raw, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("distilled entry not written at %s: %v", newPath, err)
	}
	if !strings.Contains(string(raw), "gotestsum") {
		t.Fatalf("distilled body wrong: %q", raw)
	}
}

// TestRunDryRunDistillsButWritesNothing: a dry-run still runs the distill pass
// and reports the count, but writes no new memory files and updates no state.
func TestRunDryRunDistillsButWritesNothing(t *testing.T) {
	root := t.TempDir()
	proj := t.TempDir()

	src := &stubSessions{
		headers: []session.SessionHeader{{ID: "s1", Cwd: proj, UpdatedAt: time.Now().UTC()}},
		msgs:    map[string]agentcore.MessageList{"s1": {userMsg("my stack is Go plus SQLite")}},
	}
	cons := &llmConsolidator{complete: func(_ context.Context, _, _ string) (string, error) {
		return `{"entries":[{"type":"user","scope":"global","title":"Stack","body":"Stack is Go plus SQLite."}],"notes":[]}`, nil
	}}
	r := &Runner{MemoryRoot: root, Consolidator: cons, Sessions: src}

	rep, err := r.Run(context.Background(), RunOptions{ProjectDir: proj, DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Distilled != 1 {
		t.Fatalf("dry-run Distilled = %d, want 1 (still reported)", rep.Distilled)
	}
	if _, err := os.Stat(filepath.Join(root, "global", "user", "stack.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not write the distilled entry (err=%v)", err)
	}
	st, _ := LoadState(root)
	if !st.LastRunAt.IsZero() || st.LastStatus != "" {
		t.Fatalf("dry-run must not update state: %+v", st)
	}
}

// TestRunNoDurableFactsNoOp: when distillation adds nothing, the report records
// Distilled=0 with the "无新增" note (SPEC §5.5).
func TestRunNoDurableFactsNoOp(t *testing.T) {
	root := t.TempDir()
	proj := t.TempDir()
	src := &stubSessions{
		headers: []session.SessionHeader{{ID: "s1", Cwd: proj, UpdatedAt: time.Now().UTC()}},
		msgs:    map[string]agentcore.MessageList{"s1": {userMsg("some transient chatter")}},
	}
	cons := &llmConsolidator{complete: func(_ context.Context, _, _ string) (string, error) {
		return `{"entries":[],"notes":[]}`, nil
	}}
	r := &Runner{MemoryRoot: root, Consolidator: cons, Sessions: src}
	rep, err := r.Run(context.Background(), RunOptions{ProjectDir: proj})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Distilled != 0 {
		t.Fatalf("Distilled = %d, want 0", rep.Distilled)
	}
	if !strings.Contains(strings.Join(rep.Notes, "|"), "无新增") {
		t.Fatalf("expected '无新增' no-op note, got %v", rep.Notes)
	}
}
