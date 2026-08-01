package dream

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fixedNow returns a func yielding t, for the Now seam.
func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

// writeDueState seeds a state.json under memoryRoot with a LastRunAt old enough
// that Due(cfg, now) is true for the default interval.
func writeDueState(t *testing.T, memoryRoot string, lastRun time.Time) {
	t.Helper()
	if err := SaveState(memoryRoot, State{LastRunAt: lastRun, LastStatus: "ok"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
}

func TestSchedulerDue(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	cfg := NewConfig(nil, 7, 20) // enabled, 7-day interval

	var s Scheduler

	// Disabled → never due, and cheap (no state read needed).
	disabled := NewConfig(boolPtr(false), 7, 20)
	if s.Due(root, disabled, now) {
		t.Fatal("disabled config must not be due")
	}

	// No state file (never run) → not due (first run is manual, spec §11.1).
	if s.Due(root, cfg, now) {
		t.Fatal("never-run state must not be due")
	}

	// Last run within the interval → not due.
	writeDueState(t, root, now.Add(-3*24*time.Hour))
	if s.Due(root, cfg, now) {
		t.Fatal("run 3d ago with 7d interval must not be due")
	}

	// Last run older than the interval → due.
	writeDueState(t, root, now.Add(-8*24*time.Hour))
	if !s.Due(root, cfg, now) {
		t.Fatal("run 8d ago with 7d interval must be due")
	}
}

func TestMaybeRunBackground_NotSpawnedWhenDisabled(t *testing.T) {
	root := t.TempDir()
	writeDueState(t, root, time.Now().Add(-30*24*time.Hour)) // would be due if enabled

	var spawned bool
	launched := Scheduler{}.MaybeRunBackground(context.Background(), BackgroundDeps{
		MemoryRoot: root,
		Config:     NewConfig(boolPtr(false), 7, 20),
		Now:        fixedNow(time.Now()),
		Spawn: func(context.Context, string) (Report, error) {
			spawned = true
			return Report{}, nil
		},
	})
	if launched {
		t.Fatal("MaybeRunBackground returned true for disabled dream")
	}
	if spawned {
		t.Fatal("subprocess must not be spawned when disabled")
	}
}

func TestMaybeRunBackground_NotSpawnedWhenNotDue(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	writeDueState(t, root, now.Add(-1*24*time.Hour)) // 1 day ago, interval 7d → not due

	var spawned bool
	launched := Scheduler{}.MaybeRunBackground(context.Background(), BackgroundDeps{
		MemoryRoot: root,
		Config:     NewConfig(nil, 7, 20),
		Now:        fixedNow(now),
		Spawn: func(context.Context, string) (Report, error) {
			spawned = true
			return Report{}, nil
		},
	})
	if launched || spawned {
		t.Fatalf("not-due run must not launch/spawn (launched=%v spawned=%v)", launched, spawned)
	}
}

func TestMaybeRunBackground_SpawnsAndNoticesOnChanges(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 2, 10, 9, 0, 0, 0, time.UTC)
	writeDueState(t, root, now.Add(-10*24*time.Hour)) // due

	var (
		mu       sync.Mutex
		gotDir   string
		reported *Report
		done     = make(chan struct{})
	)
	launched := Scheduler{}.MaybeRunBackground(context.Background(), BackgroundDeps{
		MemoryRoot: root,
		ProjectDir: "/proj/x",
		Config:     NewConfig(nil, 7, 20),
		Now:        fixedNow(now),
		Spawn: func(_ context.Context, dir string) (Report, error) {
			mu.Lock()
			gotDir = dir
			mu.Unlock()
			return Report{Merged: 2, Deduped: 1}, nil
		},
		OnReport: func(r Report) {
			mu.Lock()
			reported = &r
			mu.Unlock()
			close(done)
		},
	})
	if !launched {
		t.Fatal("due run must launch a background spawn")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnReport not called within timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotDir != "/proj/x" {
		t.Fatalf("spawn got dir %q, want /proj/x", gotDir)
	}
	if reported == nil || reported.Merged != 2 {
		t.Fatalf("OnReport got %+v, want Merged=2", reported)
	}
}

func TestMaybeRunBackground_SkippedRunIsSilent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	writeDueState(t, root, now.Add(-10*24*time.Hour)) // due

	spawnDone := make(chan struct{})
	var noticed bool
	var mu sync.Mutex
	Scheduler{}.MaybeRunBackground(context.Background(), BackgroundDeps{
		MemoryRoot: root,
		Config:     NewConfig(nil, 7, 20),
		Now:        fixedNow(now),
		Spawn: func(context.Context, string) (Report, error) {
			// A skipped/lock-held run emits an all-zero report with no error.
			defer close(spawnDone)
			return Report{}, nil
		},
		OnReport: func(Report) {
			mu.Lock()
			noticed = true
			mu.Unlock()
		},
	})
	<-spawnDone
	// Give the goroutine a moment past Spawn to (not) call OnReport.
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if noticed {
		t.Fatal("all-zero (skipped/no-op) report must not produce a notice")
	}
}

func TestMaybeRunBackground_FailureIsSilent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)
	writeDueState(t, root, now.Add(-10*24*time.Hour)) // due

	spawnDone := make(chan struct{})
	var noticed bool
	var mu sync.Mutex
	Scheduler{}.MaybeRunBackground(context.Background(), BackgroundDeps{
		MemoryRoot: root,
		Config:     NewConfig(nil, 7, 20),
		Now:        fixedNow(now),
		Spawn: func(context.Context, string) (Report, error) {
			defer close(spawnDone)
			return Report{Merged: 5}, errors.New("boom")
		},
		OnReport: func(Report) {
			mu.Lock()
			noticed = true
			mu.Unlock()
		},
	})
	<-spawnDone
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if noticed {
		t.Fatal("a failed background run must be silent (no notice)")
	}
}

func TestMaybeRunBackground_NilSpawn(t *testing.T) {
	if (Scheduler{}).MaybeRunBackground(context.Background(), BackgroundDeps{}) {
		t.Fatal("nil Spawn must yield false (no-op)")
	}
}
