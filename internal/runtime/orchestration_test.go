package runtime

// Tests for sub-agent orchestration, skills, and slash-commands (US-027/028/029,
// #45). The sub-agent integration test drives the flagship parent→child→parent
// path through the faux provider seam (对标 faux_provider_test.go): the parent
// loop calls a sub-agent tool, the child runs its own scripted loop, and the
// child's final text is fed back as the parent's tool result. Skills and
// slash-commands get focused load/parse unit tests.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
)

// TestSubAgentParentChildParent is acceptance-critical: a parent agent loop
// delegates to a sub-agent via a tool call, the child runs an independent loop
// with its own context and provider, and the child's final assistant text is
// returned to the parent as the tool result (父→子→父). Both loops are driven by
// faux providers over the real StreamFnFromProvider seam.
func TestSubAgentParentChildParent(t *testing.T) {
	// Child provider: a single scripted turn producing the delegated answer.
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{textTurn("child result: 42")},
	}
	// The sub-agent tool spawns a child loop over its own context + provider.
	sub := NewSubAgentTool(SubAgentSpec{
		Name:         "researcher",
		Description:  "delegate research to a fresh sub-agent",
		SystemPrompt: "you are a researcher",
		Tools:        nil,
		NewRunConfig: func() RunConfig {
			return RunConfig{
				LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
				Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
			}
		},
	})

	// Parent provider: turn 1 calls the sub-agent, turn 2 (after the tool result
	// is fed back) produces the final answer.
	parent := &fauxProvider{
		name:   "faux-parent",
		models: []provider.Model{{Provider: "faux-parent", ID: "parent"}},
		turns: []fauxTurn{
			toolCallTurn("call-sub", "researcher", `{"prompt":"find the answer"}`),
			textTurn("final: incorporated child result"),
		},
	}
	cfg := newFauxRunCfg(parent, sub)
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("delegate this")}},
	}}

	kinds, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	// The sub-agent tool must have executed exactly once in the parent loop.
	if got := countKind(kinds, agentcore.EventToolExecutionStart); got != 1 {
		t.Errorf("expected 1 sub-agent tool execution, got %d in %v", got, kinds)
	}
	// The child provider must have been driven independently.
	if child.callCount() != 1 {
		t.Errorf("child provider called %d times, want 1", child.callCount())
	}
	// The tool result fed back to the parent must carry the child's final text.
	var toolResult *agentcore.ToolResultMessage
	for i := range msgs {
		if tr, ok := msgs[i].(agentcore.ToolResultMessage); ok {
			toolResult = &tr
			break
		}
	}
	if toolResult == nil {
		t.Fatalf("no tool result message in parent transcript: %+v", msgs)
	}
	if got := textContentOf(toolResult.Content); got != "child result: 42" {
		t.Errorf("sub-agent result = %q, want %q (child final text fed to parent)", got, "child result: 42")
	}
	if toolResult.IsError {
		t.Errorf("sub-agent tool result should not be an error")
	}
	// The parent's final message incorporates the delegation.
	final := agentcore.LastAssistantOf(msgs)
	if final == nil || textContentOf(final.Content) != "final: incorporated child result" {
		t.Errorf("parent final text = %+v, want the post-delegation answer", final)
	}
}

