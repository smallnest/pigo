// Package run holds the run-assembly layer (US-005, #362): the shared setup that
// both the interactive REPL and the headless driver need — resolving the
// provider, building the tool set rooted at the working directory, discovering
// skills and plugins, and constructing the loop RunConfig. Pulling it out of
// cmd/pigo lets the subpackages assemble a run through one exported API instead
// of duplicating the wiring.
package run

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/builtinskills"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/trust"
)

// Env is the environment every run shares: the working directory, the tool set
// rooted at it, the resolved provider, and the system prompt. It is assembled
// once (SetupEnv) and consumed by whichever driver runs.
type Env struct {
	Cwd          string
	Tools        []agentcore.AgentTool
	Provider     provider.Provider
	ProviderName string
	SysPrompt    string

	// Skills is the discovered skill set (loaded once here, empty under
	// --no-skills). It is threaded into the REPL so each skill is registered as a
	// /skill-name command, and the model-invocable subset is already injected into
	// SysPrompt.
	Skills []*runtime.Skill

	// Plugins holds any loaded external plugins so the caller can Close them when
	// the run ends. It is nil when no plugins were discovered.
	Plugins *plugin.Manager
}

// SetupEnv resolves the provider for model/baseURL, builds the tool set rooted
// at the working directory, and constructs the system prompt — the setup the
// REPL and headless drivers both need. systemPrompt, when non-empty, replaces
// the default base instruction (对标 pi 的 --system-prompt); appendSystemPrompt
// entries are each resolved (a path to an existing file is read, otherwise the
// value is literal text) and layered onto the end of the prompt (对标 pi 的
// --append-system-prompt). It returns an error rather than exiting so the caller
// owns exit-code mapping.
func SetupEnv(model, baseURL, protocol, providerName string, noTools, noSkills bool, systemPrompt string, appendSystemPrompt []string) (Env, error) {
	cwd, _ := os.Getwd()
	prov, resolvedName, err := provider.ResolveProvider(model, baseURL, protocol, providerName, os.Getenv)
	if err != nil {
		return Env{}, err
	}
	appends, err := resolveAppendInstructions(appendSystemPrompt)
	if err != nil {
		return Env{}, err
	}
	tools := BuiltinTools(cwd, noTools)
	// Wire the generic task tool (US-002, #454) unless tools are disabled. It
	// dispatches general-purpose sub-agents that reuse the resolved provider
	// stream/model. Each spawn gets a fresh child RunConfig whose registry is the
	// builtins with "task" removed (the nesting guard, so a child cannot fan out
	// again), and all task calls in a run share one semaphore capping concurrency.
	if !noTools {
		sem := runtime.NewSubagentSemaphore()
		childCreds := provider.NewCredentialStore(nil) // env-resolved, like the subagent RPC child
		factory := func() runtime.RunConfig {
			childTools := BuiltinToolsExcept(cwd, false, "task")
			return runtime.RunConfig{
				LoopConfig: runtime.LoopConfig{
					Model:     model,
					Provider:  resolvedName,
					Stream:    provider.StreamFnFromProvider(prov),
					GetAPIKey: childCreds.GetAPIKey,
				},
				Batch: agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: ToolRegistry(childTools)}},
			}
		}
		tools = append(tools, runtime.NewTaskTool(factory, sem))
	}
	// Discover external plugins (US-016) and append their tools. Plugin loading
	// is fault-tolerant: a plugin that fails to start is logged and skipped, and
	// disabling tools (--no-tools) skips plugin discovery entirely.
	var mgr *plugin.Manager
	if !noTools {
		if m, err := plugin.Discover(PluginsDir(), os.Stderr, os.Stderr); err == nil {
			tools = append(tools, m.Tools()...)
			mgr = m
		} else {
			fmt.Fprintf(os.Stderr, "pigo: plugin discovery failed: %v\n", err)
		}
	}
	// Load skills once (shared between prompt injection and /skill-name
	// registration). A partial parse error still yields the skills that DID load,
	// so one malformed file is a non-fatal warning rather than a hard failure.
	skills, err := LoadSkills(noSkills)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: skills: %v\n", err)
	}
	// The model can only load a skill's body when the read tool is present, so
	// advertise skills in the prompt only then (对标 pi 的 selectedTools check).
	sysPrompt, err := runtime.BuildSystemPrompt(runtime.PromptConfig{
		BaseInstruction:    systemPrompt,
		WorkingDir:         cwd,
		Root:               cwd,
		AppendInstructions: appends,
		Skills:             skills,
		ReadToolAvailable:  hasReadTool(tools),
	})
	if err != nil {
		return Env{}, err
	}
	return Env{
		Cwd:          cwd,
		Tools:        tools,
		Provider:     prov,
		ProviderName: resolvedName,
		SysPrompt:    sysPrompt,
		Skills:       skills,
		Plugins:      mgr,
	}, nil
}

// hasReadTool reports whether the read tool is present in the tool set. Skills
// are advertised in the system prompt only when it is, since the model needs the
// read tool to load a skill's body on demand.
func hasReadTool(tools []agentcore.AgentTool) bool {
	for _, t := range tools {
		if t.Name() == "read" {
			return true
		}
	}
	return false
}

