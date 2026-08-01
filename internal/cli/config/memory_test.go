package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// boolPtr / floatPtr are test helpers for the pointer-valued MemoryConfig fields.
func boolPtr(b bool) *bool        { return &b }
func floatPtr(f float64) *float64 { return &f }

func TestMemoryConfig_ResolveDefaults(t *testing.T) {
	got := MemoryConfig{}.Resolve()
	want := ResolvedMemory{
		Enabled:           true,
		ReconcileOnSearch: true,
		SearchScoreFloor:  0.15,
		CCIndex:           false,
	}
	if got != want {
		t.Fatalf("Resolve() defaults = %+v, want %+v", got, want)
	}
}

func TestMemoryConfig_ResolveOverrides(t *testing.T) {
	got := MemoryConfig{
		Enabled:           boolPtr(false),
		ReconcileOnSearch: boolPtr(false),
		SearchScoreFloor:  floatPtr(0.5),
		CCIndex:           true,
	}.Resolve()
	want := ResolvedMemory{
		Enabled:           false,
		ReconcileOnSearch: false,
		SearchScoreFloor:  0.5,
		CCIndex:           true,
	}
	if got != want {
		t.Fatalf("Resolve() overrides = %+v, want %+v", got, want)
	}
}

// memory.enabled=false must be representable and distinct from "absent".
func TestMemoryConfig_EnabledFalseRepresentable(t *testing.T) {
	if r := (MemoryConfig{Enabled: boolPtr(false)}).Resolve(); r.Enabled {
		t.Fatal("explicit enabled=false should resolve to false")
	}
	if r := (MemoryConfig{}).Resolve(); !r.Enabled {
		t.Fatal("absent enabled should default to true")
	}
}

func TestMemoryConfig_ScoreFloorClamp(t *testing.T) {
	if r := (MemoryConfig{SearchScoreFloor: floatPtr(-1)}).Resolve(); r.SearchScoreFloor != 0 {
		t.Errorf("negative score floor should clamp to 0, got %v", r.SearchScoreFloor)
	}
	if r := (MemoryConfig{SearchScoreFloor: floatPtr(2)}).Resolve(); r.SearchScoreFloor != 1 {
		t.Errorf("score floor >1 should clamp to 1, got %v", r.SearchScoreFloor)
	}
}

