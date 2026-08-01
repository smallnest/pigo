package run

// Tests for the persistent-memory loop wiring (#481): OpenMemoryStore's
// enabled/disabled contract, MemoryRootFromTools resolving through the opened
// store, and TodoReminders registering the memory reminder provider alongside
// the todo one.

import (
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/memory"
)

// TestOpenMemoryStoreDisabled verifies memory.enabled=false yields (nil, nil):
// a disabled store is not an error, so the caller degrades to file-based
// auto-memory without logging a failure.
func TestOpenMemoryStoreDisabled(t *testing.T) {
	store, err := OpenMemoryStore(false)
	if err != nil {
		t.Fatalf("OpenMemoryStore(false) err = %v, want nil", err)
	}
	if store != nil {
		t.Fatalf("OpenMemoryStore(false) store = %v, want nil", store)
	}
}

// TestMemoryDirHonorsPIGOHome verifies MemoryDir roots the store at
// $PIGO_HOME/memory when the override is set.
func TestMemoryDirHonorsPIGOHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_HOME", dir)
	if got, want := MemoryDir(), filepath.Join(dir, "memory"); got != want {
		t.Errorf("MemoryDir() = %q, want %q", got, want)
	}
}

// TestMemoryRootFromToolsPresent verifies the root is resolved through the
// memory_search tool's Store.Root() when one is wired into the tool set.
func TestMemoryRootFromToolsPresent(t *testing.T) {
	root := t.TempDir()
	store, err := memory.Open(filepath.Join(root, "index.db"), root, "")
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	defer store.Close()

	tools := []agentcore.AgentTool{&agenttool.MemorySearchTool{Store: store}}
	if got := MemoryRootFromTools(tools); got != root {
		t.Errorf("MemoryRootFromTools = %q, want %q", got, root)
	}
}

// TestMemoryRootFromToolsAbsent verifies the root is "" when no memory_search
// tool (or a store-less one) is present.
func TestMemoryRootFromToolsAbsent(t *testing.T) {
	if got := MemoryRootFromTools(nil); got != "" {
		t.Errorf("MemoryRootFromTools(nil) = %q, want empty", got)
	}
	tools := []agentcore.AgentTool{&agenttool.MemorySearchTool{Store: nil}}
	if got := MemoryRootFromTools(tools); got != "" {
		t.Errorf("MemoryRootFromTools(store-less) = %q, want empty", got)
	}
}

// TestTodoRemindersRegistersMemoryProvider verifies TodoReminders builds a
// non-empty registry from a memory_search tool alone, and stays nil when no
// provider-bearing tool is present.
func TestTodoRemindersRegistersMemoryProvider(t *testing.T) {
	root := t.TempDir()
	store, err := memory.Open(filepath.Join(root, "index.db"), root, "")
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	defer store.Close()

	reg := TodoReminders([]agentcore.AgentTool{&agenttool.MemorySearchTool{Store: store}})
	if reg == nil || reg.Empty() {
		t.Fatal("TodoReminders with a memory_search tool should yield a non-empty registry")
	}

	if reg := TodoReminders(nil); reg != nil {
		t.Errorf("TodoReminders(nil) = %v, want nil", reg)
	}
}