// resolveAppendInstructions maps each --append-system-prompt value to the text
// to append. Following pi, a value that names an existing regular file is read
// and its contents are appended; any other value (a non-existent path, or a
// directory) is treated as literal text. Only a value that stats as a regular
// file but then fails to read (e.g. a permission error) is reported, so a
// genuinely broken file path is not silently appended verbatim.
func resolveAppendInstructions(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		info, statErr := os.Stat(v)
		if statErr == nil && !info.IsDir() {
			data, err := os.ReadFile(v)
			if err != nil {
				return nil, fmt.Errorf("read --append-system-prompt file %q: %w", v, err)
			}
			out = append(out, string(data))
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// BuiltinTools returns the default file/shell tool set rooted at cwd, or nil
// when tools are disabled. The todo tool is stateful: a single TodoStore is
// created here and held by the one TodoTool instance, so the task list persists
// across calls within a run (a later write replaces the plan).
func BuiltinTools(cwd string, disabled bool) []agentcore.AgentTool {
	if disabled {
		return nil
	}
	return []agentcore.AgentTool{
		&agenttool.ReadTool{Root: cwd, ExtraRoots: ReadableExtraRoots()},
		&agenttool.WriteTool{Root: cwd, ExtraRoots: ReadableExtraRoots()},
		&agenttool.EditTool{Root: cwd, ExtraRoots: ReadableExtraRoots()},
		&agenttool.GrepTool{Root: cwd},
		&agenttool.FindTool{Root: cwd},
		&agenttool.BashTool{Dir: cwd},
		&agenttool.TodoTool{Store: agenttool.NewTodoStore()},
		&agenttool.WebFetchTool{},
	}
}

// BuiltinToolsExcept returns the default builtin tool set (BuiltinTools) with
// any tool whose name matches one of the except names removed. It backs the
// nesting guard for the generic task tool: a child sub-agent's registry is built
// with "task" excluded so a child can never spawn further sub-agents, capping
// delegation depth at one. With no except names it is equivalent to BuiltinTools.
func BuiltinToolsExcept(cwd string, disabled bool, except ...string) []agentcore.AgentTool {
	all := BuiltinTools(cwd, disabled)
	if len(except) == 0 || len(all) == 0 {
		return all
	}
	skip := make(map[string]struct{}, len(except))
	for _, n := range except {
		skip[n] = struct{}{}
	}
	out := make([]agentcore.AgentTool, 0, len(all))
	for _, t := range all {
		if _, ok := skip[t.Name()]; ok {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ReadableExtraRoots returns trusted directories the file tools may reach beyond
// the workspace root. The skills directory is included so the model can load the
// absolute SKILL.md paths pigo advertises in the system prompt, and author or
// update skills there (they otherwise resolve outside the workspace and are
// rejected). An empty skills dir is dropped, so this stays a no-op when the home
// directory cannot be resolved.
func ReadableExtraRoots() []string {
	if dir := SkillsDir(); dir != "" {
		return []string{dir}
	}
	return nil
}

// ToolRegistry builds a registry from the given tools (skipping any that fail to
// register, e.g. a bad schema, which should not happen for built-ins).
func ToolRegistry(tools []agentcore.AgentTool) *agenttool.ToolRegistry {
	reg := agenttool.NewToolRegistry()
	for _, t := range tools {
		_ = reg.Register(t)
	}
	return reg
}

// TodoReminders builds the per-turn system-reminder registry for a tool set
// (US-002): it locates the stateful TodoTool and registers a TodoReminderProvider
// over its shared store, so the model is reminded of unfinished tasks each turn.
// Returns nil when no todo tool is present (e.g. --no-tools), leaving injection
// disabled.
func TodoReminders(tools []agentcore.AgentTool) *runtime.ReminderRegistry {
	for _, t := range tools {
		if tt, ok := t.(*agenttool.TodoTool); ok && tt.Store != nil {
			return runtime.NewReminderRegistry(&runtime.TodoReminderProvider{Store: tt.Store})
		}
	}
	return nil
}

// SkillsDir returns the directory skills are loaded from. It defaults to
// ~/.agents/skills, overridable via PIGO_SKILLS_DIR. An empty string is returned
// when the home directory cannot be resolved and no override is set.
func SkillsDir() string {
	if dir := os.Getenv("PIGO_SKILLS_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agents", "skills")
}

// LoadSkills discovers skills from SkillsDir() once, for both prompt injection
// and /skill-name registration. Under --no-skills it is a no-op. Built-in skills
// are bootstrapped into the skills dir first, then the directory is loaded.
func LoadSkills(noSkills bool) ([]*runtime.Skill, error) {
	if noSkills {
		return nil, nil
	}
	var blog io.Writer
	if os.Getenv("PIGO_DEBUG") != "" {
		blog = os.Stderr
	}
	builtinskills.Bootstrap(ConfigDir(), SkillsDir(), blog)
	dir := SkillsDir()
	if dir == "" {
		return nil, nil
	}
	return runtime.LoadSkillsDir(dir)
}

// PluginsDir returns the directory external plugins are discovered from:
// $PIGO_HOME/plugins, or ~/.pigo/plugins by default. An empty string is returned
// when the home directory cannot be resolved and no override is set (Discover
// then treats it as "no plugins").
func PluginsDir() string {
	dir := os.Getenv("PIGO_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".pigo")
	}
	return filepath.Join(dir, "plugins")
}

// ConfigDir returns the directory pigo reads its global config layer from:
// $PIGO_HOME, or ~/.pigo by default. An empty string is returned when the home
// directory cannot be resolved and no override is set (the caller then treats
// the global layer as absent).
func ConfigDir() string {
	dir := os.Getenv("PIGO_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".pigo")
	}
	return dir
}

// ResolveThinkingLevel resolves the effective reasoning-effort level through the
// layered config chain (US-023): default < global < project < env < CLI flag.
// The global layer is $PIGO_HOME/config.json (or ~/.pigo/config.json); the
// project layer is ./.pigo/config.json in the working directory. A malformed
// layer file or an invalid resolved value is a hard error, surfaced to the
// caller for exit-code mapping. cliLevel is the raw --thinking-level flag ("" =
// unset, so lower layers show through).
func ResolveThinkingLevel(cliLevel string) (agentcore.ThinkingLevel, error) {
	def := runtime.DefaultConfigLayer()
	layers := []*runtime.ConfigLayer{&def}

	if dir := ConfigDir(); dir != "" {
		global, err := runtime.LoadConfigLayer(filepath.Join(dir, "config.json"))
		if err != nil {
			return "", err
		}
		layers = append(layers, global)
	}
	project, err := runtime.LoadConfigLayer(filepath.Join(".pigo", "config.json"))
	if err != nil {
		return "", err
	}
	layers = append(layers, project)

	env := runtime.EnvConfigLayer(os.Getenv)
	layers = append(layers, &env)

	if v := strings.TrimSpace(cliLevel); v != "" {
		cli := runtime.ConfigLayer{ThinkingLevel: &v}
		layers = append(layers, &cli)
	}

	cfg, err := runtime.ResolveConfig(layers...)
	if err != nil {
		return "", err
	}
	return cfg.ThinkingLevel, nil
}

// ResolveHookSet resolves the effective hook set through the same layered config
// chain as ResolveThinkingLevel (default < global < project < env), with one
// difference required by FR-14: the project layer (./.pigo/config.json under
// cwd) is only merged when the directory is trusted. An untrusted directory
// therefore contributes no hooks, so a checked-out repo cannot run arbitrary
// commands until the user trusts it. A malformed layer file is a hard error,
// surfaced to the caller. The returned set is empty (len 0) when no layer
// defines hooks, which InstallHooks treats as "no hooks" (FR-18).
func ResolveHookSet(cwd string, trusted bool) (hooks.HookSet, error) {
	def := runtime.DefaultConfigLayer()
	layers := []*runtime.ConfigLayer{&def}

	if dir := ConfigDir(); dir != "" {
		global, err := runtime.LoadConfigLayer(filepath.Join(dir, "config.json"))
		if err != nil {
			return nil, err
		}
		layers = append(layers, global)
	}
	if trusted {
		project, err := runtime.LoadConfigLayer(filepath.Join(cwd, ".pigo", "config.json"))
		if err != nil {
			return nil, err
		}
		layers = append(layers, project)
	}
	env := runtime.EnvConfigLayer(os.Getenv)
	layers = append(layers, &env)

	cfg, err := runtime.ResolveConfig(layers...)
	if err != nil {
		return nil, err
	}
	return cfg.Hooks, nil
}

// Trusted reports whether cwd is a trusted directory per the shared trust store
// ($PIGO_HOME/trust.json). It is the trust gate for the non-interactive drivers
// (headless / TUI / sub-agent) that have no live trust.Manager to consult, so
// ResolveHookSet can honor FR-14 uniformly. A missing or unreadable store is
// treated as untrusted (fail closed): a directory only runs project-layer hooks
// after the user has explicitly trusted it.
func Trusted(cwd string) bool {
	m, err := trust.NewManager(trust.DefaultPath())
	if err != nil || m == nil {
		return false
	}
	return m.IsTrusted(cwd)
}

// NewConfig builds the loop configuration shared by every driver: the provider
// stream, the dynamic API-key resolver, and the tool registry. It is the single
// definition of "how a run is wired", so the REPL (streamRun) and the headless
// driver cannot drift apart.
func NewConfig(model, providerName string, thinking agentcore.ThinkingLevel, prov provider.Provider, creds *provider.CredentialStore, reg *agenttool.ToolRegistry, reminders *runtime.ReminderRegistry) runtime.RunConfig {
	return runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:         model,
			Provider:      providerName,
			ThinkingLevel: thinking,
			Stream:        provider.StreamFnFromProvider(prov),
			GetAPIKey:     creds.GetAPIKey,
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg},
		},
		Reminders: reminders,
	}
}