// TestSubAgentConcurrent verifies multiple sub-agents can run concurrently:
// the parent issues two parallel sub-agent tool calls in one turn, each spawning
// an independent child loop, and both results are fed back.
func TestSubAgentConcurrent(t *testing.T) {
	mkChild := func(answer string) *SubAgentTool {
		cp := &fauxProvider{
			name:   "faux-child-" + answer,
			models: []provider.Model{{Provider: "c", ID: "c"}},
			turns:  []fauxTurn{textTurn(answer)},
		}
		return NewSubAgentTool(SubAgentSpec{
			Name:        "agent-" + answer,
			Description: "child " + answer,
			NewRunConfig: func() RunConfig {
				return RunConfig{
					LoopConfig: LoopConfig{Model: "c", Stream: provider.StreamFnFromProvider(cp)},
					Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
				}
			},
		})
	}
	a, b := mkChild("alpha"), mkChild("beta")

	// A single parent turn emitting two tool calls → both run in the same batch.
	twoCall := fauxTurn{
		provider.StreamStartEvent{Partial: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}},
		provider.StreamToolCallEvent{Partial: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{
			agentcore.NewToolCallContent("c1", "agent-alpha", json.RawMessage(`{"prompt":"go"}`)),
			agentcore.NewToolCallContent("c2", "agent-beta", json.RawMessage(`{"prompt":"go"}`)),
		}}},
		provider.StreamDoneEvent{Message: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonToolUse, Content: agentcore.ContentList{
			agentcore.NewToolCallContent("c1", "agent-alpha", json.RawMessage(`{"prompt":"go"}`)),
			agentcore.NewToolCallContent("c2", "agent-beta", json.RawMessage(`{"prompt":"go"}`)),
		}}},
	}
	parent := &fauxProvider{
		name:   "faux-parent",
		models: []provider.Model{{Provider: "p", ID: "p"}},
		turns:  []fauxTurn{twoCall, textTurn("done")},
	}
	cfg := newFauxRunCfg(parent, a, b)
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("delegate both")}},
	}}

	_, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	got := map[string]string{}
	for _, m := range msgs {
		if tr, ok := m.(agentcore.ToolResultMessage); ok {
			got[tr.ToolCallID] = textContentOf(tr.Content)
		}
	}
	if got["c1"] != "alpha" || got["c2"] != "beta" {
		t.Errorf("concurrent sub-agent results = %v, want c1=alpha c2=beta", got)
	}
}

// TestSubAgentEmptyPromptErrors verifies a sub-agent invoked with no prompt
// fails cleanly rather than spawning an empty child.
func TestSubAgentEmptyPromptErrors(t *testing.T) {
	sub := NewSubAgentTool(SubAgentSpec{
		Name:         "x",
		Description:  "x",
		NewRunConfig: func() RunConfig { return RunConfig{} },
	})
	if _, err := sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":""}`), nil); err == nil {
		t.Error("empty prompt must error")
	}
}

// TestSubAgentFailedChildErrors verifies a child whose final turn stopped on
// error/aborted is surfaced to the parent as a tool error (not a silent
// success), so the parent model learns the delegation failed.
func TestSubAgentFailedChildErrors(t *testing.T) {
	// A child turn that ends with StopReason=error carrying diagnostic text.
	errTurn := func(text string) fauxTurn {
		partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
		withText := partial
		withText.Content = agentcore.ContentList{agentcore.NewTextContent(text)}
		final := withText
		final.StopReason = agentcore.StopReasonError
		return fauxTurn{
			provider.StreamStartEvent{Partial: partial},
			provider.StreamTextEvent{Partial: withText},
			provider.StreamDoneEvent{Message: final},
		}
	}
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{errTurn("provider blew up")},
	}
	sub := NewSubAgentTool(SubAgentSpec{
		Name:        "researcher",
		Description: "delegate",
		NewRunConfig: func() RunConfig {
			return RunConfig{
				LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
				Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
			}
		},
	})
	_, err := sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
	if err == nil {
		t.Fatal("a child that stopped on error must surface as a tool error")
	}
	if !strings.Contains(err.Error(), "provider blew up") {
		t.Errorf("error should carry the child's diagnostic text, got %v", err)
	}
}

// --- Skills -----------------------------------------------------------------

