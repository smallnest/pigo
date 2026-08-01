// Command pigo is the CLI entry point for the pigo agent. It parses flags,
// overlays config.toml, and dispatches to one of the run modes — interactive
// REPL, headless print, session listing, or the internal sub-agent RPC server:
//
//	pigo                                          # interactive REPL (on a TTY)
//	pigo -p "read README and summarize"           # print mode: final text
//	pigo -p "..." --output-format stream-json      # line-delimited JSON events
//	pigo install <pkg> | list | uninstall | update # package management
//
// The provider is resolved from --model against the built-in OpenAI-compatible
// gateways (OpenRouter by default, Ollama for local models), with the API key
// taken from the environment. The process exit code reflects success (0) or
// failure (1), so the command composes cleanly in pipelines. All run-assembly,
// REPL, headless, and config logic lives under internal/cli/*; this file keeps
// only flag parsing (cliOptions), config overlay (applyFileConfig), and the
// dispatch seam that wires those subpackages together.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/cli/headless"
	"github.com/smallnest/pigo/internal/cli/pkgcmd"
	"github.com/smallnest/pigo/internal/cli/repl"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/cli/tui"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/dream"
	"github.com/smallnest/pigo/internal/selfupdate"
)

// Build metadata, injected at release time via -ldflags by goreleaser
// (see .goreleaser.yaml). They keep their default values for `go build`/
// `go run` from source, so `pigo --version` still works without a release build.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// cliOptions is the parsed command line, produced by main() and consumed by
// dispatch. Separating parse from dispatch makes the dispatch logic testable
// without touching the global flag set.
type cliOptions struct {
	prompt   string
	model    string
	baseURL  string
	apiKey   string
	protocol string
	// provider, when non-empty, selects a built-in provider by name from the
	// registry (mirrors pi's provider selection): provider.ResolveProvider then builds the
	// matching wire driver using the provider's default base URL, protocol, and
	// API-key env var, ignoring the model-id heuristics.
	provider     string
	outputFmt    string
	noTools      bool
	listSessions bool
	resumeID     string
	continueLast bool
	// approve grants the launch directory session-level trust up front (mirrors pi's
	// --approve/-a): the first-launch trust prompt is skipped and side-effect
	// tools (bash/write/edit) run without per-call confirmation for this run.
	approve bool
	// noSkills disables skill discovery (mirrors pi's --no-skills): skills under
	// ~/.agents/skills are not loaded as /skill-name commands.
	noSkills bool
	// systemPrompt, when non-empty, replaces the default coding-assistant base
	// instruction (mirrors pi's --system-prompt). The environment block and
	// AGENTS.md injection still apply on top of it.
	systemPrompt string
	// appendSystemPrompt holds --append-system-prompt values (mirrors pi, repeatable):
	// each is a path to a file whose contents are appended, or literal text when
	// it is not an existing file. Appended after the base prompt and AGENTS.md.
	appendSystemPrompt []string
	// configPrompts holds prompt-template paths from the config.toml `prompts`
	// array (settings tier); each is a file or directory loaded non-recursively.
	// Populated by applyFileConfig; empty when the config omits `prompts`.
	configPrompts []string
	// promptTemplates holds --prompt-template paths (CLI tier, repeatable); each
	// is a file or directory loaded non-recursively.
	promptTemplates []string
	// noPromptTemplates disables all prompt-template discovery (global, project,
	// settings, CLI); built-in slash commands are unaffected. Independent of
	// --no-skills.
	noPromptTemplates bool
	// subagentRPC selects the process-isolated sub-agent server mode (US-019,
	// #135): pigo reads JSON-RPC sub-agent run requests from stdin and writes
	// results to stdout. Internal, used by SubAgentTool's process mode.
	subagentRPC bool
	// dream, when set, runs the process-isolated memory-consolidation pass and
	// exits: pigo enumerates + consolidates the global/project memory scope, emits
	// a single-line Report JSON on stdout, and exits 0/1. Internal, spawned by the
	// dream scheduler (and usable headlessly by scripts). See internal/dream and
	// SPEC §4.1/§4.2.
	dream bool
	// dreamDryRun pairs with --dream: analyze and report without writing files or
	// updating dream state (the lock is still taken). SPEC §5.5 dry-run row.
	dreamDryRun bool
	// thinkingLevel, when non-empty, is the --thinking-level flag: the reasoning
	// effort for requests (off|minimal|low|medium|high|xhigh). It is the highest-
	// precedence layer in resolveThinkingLevel, overriding PIGO_THINKING_LEVEL, the
	// config files, and the built-in default (medium).
	thinkingLevel string
	// showVersion prints build metadata (version/commit/date, injected at release
	// time by goreleaser) and exits, without running the agent.
	showVersion bool
	// noTUI forces the line-based REPL instead of the full-screen TUI (US-001).
	// When set — or when stdout is not a TTY — the no-prompt path falls back to
	// repl.Run rather than launching tui.Run.
	noTUI bool
	// cwd, when non-empty, is the working directory pigo switches to before doing
	// anything else (matches the Claude Agent SDK's cwd option / git -C). Every
	// cwd-derived resolution — built-in tool file roots, project trust, hooks
	// project dir, .pigo/ project config, git info, the status-bar path — reads
	// os.Getwd(), so a single os.Chdir here makes all of them operate in the
	// given directory. This is what makes pigo usable as an SDK backend that can
	// be pointed at an arbitrary project root.
	cwd string
	// memory holds the resolved [memory]/[checkpoint]/[compaction] config tables
	// (defaults applied, string forms parsed). These have no CLI flags — the
	// config file is their only source — so applyFileConfig always populates this
	// (defaults when the tables are absent) for downstream memory/checkpoint/
	// compaction wiring to consume. See config.MemorySettings.
	memory config.MemorySettings
	// dreamCfg is the resolved [dream] configuration (enabled / interval /
	// recent-sessions), populated by applyFileConfig from the [dream] table with
	// defaults applied. The interactive REPL consumes it to decide the startup
	// background auto-consolidation (US-008). Like memory it has no CLI flags.
	dreamCfg dream.Config
}