func TestParseThresholdFraction(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"40%", 0.40, true},
		{"60%", 0.60, true},
		{"80%", 0.80, true},
		{"100%", 1.0, true},
		{" 75% ", 0.75, true},
		{"0%", 0, false},   // out of range (must be >0)
		{"120%", 0, false}, // out of range (>100%)
		{"-10%", 0, false},
		{"80", 0, false},  // missing %
		{"abc%", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseThresholdFraction(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParseThresholdFraction(%q) = (%v,%v), want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCheckpointConfig_ResolveThresholds(t *testing.T) {
	// Absent → defaults 40/60/80%.
	if got := (CheckpointConfig{}).ResolveThresholds(); !reflect.DeepEqual(got, []float64{0.40, 0.60, 0.80}) {
		t.Errorf("default thresholds = %v, want [0.4 0.6 0.8]", got)
	}
	// Explicit override.
	if got := (CheckpointConfig{Thresholds: []string{"50%", "90%"}}).ResolveThresholds(); !reflect.DeepEqual(got, []float64{0.50, 0.90}) {
		t.Errorf("override thresholds = %v, want [0.5 0.9]", got)
	}
	// Out-of-range entries skipped, valid kept.
	if got := (CheckpointConfig{Thresholds: []string{"120%", "70%"}}).ResolveThresholds(); !reflect.DeepEqual(got, []float64{0.70}) {
		t.Errorf("mixed thresholds = %v, want [0.7]", got)
	}
	// All invalid → fall back to defaults.
	if got := (CheckpointConfig{Thresholds: []string{"nope", "0%"}}).ResolveThresholds(); !reflect.DeepEqual(got, []float64{0.40, 0.60, 0.80}) {
		t.Errorf("all-invalid thresholds = %v, want defaults", got)
	}
}

func TestParseMaxContext(t *testing.T) {
	cases := []struct {
		in     string
		set    bool
		window int
		want   int
	}{
		{"", false, 200000, 0},
		{"300000", true, 200000, 300000},
		{"300K", true, 200000, 300000},
		{"1M", true, 200000, 1000000},
		{"1m", true, 200000, 1000000},
		{"1.5M", true, 200000, 1500000},
		{"50%", true, 200000, 100000},
		{"25%", true, 400000, 100000},
	}
	for _, c := range cases {
		mc, err := ParseMaxContext(c.in)
		if err != nil {
			t.Errorf("ParseMaxContext(%q) unexpected error: %v", c.in, err)
			continue
		}
		if mc.IsSet() != c.set {
			t.Errorf("ParseMaxContext(%q).IsSet() = %v, want %v", c.in, mc.IsSet(), c.set)
		}
		if got := mc.Resolve(c.window); got != c.want {
			t.Errorf("ParseMaxContext(%q).Resolve(%d) = %d, want %d", c.in, c.window, got, c.want)
		}
	}
}

func TestParseMaxContext_Invalid(t *testing.T) {
	for _, in := range []string{"0%", "150%", "-100%", "abc", "0", "-5", "12x"} {
		if _, err := ParseMaxContext(in); err == nil {
			t.Errorf("ParseMaxContext(%q) should error", in)
		}
	}
}

func TestLoadFileConfig_MemoryTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[memory]
enabled = false
reconcile_on_search = false
search_score_floor = 0.3
cc_index = true

[checkpoint]
thresholds = ["50%", "70%"]
reserved = 4096

[checkpoint.push_caps]
memory = 800
recall = 1200

[compaction]
max_context = "300K"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}

	mem := cfg.Memory.Resolve()
	if mem.Enabled || mem.ReconcileOnSearch || !mem.CCIndex || mem.SearchScoreFloor != 0.3 {
		t.Errorf("memory resolved = %+v", mem)
	}
	if got := cfg.Checkpoint.ResolveThresholds(); !reflect.DeepEqual(got, []float64{0.50, 0.70}) {
		t.Errorf("thresholds = %v, want [0.5 0.7]", got)
	}
	if !cfg.Checkpoint.Reserved.Set || !cfg.Checkpoint.Reserved.IsInt || cfg.Checkpoint.Reserved.Int != 4096 {
		t.Errorf("reserved = %+v, want int 4096", cfg.Checkpoint.Reserved)
	}
	if cfg.Checkpoint.PushCaps["memory"] != 800 || cfg.Checkpoint.PushCaps["recall"] != 1200 {
		t.Errorf("push_caps = %v", cfg.Checkpoint.PushCaps)
	}
	mc, err := cfg.Compaction.ResolveMaxContext()
	if err != nil {
		t.Fatalf("ResolveMaxContext: %v", err)
	}
	if got := mc.Resolve(200000); got != 300000 {
		t.Errorf("max_context resolve = %d, want 300000", got)
	}
}

func TestLoadFileConfig_ReservedString(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[checkpoint]\nreserved = \"10%\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if !cfg.Checkpoint.Reserved.Set || cfg.Checkpoint.Reserved.IsInt || cfg.Checkpoint.Reserved.Str != "10%" {
		t.Errorf("reserved = %+v, want string 10%%", cfg.Checkpoint.Reserved)
	}
}

// Absent tables → default-safe resolved settings.
func TestResolveMemorySettings_Defaults(t *testing.T) {
	ms := FileConfig{}.ResolveMemorySettings()
	if !ms.Memory.Enabled || !ms.Memory.ReconcileOnSearch || ms.Memory.SearchScoreFloor != 0.15 || ms.Memory.CCIndex {
		t.Errorf("default memory = %+v", ms.Memory)
	}
	if !reflect.DeepEqual(ms.CheckpointThresholds, []float64{0.40, 0.60, 0.80}) {
		t.Errorf("default thresholds = %v", ms.CheckpointThresholds)
	}
	if ms.MaxContext.IsSet() {
		t.Errorf("default max_context should be unset")
	}
	if ms.CheckpointReserved.Set {
		t.Errorf("default reserved should be unset")
	}
}

// An invalid max_context must not fail the overlay — it drops to unset.
func TestResolveMemorySettings_InvalidMaxContextIgnored(t *testing.T) {
	ms := FileConfig{Compaction: CompactionConfig{MaxContext: "garbage"}}.ResolveMemorySettings()
	if ms.MaxContext.IsSet() {
		t.Errorf("invalid max_context should resolve to unset, got set")
	}
}