// TestParseSkill verifies frontmatter + body parsing, including the name/body
// split and the required-description guard.
func TestParseSkill(t *testing.T) {
	content := []byte("---\nname: summarize\ndescription: summarize a file\nallowed-tools:\n  - read\n---\nYou summarize files.\nBe concise.\n")
	sk, err := ParseSkill("summarize.md", content)
	if err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
	if sk.Frontmatter.Name != "summarize" {
		t.Errorf("name = %q, want summarize", sk.Frontmatter.Name)
	}
	if sk.Frontmatter.Description != "summarize a file" {
		t.Errorf("description = %q", sk.Frontmatter.Description)
	}
	if len(sk.Frontmatter.AllowedTools) != 1 || sk.Frontmatter.AllowedTools[0] != "read" {
		t.Errorf("allowed-tools = %v, want [read]", sk.Frontmatter.AllowedTools)
	}
	if !strings.Contains(sk.Body, "You summarize files.") {
		t.Errorf("body missing instructions: %q", sk.Body)
	}
}

// TestParseSkillDefaultsNameToFile verifies a skill without an explicit name
// defaults to its file base name.
func TestParseSkillDefaultsNameToFile(t *testing.T) {
	sk, err := ParseSkill("/skills/deploy.md", []byte("---\ndescription: deploys\n---\nbody"))
	if err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
	if sk.Frontmatter.Name != "deploy" {
		t.Errorf("name defaulted to %q, want deploy", sk.Frontmatter.Name)
	}
}

// TestParseSkillRequiresDescription verifies the description guard.
func TestParseSkillRequiresDescription(t *testing.T) {
	if _, err := ParseSkill("x.md", []byte("---\nname: x\n---\nbody")); err == nil {
		t.Error("skill without description must error")
	}
	if _, err := ParseSkill("x.md", []byte("no frontmatter here")); err == nil {
		t.Error("skill without frontmatter must error")
	}
}

