package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// policyTool is a minimal AgentTool whose only meaningful property is its name —
// the tool policy matches on nothing else.
type policyTool struct{ name string }

func (t policyTool) Name() string        { return t.name }
func (t policyTool) Description() string { return "stub" }
func (t policyTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t policyTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}
func (t policyTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{}, nil
}

// toolSet builds a tool set from names.
func toolSet(names ...string) []agentcore.AgentTool {
	out := make([]agentcore.AgentTool, 0, len(names))
	for _, n := range names {
		out = append(out, policyTool{name: n})
	}
	return out
}

// names extracts the tool names from a set, for comparison.
func names(tools []agentcore.AgentTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name())
	}
	return out
}

// TestSplitToolNames covers the accepted input forms: a single value, repeated
// flags, comma-separated values, mixed forms, and whitespace/empty entries.
func TestSplitToolNames(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"single", []string{"read"}, []string{"read"}},
		{"repeated flag", []string{"read", "grep"}, []string{"read", "grep"}},
		{"comma", []string{"read,grep"}, []string{"read", "grep"}},
		{"mixed", []string{"read,grep", "bash"}, []string{"read", "grep", "bash"}},
		{"whitespace and empties", []string{"read, ,grep", "  bash  "}, []string{"read", "grep", "bash"}},
		{"case folded", []string{"Read", "BASH"}, []string{"read", "bash"}},
		{"only empties", []string{"", " ", ",,"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitToolNames(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("SplitToolNames(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("SplitToolNames(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
}

// TestApplyToolPolicy covers allow-only, deny-only, the overlap (deny wins), the
// unconstrained no-op, and filtering down to an empty set.
func TestApplyToolPolicy(t *testing.T) {
	all := toolSet("read", "write", "bash", "grep")
	tests := []struct {
		name   string
		policy ToolPolicy
		want   []string
	}{
		{"unconstrained is a no-op", ToolPolicy{}, []string{"read", "write", "bash", "grep"}},
		{"allow only", NewToolPolicy([]string{"read,grep"}, nil), []string{"read", "grep"}},
		{"deny only", NewToolPolicy(nil, []string{"bash"}), []string{"read", "write", "grep"}},
		{"deny wins over allow", NewToolPolicy([]string{"read,bash"}, []string{"bash"}), []string{"read"}},
		{"case-insensitive allow", NewToolPolicy([]string{"Read", "GREP"}, nil), []string{"read", "grep"}},
		{"case-insensitive deny", NewToolPolicy(nil, []string{"Bash"}), []string{"read", "write", "grep"}},
		{"filters to empty", NewToolPolicy([]string{"read"}, []string{"read"}), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := names(ApplyToolPolicy(all, tc.policy))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("ApplyToolPolicy = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestApplyToolPolicyEmptyToolSet confirms an empty input (--no-tools) is left
// alone rather than being treated as "everything denied".
func TestApplyToolPolicyEmptyToolSet(t *testing.T) {
	if got := ApplyToolPolicy(nil, NewToolPolicy([]string{"read"}, nil)); got != nil {
		t.Errorf("ApplyToolPolicy(nil, ...) = %v, want nil", got)
	}
}

// TestValidateToolPolicyAccepts confirms known names — including case variants —
// pass validation.
func TestValidateToolPolicyAccepts(t *testing.T) {
	all := toolSet("read", "bash")
	for _, policy := range []ToolPolicy{
		{},
		NewToolPolicy([]string{"read"}, nil),
		NewToolPolicy([]string{"Read"}, []string{"BASH"}),
		NewToolPolicy(nil, []string{"bash"}),
	} {
		if err := ValidateToolPolicy(all, policy); err != nil {
			t.Errorf("ValidateToolPolicy(%+v) = %v, want nil", policy, err)
		}
	}
}

// TestValidateToolPolicyReportsAllUnknown is the anti-typo guarantee: every bad
// name from both flags is reported in one error, alongside the available names,
// so a user with two typos fixes both in one round.
func TestValidateToolPolicyReportsAllUnknown(t *testing.T) {
	all := toolSet("read", "bash", "grep")
	err := ValidateToolPolicy(all, NewToolPolicy([]string{"raed,gerp"}, []string{"bahs"}))
	if err == nil {
		t.Fatal("ValidateToolPolicy = nil, want an error for the misspelled names")
	}
	var policyErr *ToolPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("error type = %T, want *ToolPolicyError (exit-code mapping depends on it)", err)
	}
	if len(policyErr.UnknownAllowed) != 2 {
		t.Errorf("UnknownAllowed = %q, want both misspellings", policyErr.UnknownAllowed)
	}
	if len(policyErr.UnknownDisallowed) != 1 {
		t.Errorf("UnknownDisallowed = %q, want the one misspelling", policyErr.UnknownDisallowed)
	}
	msg := err.Error()
	for _, want := range []string{`"raed"`, `"gerp"`, `"bahs"`, "available:", "read", "bash", "grep"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q is missing %q", msg, want)
		}
	}
}

// TestValidateToolPolicyDeduplicatesUnknown confirms a name repeated across
// values is reported once.
func TestValidateToolPolicyDeduplicatesUnknown(t *testing.T) {
	err := ValidateToolPolicy(toolSet("read"), NewToolPolicy([]string{"raed", "raed"}, nil))
	var policyErr *ToolPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("error = %v, want *ToolPolicyError", err)
	}
	if len(policyErr.UnknownAllowed) != 1 {
		t.Errorf("UnknownAllowed = %q, want one entry", policyErr.UnknownAllowed)
	}
}

// TestValidateToolPolicySkipsEmptyToolSet confirms --no-tools does not turn every
// policy name into an error.
func TestValidateToolPolicySkipsEmptyToolSet(t *testing.T) {
	if err := ValidateToolPolicy(nil, NewToolPolicy([]string{"anything"}, nil)); err != nil {
		t.Errorf("ValidateToolPolicy(nil tools) = %v, want nil", err)
	}
}
