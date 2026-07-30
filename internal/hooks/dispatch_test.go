package hooks

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestNewDispatcherNilOnEmpty(t *testing.T) {
	if d := NewDispatcher(nil, "/tmp", nil); d != nil {
		t.Fatal("expected nil dispatcher for empty set")
	}
	if d := NewDispatcher(HookSet{}, "/tmp", nil); d != nil {
		t.Fatal("expected nil dispatcher for empty set")
	}
}

func TestNilDispatcherDispatchIsNoOp(t *testing.T) {
	var d *Dispatcher
	dec := d.Dispatch(context.Background(), "PreToolUse", "bash", HookInput{})
	if dec.Block || dec.AdditionalContext != "" || dec.UpdatedInput != nil {
		t.Fatalf("expected empty decision, got %+v", dec)
	}
}

func TestDispatchMergesContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh -c not available on windows")
	}
	set := HookSet{
		"PostToolUse": {
			{Matcher: "*", Hooks: []HookConfig{
				{Command: `echo '{"additionalContext":"first"}'`},
				{Command: `echo '{"additionalContext":"second"}'`},
			}},
		},
	}
	d := NewDispatcher(set, t.TempDir(), nil)
	dec := d.Dispatch(context.Background(), "PostToolUse", "write", HookInput{})
	if dec.AdditionalContext != "first\nsecond" {
		t.Fatalf("expected merged context, got %q", dec.AdditionalContext)
	}
}

func TestDispatchPreToolUseFirstBlockShortCircuits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh -c not available on windows")
	}
	set := HookSet{
		"PreToolUse": {
			{Matcher: "*", Hooks: []HookConfig{
				{Command: `echo "stop" >&2; exit 2`},
				{Command: `echo '{"updatedInput":{"changed":true}}'`},
			}},
		},
	}
	d := NewDispatcher(set, t.TempDir(), nil)
	dec := d.Dispatch(context.Background(), "PreToolUse", "bash", HookInput{})
	if !dec.Block {
		t.Fatal("expected block")
	}
	if dec.UpdatedInput != nil {
		t.Fatalf("expected short-circuit before rewrite, got %s", dec.UpdatedInput)
	}
}

func TestDispatchFailOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh -c not available on windows")
	}
	set := HookSet{
		"PreToolUse": {
			{Matcher: "*", Hooks: []HookConfig{
				{Command: `exit 1`},
				{Command: `echo '{"additionalContext":"survived"}'`},
			}},
		},
	}
	var warn bytes.Buffer
	d := NewDispatcher(set, t.TempDir(), &warn)
	dec := d.Dispatch(context.Background(), "PreToolUse", "bash", HookInput{})
	if dec.Block {
		t.Fatal("failed hook must not block (fail-open)")
	}
	if dec.AdditionalContext != "survived" {
		t.Fatalf("expected later hook to still run, got %q", dec.AdditionalContext)
	}
	if !strings.Contains(warn.String(), "PreToolUse") {
		t.Fatalf("expected failure warning, got %q", warn.String())
	}
}

func TestDispatchUpdatedInputLastWins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh -c not available on windows")
	}
	set := HookSet{
		"PreToolUse": {
			{Matcher: "*", Hooks: []HookConfig{
				{Command: `echo '{"updatedInput":{"n":1}}'`},
				{Command: `echo '{"updatedInput":{"n":2}}'`},
			}},
		},
	}
	d := NewDispatcher(set, t.TempDir(), nil)
	dec := d.Dispatch(context.Background(), "PreToolUse", "bash", HookInput{})
	if string(dec.UpdatedInput) != `{"n":2}` {
		t.Fatalf("expected last writer wins, got %s", dec.UpdatedInput)
	}
}
