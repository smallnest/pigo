package runtime

// Tests for tiered priority resolution (US-009, #335): same-name commands
// across sources resolve by tier (built-in > project > global > package >
// settings > CLI), the loser is shadowed with its tier recorded, and same-tier
// adds override silently (last-write-wins, no shadow entry).

import "testing"

// TestSlashTierProjectOverridesGlobal: a project-tier template added after a
// global one wins; the global entry is shadowed with TierGlobal.
func TestSlashTierProjectOverridesGlobal(t *testing.T) {
	r := NewSlashRegistry()
	r.AddUser(SlashCommand{Name: "t", Expand: func(string) string { return "global" }})
	r.AddProject(SlashCommand{Name: "t", Expand: func(string) string { return "project" }})
	cmd, ok := r.Lookup("t")
	if !ok {
		t.Fatalf("command %q not found", "t")
	}
	if got := cmd.Expand(""); got != "project" {
		t.Errorf("project must override global, got %q", got)
	}
	sh := r.Shadowed()
	if len(sh) != 1 || sh[0].Name != "t" || sh[0].Tier != TierGlobal {
		t.Errorf("global should be shadowed, got %v", sh)
	}
}

// TestSlashTierGlobalOverridesSettings: a global template added after a
// settings-tier one wins; the settings entry is shadowed with TierSettings.
func TestSlashTierGlobalOverridesSettings(t *testing.T) {
	r := NewSlashRegistry()
	r.AddSettings(SlashCommand{Name: "t", Expand: func(string) string { return "settings" }})
	r.AddUser(SlashCommand{Name: "t", Expand: func(string) string { return "global" }})
	cmd, ok := r.Lookup("t")
	if !ok {
		t.Fatalf("command %q not found", "t")
	}
	if got := cmd.Expand(""); got != "global" {
		t.Errorf("global must override settings, got %q", got)
	}
	sh := r.Shadowed()
	if len(sh) != 1 || sh[0].Name != "t" || sh[0].Tier != TierSettings {
		t.Errorf("settings should be shadowed, got %v", sh)
	}
}

// TestSlashTierBuiltinOverridesProject: a built-in added after a project-tier
// template wins; the project entry is shadowed with TierProject.
func TestSlashTierBuiltinOverridesProject(t *testing.T) {
	r := NewSlashRegistry()
	r.AddProject(SlashCommand{Name: "t", Expand: func(string) string { return "project" }})
	r.AddBuiltin(SlashCommand{Name: "t", Action: func(string) string { return "builtin" }})
	cmd, ok := r.Lookup("t")
	if !ok || cmd.Source != SourceBuiltin {
		t.Fatalf("built-in must override project, got ok=%v source=%v", ok, cmd.Source)
	}
	sh := r.Shadowed()
	if len(sh) != 1 || sh[0].Name != "t" || sh[0].Tier != TierProject {
		t.Errorf("project should be shadowed, got %v", sh)
	}
}

// TestSlashTierSameTierLastWriteWins: two same-tier (global) adds resolve to the
// last one, with no shadow entry recorded.
func TestSlashTierSameTierLastWriteWins(t *testing.T) {
	r := NewSlashRegistry()
	r.AddUser(SlashCommand{Name: "t", Expand: func(string) string { return "first" }})
	r.AddUser(SlashCommand{Name: "t", Expand: func(string) string { return "second" }})
	cmd, ok := r.Lookup("t")
	if !ok {
		t.Fatalf("command %q not found", "t")
	}
	if got := cmd.Expand(""); got != "second" {
		t.Errorf("same-tier last-write-wins, got %q", got)
	}
	if len(r.Shadowed()) != 0 {
		t.Errorf("same-tier override must not shadow, got %v", r.Shadowed())
	}
}

// TestSlashTierFullOrdering exercises the full tier ladder: adding lowest-first
// up to built-in, the built-in wins and every lower tier is shadowed.
func TestSlashTierFullOrdering(t *testing.T) {
	r := NewSlashRegistry()
	r.AddCLI(SlashCommand{Name: "t", Expand: func(string) string { return "cli" }})
	r.AddSettings(SlashCommand{Name: "t", Expand: func(string) string { return "settings" }})
	r.AddPackage(SlashCommand{Name: "t", Expand: func(string) string { return "package" }})
	r.AddUser(SlashCommand{Name: "t", Expand: func(string) string { return "global" }})
	r.AddProject(SlashCommand{Name: "t", Expand: func(string) string { return "project" }})
	r.AddBuiltin(SlashCommand{Name: "t", Action: func(string) string { return "builtin" }})
	cmd, ok := r.Lookup("t")
	if !ok || cmd.Source != SourceBuiltin {
		t.Fatalf("built-in must win the full ladder, got ok=%v source=%v", ok, cmd.Source)
	}
	// Five lower-tier commands (cli, settings, package, global, project) lost.
	if len(r.Shadowed()) != 5 {
		t.Errorf("expected 5 shadowed entries, got %d: %v", len(r.Shadowed()), r.Shadowed())
	}
}