func main() {
	// Package-management subcommands (pigo install|list|uninstall|update ...) are
	// positional and distinct from the flag-driven agent modes, so peel them off
	// before pflag parsing — the agent flags don't apply to them.
	if len(os.Args) > 1 && pkgcmd.Subcommands[os.Args[1]] {
		// `pigo update` routes by whether a positional package name follows it:
		// none — or flags-only, e.g. `pigo update --check` — is binary self-update
		// (#466: download the latest release and replace this binary); a package
		// name stays package-update (handled by pkgcmd). This is the US-003 dispatch
		// split, with updateIsSelfUpdate as the pure classifier so routing is
		// unit-testable (TestUpdateIsSelfUpdate).
		if os.Args[1] == "update" && updateIsSelfUpdate(os.Args[2:]) {
			os.Exit(selfupdate.Run(context.Background(), version, os.Stdout, os.Stderr))
		}
		os.Exit(pkgcmd.Run(os.Args[1], os.Args[2:], os.Stdout, os.Stderr))
	}

	var opts cliOptions
	flag.StringVarP(&opts.prompt, "print", "p", "", "prompt to run in headless print mode")
	flag.StringVarP(&opts.model, "model", "m", "openrouter/free", "model id to run against (a well-known model name like claude-opus-4-8 or deepseek-chat auto-selects its provider when --provider/--protocol/--base-url are unset)")
	flag.StringVarP(&opts.baseURL, "base-url", "u", "", "override provider base URL (e.g. local Ollama)")
	flag.StringVarP(&opts.apiKey, "api-key", "k", "", "API key for the resolved provider (overrides env/config; else <PROVIDER>_API_KEY)")
	flag.StringVarP(&opts.protocol, "protocol", "P", "", "force wire protocol for a custom endpoint: openai | anthropic (default: inferred from model id)")
	flag.StringVar(&opts.provider, "provider", "", "select a built-in provider by name (e.g. deepseek, minimax); uses its default base URL, protocol, and API-key env var (see --help provider list)")
	flag.StringVarP(&opts.outputFmt, "output-format", "o", "text", "output format: text | stream-json")
	flag.BoolVarP(&opts.noTools, "no-tools", "n", false, "disable the built-in file/shell tools")
	flag.BoolVarP(&opts.listSessions, "list-sessions", "l", false, "list stored interactive sessions and exit")
	flag.StringVarP(&opts.resumeID, "resume", "r", "", "resume the interactive session with this id")
	flag.BoolVarP(&opts.continueLast, "continue", "c", false, "resume the most recent interactive session")
	flag.BoolVarP(&opts.approve, "approve", "a", false, "trust the working directory for this run: skip the first-launch trust prompt and run side-effect tools without per-call confirmation")
	flag.BoolVar(&opts.noSkills, "no-skills", false, "disable skill discovery (do not load skills under ~/.agents/skills as /skill-name commands)")
	flag.BoolVar(&opts.noPromptTemplates, "no-prompt-templates", false, "disable prompt-template discovery (do not load ~/.pigo/{commands,prompts}, .pigo/prompts, config prompts, or --prompt-template); built-in slash commands are unaffected")
	flag.StringVar(&opts.systemPrompt, "system-prompt", "", "system prompt to use instead of the default coding-assistant prompt (mirrors pi --system-prompt)")
	flag.StringArrayVar(&opts.appendSystemPrompt, "append-system-prompt", nil, "append text or file contents to the system prompt; repeatable (mirrors pi --append-system-prompt)")
	flag.StringArrayVar(&opts.promptTemplates, "prompt-template", nil, "load a prompt template from a file or directory (non-recursive); repeatable (mirrors pi --prompt-template)")
	flag.StringVar(&opts.thinkingLevel, "thinking-level", "", "reasoning effort: off|minimal|low|medium|high|xhigh (overrides PIGO_THINKING_LEVEL and config; default medium)")
	flag.BoolVar(&opts.subagentRPC, "subagent-rpc", false, "internal: run as a process-isolated sub-agent JSON-RPC server over stdio (US-019)")
	flag.BoolVar(&opts.dream, "dream", false, "internal: run a memory-consolidation pass over the global/project memory scope, emit a Report JSON on stdout, and exit (SPEC §4.1)")
	flag.BoolVar(&opts.dreamDryRun, "dream-dry-run", false, "internal: with --dream, analyze and report without writing files or updating dream state (SPEC §5.5)")
	flag.BoolVar(&opts.noTUI, "no-tui", false, "use the line-based REPL instead of the full-screen TUI")
	flag.StringVarP(&opts.cwd, "cwd", "C", "", "run as if pigo was started in this directory (matches the Claude Agent SDK's cwd; like git -C): tool file access, trust, hooks, and project config all resolve against it")
	flag.BoolVarP(&opts.showVersion, "version", "v", false, "print version information and exit")
	// Extend the default pflag usage with a "Supported providers" block so
	// `--help` documents the values accepted by --provider (name → env var →
	// default base URL → protocol). The list is derived from the provider
	// registry, so it never drifts from the code.
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		cli.PrintProviderHelp(out)
	}
	flag.Parse()

	// --cwd switches the process working directory before anything cwd-derived is
	// resolved (tool roots, trust, hooks, project config, git info). Doing it here
	// — after parse, before config overlay and dispatch — means every downstream
	// os.Getwd() sees the requested directory, so pigo behaves as if it had been
	// launched there. A bad path is a usage error (exit 2) rather than a silent
	// fall-through to the original directory.
	if opts.cwd != "" {
		if err := os.Chdir(opts.cwd); err != nil {
			fmt.Fprintf(os.Stderr, "pigo: --cwd: %v\n", err)
			os.Exit(2)
		}
	}

	// Overlay ~/.config/pigo/config.toml: file values replace built-in defaults,
	// but any flag the user set on the command line still wins (CLI > file >
	// default). A malformed file warns but does not abort — defaults apply.
	if cfg, err := config.LoadFileConfig(config.FileConfigPath()); err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
	} else {
		applyFileConfig(&opts, cfg, flag.CommandLine.Changed)
	}

	// --version is a standalone action: print build metadata and exit.
	if opts.showVersion {
		fmt.Printf("pigo %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

	// A prompt may also be supplied as positional args.
	if opts.prompt == "" {
		opts.prompt = strings.TrimSpace(strings.Join(flag.Args(), " "))
	}

	os.Exit(dispatch(context.Background(), opts, os.Stdout, os.Stderr))
}

// applyFileConfig overlays config.toml values onto opts, but only for flags the
// user did not set on the command line (changed reports whether a flag name was
// explicitly passed). This yields the precedence: CLI flag > config file >
// default. Zero-valued config fields never override.
func applyFileConfig(opts *cliOptions, cfg config.FileConfig, changed func(string) bool) {
	if cfg.Model != "" && !changed("model") {
		opts.model = cfg.Model
	}
	if cfg.BaseURL != "" && !changed("base-url") {
		opts.baseURL = cfg.BaseURL
	}
	if cfg.APIKey != "" && !changed("api-key") {
		opts.apiKey = cfg.APIKey
	}
	if cfg.Protocol != "" && !changed("protocol") {
		opts.protocol = cfg.Protocol
	}
	if cfg.Provider != "" && !changed("provider") {
		opts.provider = cfg.Provider
	}
	if cfg.ThinkingLevel != "" && !changed("thinking-level") {
		opts.thinkingLevel = cfg.ThinkingLevel
	}
	if cfg.OutputFormat != "" && !changed("output-format") {
		opts.outputFmt = cfg.OutputFormat
	}
	if cfg.NoTools && !changed("no-tools") {
		opts.noTools = true
	}
	if cfg.NoSkills && !changed("no-skills") {
		opts.noSkills = true
	}
	if cfg.Approve && !changed("approve") {
		opts.approve = true
	}
	if cfg.SystemPrompt != "" && !changed("system-prompt") {
		opts.systemPrompt = cfg.SystemPrompt
	}
	// prompts (settings tier) are additive with --prompt-template (CLI tier,
	// wired in #339), so they are always passed through when present.
	if len(cfg.Prompts) > 0 {
		opts.configPrompts = cfg.Prompts
	}
	// The [memory]/[checkpoint]/[compaction] tables have no CLI flags, so they
	// are resolved (with defaults) and overlaid unconditionally — an absent set
	// of tables yields the default-safe MemorySettings.
	opts.memory = cfg.ResolveMemorySettings()
	// The [dream] table also has no CLI flags; normalize it (defaults applied when
	// the table is absent) so the interactive startup trigger has a resolved
	// Config. NewConfig treats a nil enabled as true, so dream is on by default.
	opts.dreamCfg = dream.NewConfig(cfg.Dream.Enabled, cfg.Dream.IntervalDays, cfg.Dream.RecentSessions)
}

// dispatch runs the resolved command and returns a process exit code, writing
// diagnostics to errOut. It is the run-assembly seam: every path (list, REPL,
// headless, subagent-rpc) is reached from here, so the CLI's behavior can be
// exercised without re-parsing flags. A returned code of 0 is success.
func dispatch(ctx context.Context, opts cliOptions, out, errOut io.Writer) int {
	// --subagent-rpc is a fully separate mode: speak the sub-agent JSON-RPC
	// protocol over stdio and exit. It is the subprocess end of process-isolated
	// sub-agents and shares nothing with the interactive/headless paths.
	if opts.subagentRPC {
		return headless.RunSubAgentRPC(ctx, os.Stdin, out, errOut)
	}

	// --dream is the subprocess consolidation mode (SPEC §4.1/§4.2): run one
	// memory-consolidation pass to completion, emit a single-line Report JSON on
	// stdout (progress/logs go to stderr), and exit 0 on success / 1 on failure.
	// It runs before any interactive/headless session assembly and honors -C/--cwd
	// for the project scope (applied above via os.Chdir). It shares nothing with
	// the REPL/headless paths.
	if opts.dream {
		return runDream(ctx, opts, out, errOut)
	}

	// --list-sessions is a standalone action: print and exit.
	if opts.listSessions {
		if err := headless.PrintSessions(out); err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		return 0
	}

	// --continue resolves to the most recently updated session id.
	resumeID := opts.resumeID
	if opts.continueLast && resumeID == "" {
		id, err := headless.MostRecentSessionID()
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		if id == "" {
			fmt.Fprintln(errOut, "pigo: no sessions to continue")
			return 1
		}
		resumeID = id
	}

	// No prompt + an interactive terminal → start the interactive UI. By default
	// this is the full-screen TUI (US-001); --no-tui (or a non-terminal stdout)
	// forces the line-based REPL (US-003). A --resume id also enters the
	// interactive UI to continue an existing session. No prompt with a
	// non-terminal stdout (pipe/CI) and no resume is an error, since there is
	// nothing to run and nothing to interact with.
	if opts.prompt == "" {
		isTTY := ui.StdoutIsTerminal()
		if resumeID == "" && !isTTY {
			fmt.Fprintln(errOut, "pigo: no prompt (use -p \"...\" or positional args)")
			return 2
		}
		env, err := run.SetupEnv(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.apiKey, opts.noTools, opts.noSkills, opts.systemPrompt, opts.appendSystemPrompt, opts.memory.Memory.Enabled)
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		if env.Plugins != nil {
			defer env.Plugins.Close()
		}
		if env.Memory != nil {
			defer env.Memory.Close()
		}
		thinking, err := run.ResolveThinkingLevel(opts.thinkingLevel)
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 2
		}
		if shouldUseTUI(opts, isTTY) {
			// Refresh the cached latest-release check off the hot path so the banner
			// can show an upgrade hint on this or the next launch without blocking
			// startup (US-004). No-ops for dev builds or a fresh cache.
			selfupdate.StartBackgroundCheck(version)
			if err := tui.Run(tui.Options{
				Model:             opts.model,
				ProviderName:      env.ProviderName,
				Provider:          env.Provider,
				BaseURL:           opts.baseURL,
				APIKey:            opts.apiKey,
				Protocol:          opts.protocol,
				Version:           version,
				ThinkingLevel:     thinking,
				Tools:             env.Tools,
				SysPrompt:         env.SysPrompt,
				ResumeID:          resumeID,
				Approve:           opts.approve,
				Skills:            env.Skills,
				Plugins:           env.Plugins,
				ConfigPrompts:     opts.configPrompts,
				CliPrompts:        opts.promptTemplates,
				NoPromptTemplates: opts.noPromptTemplates,
			}); err != nil {
				fmt.Fprintf(errOut, "pigo: %v\n", err)
				return 1
			}
			return 0
		}
		if err := repl.Run(repl.Options{
			Model:             opts.model,
			ProviderName:      env.ProviderName,
			Provider:          env.Provider,
			BaseURL:           opts.baseURL,
			APIKey:            opts.apiKey,
			Protocol:          opts.protocol,
			ThinkingLevel:     thinking,
			Tools:             env.Tools,
			SysPrompt:         env.SysPrompt,
			ResumeID:          resumeID,
			Approve:           opts.approve,
			Skills:            env.Skills,
			Plugins:           env.Plugins,
			ConfigPrompts:     opts.configPrompts,
			CliPrompts:        opts.promptTemplates,
			NoPromptTemplates: opts.noPromptTemplates,
			Dream:             opts.dreamCfg,
		}); err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		return 0
	}

	mode, err := headless.ParseOutputMode(opts.outputFmt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 2
	}

	env, err := run.SetupEnv(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.apiKey, opts.noTools, opts.noSkills, opts.systemPrompt, opts.appendSystemPrompt, opts.memory.Memory.Enabled)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	if env.Plugins != nil {
		defer env.Plugins.Close()
	}
	if env.Memory != nil {
		defer env.Memory.Close()
	}
	return headless.Run(ctx, headless.RunParams{
		Mode:          mode,
		Env:           env,
		Prompt:        opts.prompt,
		Model:         opts.model,
		APIKey:        opts.apiKey,
		ThinkingLevel: opts.thinkingLevel,
		ResumeID:      resumeID,
	}, out, errOut)
}