// TestLoadSkillsDir verifies loading flat *.md skills and nested <name>/SKILL.md,
// sorted by name; a missing dir is not an error.
func TestLoadSkillsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "beta.md"), []byte("---\ndescription: beta skill\n---\nbeta body"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "alpha")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "SKILL.md"), []byte("---\nname: alpha\ndescription: alpha skill\n---\nalpha body"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := LoadSkillsDir(dir)
	if err != nil {
		t.Fatalf("LoadSkillsDir: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("loaded %d skills, want 2", len(skills))
	}
	if skills[0].Frontmatter.Name != "alpha" || skills[1].Frontmatter.Name != "beta" {
		t.Errorf("skills not sorted by name: %q, %q", skills[0].Frontmatter.Name, skills[1].Frontmatter.Name)
	}

	// Missing directory → no skills, no error.
	empty, err := LoadSkillsDir(filepath.Join(dir, "does-not-exist"))
	if err != nil || empty != nil {
		t.Errorf("missing dir should yield (nil, nil), got (%v, %v)", empty, err)
	}
}

// TestSkillSubAgentSpec verifies a skill materializes as a sub-agent whose
// system prompt is the body, whose tools are filtered by allowed-tools, and
// whose description is surfaced.
func TestSkillSubAgentSpec(t *testing.T) {
	sk := &Skill{
		Frontmatter: SkillFrontmatter{Name: "reader", Description: "reads", AllowedTools: []string{"read"}},
		Body:        "you read files",
	}
	tools := []agentcore.AgentTool{execTool{name: "read"}, execTool{name: "write"}, execTool{name: "bash"}}
	var gotTools []agentcore.AgentTool
	spec := sk.SubAgentSpec(tools, func(resolved []agentcore.AgentTool) RunConfig {
		gotTools = resolved
		return RunConfig{}
	})
	if spec.Name != "reader" || spec.Description != "reads" {
		t.Errorf("spec identity = %q/%q", spec.Name, spec.Description)
	}
	if spec.SystemPrompt != "you read files" {
		t.Errorf("spec system prompt = %q", spec.SystemPrompt)
	}
	if len(spec.Tools) != 1 || spec.Tools[0].Name() != "read" {
		t.Errorf("allowed-tools filter failed, spec tools = %v", spec.Tools)
	}
	// NewRunConfig passes the resolved (filtered) tool set to the factory.
	spec.NewRunConfig()
	if len(gotTools) != 1 || gotTools[0].Name() != "read" {
		t.Errorf("factory received %v, want [read]", gotTools)
	}
}

// --- Slash-commands ---------------------------------------------------------

// TestSlashBuiltinWinsOverUser is acceptance-critical: the conflict priority
// rule keeps a built-in over a same-named user command, recording the shadow.
func TestSlashBuiltinWinsOverUser(t *testing.T) {
	// Register a built-in under a unique name to avoid cross-test pollution.
	name := "compact-test-builtin"
	if _, exists := builtinCommands[name]; !exists {
		RegisterBuiltin(SlashCommand{Name: name, Description: "builtin", Expand: func(string) string { return "BUILTIN" }})
	}
	r := NewSlashRegistry()
	r.AddUser(SlashCommand{Name: name, Expand: func(string) string { return "USER" }})

	cmd, ok := r.Lookup(name)
	if !ok {
		t.Fatalf("command %q not found", name)
	}
	if cmd.Source != SourceBuiltin {
		t.Errorf("built-in must win, got source %v", cmd.Source)
	}
	if got := cmd.Expand(""); got != "BUILTIN" {
		t.Errorf("expanded %q, want BUILTIN (built-in wins)", got)
	}
	shadowed := r.Shadowed()
	if len(shadowed) != 1 || shadowed[0] != name {
		t.Errorf("shadowed = %v, want [%s]", shadowed, name)
	}
}

// TestSlashResolve verifies "/name args" parsing, non-command passthrough, and
// the unknown-command error.
func TestSlashResolve(t *testing.T) {
	r := NewSlashRegistry()
	r.AddUser(SlashCommand{Name: "greet", Expand: func(args string) string { return "hello " + args }})

	// Slash command with args → expanded.
	prompt, handled, err := r.Resolve("/greet world")
	if err != nil || !handled || prompt != "hello world" {
		t.Errorf("Resolve(/greet world) = (%q, %v, %v)", prompt, handled, err)
	}
	// Non-command passthrough.
	prompt, handled, err = r.Resolve("just a normal prompt")
	if err != nil || handled || prompt != "just a normal prompt" {
		t.Errorf("non-command passthrough failed: (%q, %v, %v)", prompt, handled, err)
	}
	// Unknown command errors.
	if _, _, err := r.Resolve("/nope"); err == nil {
		t.Error("unknown command must error")
	}
}

// TestSlashActionCommand verifies an action command runs its side effect via
// ResolveOutcome and reports SlashAction with its status message (no prompt),
// while a prompt command reports SlashPrompt with expanded text.
func TestSlashActionCommand(t *testing.T) {
	r := NewSlashRegistry()
	var ran string
	r.AddBuiltin(SlashCommand{
		Name:        "model",
		Description: "switch model",
		Action:      func(args string) string { ran = args; return "switched to " + args },
	})
	r.AddUser(SlashCommand{Name: "greet", Expand: func(args string) string { return "hello " + args }})

	// Action command: runs the side effect and returns a status message.
	out, err := r.ResolveOutcome("/model gpt-5")
	if err != nil {
		t.Fatalf("ResolveOutcome(/model) error: %v", err)
	}
	if !out.Handled || out.Kind != SlashAction {
		t.Errorf("action command: got handled=%v kind=%v, want true/SlashAction", out.Handled, out.Kind)
	}
	if ran != "gpt-5" {
		t.Errorf("action side effect not run with args: got %q", ran)
	}
	if out.Message != "switched to gpt-5" || out.Prompt != "" {
		t.Errorf("action outcome = {msg:%q prompt:%q}, want status message and empty prompt", out.Message, out.Prompt)
	}

	// Prompt command: expands, no action.
	out, err = r.ResolveOutcome("/greet world")
	if err != nil {
		t.Fatalf("ResolveOutcome(/greet) error: %v", err)
	}
	if !out.Handled || out.Kind != SlashPrompt || out.Prompt != "hello world" {
		t.Errorf("prompt outcome = {kind:%v prompt:%q}, want SlashPrompt/hello world", out.Kind, out.Prompt)
	}

	// Non-command passthrough.
	out, err = r.ResolveOutcome("plain text")
	if err != nil || out.Handled || out.Prompt != "plain text" {
		t.Errorf("passthrough = {%v %q %v}, want unhandled verbatim", out.Handled, out.Prompt, err)
	}
}

// TestAddBuiltinDuplicatePanics verifies AddBuiltin rejects a duplicate
// built-in name (a programming error), matching RegisterBuiltin semantics.
func TestAddBuiltinDuplicatePanics(t *testing.T) {
	r := NewSlashRegistry()
	r.AddBuiltin(SlashCommand{Name: "dup", Action: func(string) string { return "" }})
	defer func() {
		if recover() == nil {
			t.Error("duplicate AddBuiltin must panic")
		}
	}()
	r.AddBuiltin(SlashCommand{Name: "dup", Action: func(string) string { return "" }})
}

// TestAddBuiltinWinsOverUser verifies an instance built-in (AddBuiltin) shadows
// a same-named user command, just like a globally registered built-in.
func TestAddBuiltinWinsOverUser(t *testing.T) {
	r := NewSlashRegistry()
	r.AddBuiltin(SlashCommand{Name: "x", Action: func(string) string { return "builtin" }})
	r.AddUser(SlashCommand{Name: "x", Expand: func(string) string { return "user" }})
	cmd, ok := r.Lookup("x")
	if !ok || cmd.Source != SourceBuiltin {
		t.Errorf("instance built-in must win, got ok=%v source=%v", ok, cmd.Source)
	}
	if len(r.Shadowed()) != 1 || r.Shadowed()[0] != "x" {
		t.Errorf("shadowed = %v, want [x]", r.Shadowed())
	}
}

// TestParseUserCommand verifies $ARGUMENTS substitution, frontmatter description,
// and the append fallback when no placeholder is present.
func TestParseUserCommand(t *testing.T) {
	// With frontmatter + placeholder.
	cmd, err := ParseUserCommand("review", []byte("---\ndescription: review code\n---\nReview this: $ARGUMENTS please"))
	if err != nil {
		t.Fatalf("ParseUserCommand: %v", err)
	}
	if cmd.Description != "review code" {
		t.Errorf("description = %q", cmd.Description)
	}
	if got := cmd.Expand("main.go"); got != "Review this: main.go please" {
		t.Errorf("expand = %q", got)
	}
	// No placeholder: args appended.
	bare, _ := ParseUserCommand("note", []byte("Take a note"))
	if got := bare.Expand("buy milk"); got != "Take a note\n\nbuy milk" {
		t.Errorf("bare expand = %q", got)
	}
	if got := bare.Expand(""); got != "Take a note" {
		t.Errorf("bare expand no args = %q", got)
	}
}

// TestLoadUserCommandsDir verifies loading *.md command templates from a dir,
// sorted by name; a missing dir is not an error.
func TestLoadUserCommandsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.md"), []byte("Deploy $ARGUMENTS now"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test.md"), []byte("---\ndescription: run tests\n---\nRun tests"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds, err := LoadUserCommandsDir(dir)
	if err != nil {
		t.Fatalf("LoadUserCommandsDir: %v", err)
	}
	if len(cmds) != 2 || cmds[0].Name != "deploy" || cmds[1].Name != "test" {
		t.Fatalf("loaded %d commands (want deploy,test sorted): %+v", len(cmds), cmds)
	}
	if got := cmds[0].Expand("prod"); got != "Deploy prod now" {
		t.Errorf("deploy expand = %q", got)
	}

	empty, err := LoadUserCommandsDir(filepath.Join(dir, "missing"))
	if err != nil || empty != nil {
		t.Errorf("missing dir should yield (nil, nil), got (%v, %v)", empty, err)
	}
}
