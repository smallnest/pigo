package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseHookOutput(t *testing.T) {
	t.Run("empty is no-op", func(t *testing.T) {
		if _, ok := parseHookOutput([]byte("   ")); ok {
			t.Fatal("expected ok=false for empty")
		}
	})
	t.Run("non-json is no-op", func(t *testing.T) {
		if _, ok := parseHookOutput([]byte("just some text")); ok {
			t.Fatal("expected ok=false for non-json")
		}
	})
	t.Run("valid decision block", func(t *testing.T) {
		out, ok := parseHookOutput([]byte(`{"decision":"block","reason":"nope"}`))
		if !ok || !out.blocks() || out.Reason != "nope" {
			t.Fatalf("unexpected: ok=%v out=%+v", ok, out)
		}
	})
	t.Run("additionalContext", func(t *testing.T) {
		out, ok := parseHookOutput([]byte(`{"additionalContext":"extra"}`))
		if !ok || out.AdditionalContext != "extra" {
			t.Fatalf("unexpected: ok=%v out=%+v", ok, out)
		}
	})
	t.Run("updatedInput preserved as raw", func(t *testing.T) {
		out, ok := parseHookOutput([]byte(`{"updatedInput":{"a":1}}`))
		if !ok || string(out.UpdatedInput) != `{"a":1}` {
			t.Fatalf("unexpected: ok=%v raw=%s", ok, out.UpdatedInput)
		}
	})
}

func TestHookInputNoSecretFields(t *testing.T) {
	// A fully-populated payload must never carry credential-like keys: the
	// struct is a whitelist, so marshaling can only emit its declared fields.
	in := HookInput{
		EventType: "PreToolUse", SessionID: "s", ProjectDir: "/p", ToolName: "bash",
		ToolInput: json.RawMessage(`{"cmd":"ls"}`), Prompt: "hi", Message: "m",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	forbidden := []string{"api_key", "apikey", "token", "credential", "password", "secret", "authorization"}
	lower := strings.ToLower(string(data))
	for _, k := range forbidden {
		if strings.Contains(lower, k) {
			t.Fatalf("payload contains forbidden key %q: %s", k, data)
		}
	}
}

func TestHookOutputBlocks(t *testing.T) {
	tests := []struct {
		name string
		out  HookOutput
		want bool
	}{
		{"decision block", HookOutput{Decision: "block"}, true},
		{"decision BLOCK case-insensitive", HookOutput{Decision: "BLOCK"}, true},
		{"decision approve", HookOutput{Decision: "approve"}, false},
		{"empty decision", HookOutput{}, false},
		{"continue false blocks", HookOutput{Continue: ptr(false)}, true},
		{"continue true allows", HookOutput{Continue: ptr(true)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.out.blocks(); got != tt.want {
				t.Fatalf("blocks() = %v, want %v", got, tt.want)
			}
		})
	}
}