// runDream executes the subprocess memory-consolidation pass (SPEC §4.1/§4.2).
// It runs dream.Runner to completion, marshals the resulting Report as a single
// line of JSON on stdout (the parent/scheduler parses this), and returns the
// process exit code: 0 on success (including a "skipped" run when another dream
// holds the lock) or 1 on failure. Progress and diagnostics go to errOut. The
// project scope comes from the working directory, which -C/--cwd already applied
// via os.Chdir before dispatch, so an empty ProjectDir here resolves to cwd.
func runDream(ctx context.Context, opts cliOptions, out, errOut io.Writer) int {
	projectDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(errOut, "pigo: dream: %v\n", err)
		return 1
	}
	// The dream pass reuses the main-session model (SPEC Q3): resolve the same
	// model/provider/api-key tuple cmd/pigo already overlaid from flags+config,
	// and inject a real LLM-backed Consolidator so `pigo --dream` performs the
	// semantic merge/prune step (not just the deterministic dedup/path-clean).
	thinking, err := run.ResolveThinkingLevel(opts.thinkingLevel)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: dream: %v\n", err)
		return 1
	}
	cons, err := dream.NewLLMConsolidator(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.apiKey, thinking)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: dream: %v\n", err)
		return 1
	}
	r := &dream.Runner{Consolidator: cons}
	report, err := r.Run(ctx, dream.RunOptions{
		DryRun:     opts.dreamDryRun,
		ProjectDir: projectDir,
	})
	if err != nil {
		fmt.Fprintf(errOut, "pigo: dream: %v\n", err)
		return 1
	}
	// Single-line JSON on stdout is the stdout contract (SPEC §4.2). Encoder
	// writes a trailing newline, keeping the report one line.
	if err := json.NewEncoder(out).Encode(report); err != nil {
		fmt.Fprintf(errOut, "pigo: dream: encode report: %v\n", err)
		return 1
	}
	return 0
}

