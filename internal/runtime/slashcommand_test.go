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
