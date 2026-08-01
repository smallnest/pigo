package repl

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/dream"
)

// syncWriter is a concurrency-safe writer whose first Write closes done, so a
// test can wait for the async one-line notice and then read it under the same
// lock the background goroutine wrote it under (bytes.Buffer is not safe for
// concurrent use).
type syncWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
	once sync.Once
}

func newSyncWriter() *syncWriter { return &syncWriter{done: make(chan struct{})} }

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	w.once.Do(func() { close(w.done) })
	return n, err
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

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
	spawnDream = func(_ context.Context, dir string, dryRun bool) (dreamSubprocessResult, error) {
		if dryRun {
			t.Errorf("background trigger must not run in dry-run mode")
		}
		if dir != "/proj/y" {
			t.Errorf("spawn got dir %q, want /proj/y", dir)
		}
		return dreamSubprocessResult{report: dream.Report{Merged: 3}}, nil
	}

	w := newSyncWriter()
	if !maybeStartBackgroundDream(w, root, "/proj/y", dream.NewConfig(nil, 7, 20)) {
		t.Fatal("due+enabled dream should launch a background run")
	}
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("one-line notice not written within timeout")
	}
	if got := w.String(); !strings.Contains(got, "dream:") || !strings.Contains(got, "merged 3") {
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