// shouldUseTUI is the pure entry-gating predicate for the no-prompt path
// (US-001, SPEC 4.2/5.2): the full-screen TUI is used only when stdout is a TTY
// and --no-tui was not set. --no-tui or a non-terminal stdout always forces the
// line-based REPL. Keeping the decision in a side-effect-free function lets the
// gating be unit-tested without a real terminal or spawning Bubble Tea (see
// TestDispatchTUIGating); dispatch handles the non-TTY/no-resume usage error
// before calling this, so it only decides TUI-vs-REPL for the interactive case.
func shouldUseTUI(opts cliOptions, isTTY bool) bool {
	return isTTY && !opts.noTUI
}

// updateIsSelfUpdate classifies the arguments that follow `pigo update` (US-003)
// to route between binary self-update and pkgmgr package-update. It returns true
// — self-update — when no positional package name is present: any argument that
// does not begin with '-' is treated as a package name and routes to
// package-update, while flags-only invocations (e.g. `pigo update --check`) stay
// on the self-update path. Keeping the decision side-effect-free lets the routing
// be unit-tested without spawning either update path (see TestUpdateIsSelfUpdate).
func updateIsSelfUpdate(rest []string) bool {
	for _, a := range rest {
		if !strings.HasPrefix(a, "-") {
			return false
		}
	}
	return true
}
