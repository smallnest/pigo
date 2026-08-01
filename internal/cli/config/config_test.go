package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFileConfigPath_XDGOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgroot")
	got := FileConfigPath()
	want := filepath.Join("/tmp/xdgroot", "pigo", "config.toml")
	if got != want {
		t.Fatalf("FileConfigPath() = %q, want %q", got, want)
	}
}

func TestFileConfigPath_DefaultHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := FileConfigPath()
	want := filepath.Join(home, ".config", "pigo", "config.toml")
	if got != want {
		t.Fatalf("FileConfigPath() = %q, want %q", got, want)
	}
}

func TestLoadFileConfig_Missing(t *testing.T) {
	cfg, err := LoadFileConfig(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if !reflect.DeepEqual(cfg, FileConfig{}) {
		t.Fatalf("missing file should yield zero config, got %+v", cfg)
	}
}

func TestLoadFileConfig_EmptyPath(t *testing.T) {
	cfg, err := LoadFileConfig("")
	if err != nil {
		t.Fatalf("empty path should not error, got %v", err)
	}
	if !reflect.DeepEqual(cfg, FileConfig{}) {
		t.Fatalf("empty path should yield zero config, got %+v", cfg)
	}
}

func TestLoadFileConfig_Valid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
model = "claude-opus-4-8"
base_url = "https://example.com"
api_key = "sk-test"
protocol = "anthropic"
provider = "deepseek"
thinking_level = "high"
output_format = "stream-json"
no_tools = true
no_skills = true
approve = true
system_prompt = "be terse"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("valid file should parse, got %v", err)
	}
	want := FileConfig{
		Model:         "claude-opus-4-8",
		BaseURL:       "https://example.com",
		APIKey:        "sk-test",
		Protocol:      "anthropic",
		Provider:      "deepseek",
		ThinkingLevel: "high",
		OutputFormat:  "stream-json",
		NoTools:       true,
		NoSkills:      true,
		Approve:       true,
		SystemPrompt:  "be terse",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("parsed config = %+v, want %+v", cfg, want)
	}
}

func TestLoadFileConfig_Malformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("model = = ="), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFileConfig(path); err == nil {
		t.Fatal("malformed file should error")
	}
}

func TestLoadFileConfigPromptsArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "prompts = [\"./my-prompts\", \"/abs/x.md\"]\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if len(cfg.Prompts) != 2 || cfg.Prompts[0] != "./my-prompts" || cfg.Prompts[1] != "/abs/x.md" {
		t.Errorf("Prompts = %v, want [./my-prompts /abs/x.md]", cfg.Prompts)
	}
}

// TestGenericBaseURLEnvVar verifies the <PROVIDER>_BASE_URL name derivation,
// especially the hyphen→underscore conversion and uppercasing.
func TestGenericBaseURLEnvVar(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"deepseek", "DEEPSEEK_BASE_URL"},
		{"zai-coding-cn", "ZAI_CODING_CN_BASE_URL"},
		{"vercel-ai-gateway", "VERCEL_AI_GATEWAY_BASE_URL"},
		{"", ""},
	}
	for _, c := range cases {
		if got := GenericBaseURLEnvVar(c.name); got != c.want {
			t.Errorf("GenericBaseURLEnvVar(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLoadFileConfig_DreamTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[dream]
enabled = false
interval_days = 14
recent_sessions = 50
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if cfg.Dream.Enabled == nil || *cfg.Dream.Enabled {
		t.Errorf("Dream.Enabled = %v, want explicit false", cfg.Dream.Enabled)
	}
	if cfg.Dream.IntervalDays != 14 {
		t.Errorf("Dream.IntervalDays = %d, want 14", cfg.Dream.IntervalDays)
	}
	if cfg.Dream.RecentSessions != 50 {
		t.Errorf("Dream.RecentSessions = %d, want 50", cfg.Dream.RecentSessions)
	}
}

func TestLoadFileConfig_DreamTableAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("model = \"foo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	// Absent [dream] table: Enabled pointer nil (→ default true downstream),
	// ints zero (→ defaults downstream). Parsing must not error.
	if cfg.Dream.Enabled != nil || cfg.Dream.IntervalDays != 0 || cfg.Dream.RecentSessions != 0 {
		t.Errorf("absent dream table = %+v, want zero-value", cfg.Dream)
	}
}
