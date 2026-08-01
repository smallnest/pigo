package repl

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/dream"
)

// seedDueDreamState writes a state.json under memoryRoot old enough that a
// 7-day-interval dream is due, so maybeStartBackgroundDream will spawn.
func seedDueDreamState(t *testing.T, memoryRoot string) {
	t.Helper()
	if err := dream.SaveState(memoryRoot, dream.State{
		LastRunAt:  time.Now().Add(-30 * 24 * time.Hour),
		LastStatus: "ok",
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
}

func TestMaybeStartBackgroundDream_NoticeOnChanges(t *testing.T) {
	root := t.TempDir()
	seedDueDreamState(t, root)

	orig := spawnDream
	t.Cleanup(func() { spawnDream = orig })
	done := make(chan struct{})
	spawnDream = func(_ context.Context, dir string, dryRun bool) (dreamSubprocessResult, error) {
		defer close(done)
		if dryRun {
			t.Errorf("background trigger must not run in dry-run mode")
		}
		if dir != "/proj/y" {
			t.Errorf("spawn got dir %q, want /proj/y", dir)
		}
		return dreamSubprocessResult{report: dream.Report{Merged: 3}}, nil
	}

	var buf bytes.Buffer
	if !maybeStartBackgroundDream(&buf, root, "/proj/y", dream.NewConfig(nil, 7, 20)) {
		t.Fatal("due+enabled dream should launch a background run")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("spawn not invoked within timeout")
	}
	// The notice writes asynchronously after spawn returns; poll briefly.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && buf.Len() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := buf.String(); !strings.Contains(got, "dream:") || !strings.Contains(got, "merged 3") {
		t.Fatalf("one-line notice missing/incorrect: %q", got)
	}
}

func TestMaybeStartBackgroundDream_DisabledNoSpawn(t *testing.T) {
	root := t.TempDir()
	seedDueDreamState(t, root)

	orig := spawnDream
	t.Cleanup(func() { spawnDream = orig })
	spawnDream = func(context.Context, string, bool) (dreamSubprocessResult, error) {
		t.Fatal("disabled dream must not spawn a subprocess")
		return dreamSubprocessResult{}, nil
	}

	enabledFalse := false
	var buf bytes.Buffer
	if maybeStartBackgroundDream(&buf, root, "/proj/y", dream.NewConfig(&enabledFalse, 7, 20)) {
		t.Fatal("disabled dream must not launch")
	}
}

func TestMaybeStartBackgroundDream_EmptyRootNoSpawn(t *testing.T) {
	orig := spawnDream
	t.Cleanup(func() { spawnDream = orig })
	spawnDream = func(context.Context, string, bool) (dreamSubprocessResult, error) {
		t.Fatal("empty memory root must not spawn")
		return dreamSubprocessResult{}, nil
	}
	var buf bytes.Buffer
	if maybeStartBackgroundDream(&buf, "", "/proj/y", dream.NewConfig(nil, 7, 20)) {
		t.Fatal("empty memory root must not launch")
	}
}

func TestMaybeStartBackgroundDream_NeverRunNoSpawn(t *testing.T) {
	// A fresh memory root (no state.json) is "never run" → not due → no spawn.
	root := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := spawnDream
	t.Cleanup(func() { spawnDream = orig })
	spawnDream = func(context.Context, string, bool) (dreamSubprocessResult, error) {
		t.Fatal("never-run state must not spawn (first run is manual)")
		return dreamSubprocessResult{}, nil
	}
	var buf bytes.Buffer
	if maybeStartBackgroundDream(&buf, root, "/proj/y", dream.NewConfig(nil, 7, 20)) {
		t.Fatal("never-run dream must not launch")
	}
}
