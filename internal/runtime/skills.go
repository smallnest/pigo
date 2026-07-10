// This file implements declarative skills (US-028, #45): a skill is a markdown
// file with a YAML frontmatter block (对标 SKILL.md) that names a reusable
// capability. Loading a skill parses its metadata (name, description, optional
// tool allow-list and model) and its markdown body (the skill's system prompt),
// then materializes it as a sub-agent tool — so invoking a skill runs its body
// as the system prompt of a child agent loop, reusing the SubAgentTool
// abstraction rather than introducing a second execution path. Each skill's
// description is surfaced in the parent's capability list so the model can
// choose to delegate to it.
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"gopkg.in/yaml.v3"
)

// SkillFrontmatter is the YAML metadata block at the head of a skill file.
type SkillFrontmatter struct {
	// Name is the skill identifier; it becomes the spawnable tool name. When
	// omitted it defaults to the file's base name (without extension).
	Name string `yaml:"name"`
	// Description tells the model what the skill does and when to use it. It is
	// injected into the capability list, so it should be action-oriented.
	Description string `yaml:"description"`
	// AllowedTools optionally restricts the tools the skill's sub-agent may use,
	// by tool name. Empty means "inherit the provided tool set as-is".
	AllowedTools []string `yaml:"allowed-tools"`
	// Model optionally pins the skill to a specific model; empty inherits.
	Model string `yaml:"model"`
}

// Skill is a parsed skill file: its metadata plus the markdown body that serves
// as the sub-agent's system prompt.
type Skill struct {
	Frontmatter SkillFrontmatter
	// Body is the markdown after the frontmatter block — the skill's instructions,
	// used as the child agent's system prompt.
	Body string
	// Path is the source file, retained for diagnostics.
	Path string
}

// ParseSkill parses a skill's raw file content into a Skill. The file must open
// with a YAML frontmatter block delimited by lines containing only "---"; the
// remainder is the markdown body. A missing or malformed frontmatter block is
// an error, since name/description drive discovery.
func ParseSkill(path string, content []byte) (*Skill, error) {
	fm, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("skill %s: %w", path, err)
	}
	var meta SkillFrontmatter
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		return nil, fmt.Errorf("skill %s: parse frontmatter: %w", path, err)
	}
	if meta.Name == "" {
		base := filepath.Base(path)
		meta.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if meta.Description == "" {
		return nil, fmt.Errorf("skill %s: frontmatter missing required 'description'", path)
	}
	return &Skill{Frontmatter: meta, Body: strings.TrimSpace(string(body)), Path: path}, nil
}

// splitFrontmatter separates a leading "---"-delimited YAML block from the rest
// of the document. It returns the frontmatter bytes (without the fences) and
// the remaining body. It errors if the document does not open with a fence or
// the closing fence is missing.
func splitFrontmatter(content []byte) (frontmatter, body []byte, err error) {
	text := string(content)
	// Tolerate a UTF-8 BOM and leading blank lines before the opening fence.
	text = strings.TrimPrefix(text, "\ufeff")
	trimmed := strings.TrimLeft(text, "\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return nil, nil, fmt.Errorf("missing YAML frontmatter (file must start with '---')")
	}
	lines := strings.Split(trimmed, "\n")
	// lines[0] is the opening fence. Find the closing fence.
	var fmLines []string
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closeIdx = i
			break
		}
		fmLines = append(fmLines, lines[i])
	}
	if closeIdx == -1 {
		return nil, nil, fmt.Errorf("unterminated YAML frontmatter (missing closing '---')")
	}
	bodyLines := lines[closeIdx+1:]
	return []byte(strings.Join(fmLines, "\n")), []byte(strings.Join(bodyLines, "\n")), nil
}

// LoadSkillsDir loads every "*.md" skill file in dir (non-recursively) plus any
// "<name>/SKILL.md" nested layout (对标 the SKILL.md convention). It returns the
// parsed skills sorted by name. A missing directory yields no skills and no
// error (skills are optional). A malformed skill file is an error so problems
// surface at load time rather than silently disappearing.
func LoadSkillsDir(dir string) ([]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
	}
	var skills []*Skill
	for _, e := range entries {
		var path string
		switch {
		case e.IsDir():
			// Nested layout: <dir>/<name>/SKILL.md.
			candidate := filepath.Join(dir, e.Name(), "SKILL.md")
			if _, statErr := os.Stat(candidate); statErr != nil {
				continue
			}
			path = candidate
		case strings.EqualFold(filepath.Ext(e.Name()), ".md"):
			path = filepath.Join(dir, e.Name())
		default:
			continue
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read skill %s: %w", path, readErr)
		}
		skill, parseErr := ParseSkill(path, content)
		if parseErr != nil {
			return nil, parseErr
		}
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Frontmatter.Name < skills[j].Frontmatter.Name
	})
	return skills, nil
}

// SubAgentSpec turns a skill into a sub-agent spec: the skill body becomes the
// child's system prompt, the description is surfaced to the model, and the tool
// set is the provided tools filtered by AllowedTools (when set). newRunConfig
// builds each child run's configuration; it receives the resolved tool set so
// the caller can wire a matching registry.
func (s *Skill) SubAgentSpec(tools []agentcore.AgentTool, newRunConfig func(tools []agentcore.AgentTool) RunConfig) SubAgentSpec {
	resolved := filterToolsByName(tools, s.Frontmatter.AllowedTools)
	return SubAgentSpec{
		Name:         s.Frontmatter.Name,
		Description:  s.Frontmatter.Description,
		SystemPrompt: s.Body,
		Tools:        resolved,
		NewRunConfig: func() RunConfig { return newRunConfig(resolved) },
	}
}

// SkillTool materializes a skill as an invocable sub-agent tool.
func (s *Skill) SkillTool(tools []agentcore.AgentTool, newRunConfig func(tools []agentcore.AgentTool) RunConfig) *SubAgentTool {
	return NewSubAgentTool(s.SubAgentSpec(tools, newRunConfig))
}

// filterToolsByName keeps only tools whose Name is in allow. An empty allow
// list means "no restriction" and returns the input unchanged.
func filterToolsByName(tools []agentcore.AgentTool, allow []string) []agentcore.AgentTool {
	if len(allow) == 0 {
		return tools
	}
	set := make(map[string]bool, len(allow))
	for _, n := range allow {
		set[n] = true
	}
	out := make([]agentcore.AgentTool, 0, len(tools))
	for _, t := range tools {
		if set[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}