// Tests for ParseUserCommand wiring to the expansion engine (US-003, #333):
// Expand tokenizes args via SplitArgs and expands via ExpandTemplate, falling
// back to the raw arg string as $ARGUMENTS when tokenization fails.

// TestParseUserCommandPositionalAndQuoted verifies multi-arg invocation,
// quoted-arg preservation, and the ${1:-default} form through ParseUserCommand.
func TestParseUserCommandPositionalAndQuoted(t *testing.T) {
	// /review with no args: $ARGUMENTS expands to empty.
	review, _ := ParseUserCommand("review", []byte("Review: $ARGUMENTS"))
	if got := review.Expand(""); got != "Review: " {
		t.Errorf("no args: got %q, want \"Review: \"", got)
	}
	// /component Button "click handler": quoted arg stays one token ($2).
	comp, _ := ParseUserCommand("component", []byte("name=$1 feat=$2"))
	if got := comp.Expand(`Button "click handler"`); got != "name=Button feat=click handler" {
		t.Errorf("quoted args: got %q", got)
	}
	// ${1:-7} default: no arg -> 7, explicit -> the arg.
	bul, _ := ParseUserCommand("summarize", []byte("in ${1:-7} bullets"))
	if got := bul.Expand(""); got != "in 7 bullets" {
		t.Errorf("default no arg: got %q", got)
	}
	if got := bul.Expand("5"); got != "in 5 bullets" {
		t.Errorf("explicit arg: got %q", got)
	}
}

// TestParseUserCommandSplitFailureFallback verifies that an unterminated quote
// (SplitArgs error) falls back to treating the raw arg string as $ARGUMENTS.
func TestParseUserCommandSplitFailureFallback(t *testing.T) {
	cmd, _ := ParseUserCommand("t", []byte("echo $ARGUMENTS"))
	if got := cmd.Expand(`"unterminated`); got != `echo "unterminated` {
		t.Errorf("split-failure fallback: got %q, want raw string as $ARGUMENTS", got)
	}
	// A no-placeholder template with split failure still appends the raw string.
	bare, _ := ParseUserCommand("note", []byte("Take a note"))
	if got := bare.Expand(`"unterminated`); got != "Take a note\n\n\"unterminated" {
		t.Errorf("no-placeholder split failure: got %q", got)
	}
}

// TestParseUserCommandArgumentHintAndDescriptionFallback (US-004, #334):
// argument-hint is parsed from frontmatter, and description falls back to the
// first non-empty body line when absent.
func TestParseUserCommandArgumentHintAndDescriptionFallback(t *testing.T) {
	// Both description and argument-hint.
	cmd, err := ParseUserCommand("pr", []byte("---\ndescription: review PR\nargument-hint: \"<PR-URL>\"\n---\nReview the PR"))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Description != "review PR" {
		t.Errorf("description = %q, want \"review PR\"", cmd.Description)
	}
	if cmd.ArgumentHint != "<PR-URL>" {
		t.Errorf("argument-hint = %q, want \"<PR-URL>\"", cmd.ArgumentHint)
	}
	// Only argument-hint: description falls back to first non-empty body line.
	hintOnly, _ := ParseUserCommand("wr", []byte("---\nargument-hint: \"[instructions]\"\n---\nFinish the current task\nend-to-end"))
	if hintOnly.Description != "Finish the current task" {
		t.Errorf("description fallback = %q, want first body line", hintOnly.Description)
	}
	if hintOnly.ArgumentHint != "[instructions]" {
		t.Errorf("argument-hint = %q, want \"[instructions]\"", hintOnly.ArgumentHint)
	}
	// Only description: argument-hint stays empty.
	descOnly, _ := ParseUserCommand("cl", []byte("---\ndescription: audit changelog\n---\nAudit changelog entries"))
	if descOnly.Description != "audit changelog" {
		t.Errorf("description = %q", descOnly.Description)
	}
	if descOnly.ArgumentHint != "" {
		t.Errorf("argument-hint should be empty, got %q", descOnly.ArgumentHint)
	}
	// No frontmatter at all: description falls back to first non-empty line.
	neither, _ := ParseUserCommand("bare", []byte("First line is the desc\nSecond line is body"))
	if neither.Description != "First line is the desc" {
		t.Errorf("no-frontmatter fallback = %q, want first line", neither.Description)
	}
	if neither.ArgumentHint != "" {
		t.Errorf("argument-hint should be empty, got %q", neither.ArgumentHint)
	}
}
