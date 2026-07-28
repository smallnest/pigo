// This file holds the run dispatch seam. main() parses flags and calls dispatch,
// which resolves the command (list / REPL / headless / subagent-rpc) and maps
// the outcome to an exit code. The shared run-assembly setup (provider, tools,
// system prompt, RunConfig) moved to internal/cli/run (US-005, #362); dispatch
// wires those pieces together for the driver each path needs.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
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
	// registry (对标 pi 的 provider selection): provider.ResolveProvider then builds the
	// matching wire driver using the provider's default base URL, protocol, and
	// API-key env var, ignoring the model-id heuristics.
	provider     string
	outputFmt    string
	noTools      bool
	listSessions bool
	resumeID     string
	continueLast bool
	// approve grants the launch directory session-level trust up front (对标 pi
	// 的 --approve/-a): the first-launch trust prompt is skipped and side-effect
	// tools (bash/write/edit) run without per-call confirmation for this run.
	approve bool
	// noSkills disables skill discovery (对标 pi 的 --no-skills): skills under
	// ~/.agents/skills are not loaded as /skill-name commands.
	noSkills bool
	// systemPrompt, when non-empty, replaces the default coding-assistant base
	// instruction (对标 pi 的 --system-prompt). The environment block and
	// AGENTS.md injection still apply on top of it.
	systemPrompt string
	// appendSystemPrompt holds --append-system-prompt values (对标 pi, repeatable):
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
	// thinkingLevel, when non-empty, is the --thinking-level flag: the reasoning
	// effort for requests (off|minimal|low|medium|high|xhigh). It is the highest-
	// precedence layer in resolveThinkingLevel, overriding PIGO_THINKING_LEVEL, the
	// config files, and the built-in default (medium).
	thinkingLevel string
	// showVersion prints build metadata (version/commit/date, injected at release
	// time by goreleaser) and exits, without running the agent.
	showVersion bool
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
		return runSubAgentRPC(ctx, os.Stdin, out, errOut)
	}

	// --list-sessions is a standalone action: print and exit.
	if opts.listSessions {
		if err := printSessions(out); err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		return 0
	}

	// --continue resolves to the most recently updated session id.
	resumeID := opts.resumeID
	if opts.continueLast && resumeID == "" {
		id, err := mostRecentSessionID()
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

	// No prompt + an interactive terminal → start the line-based REPL (US-003). A
	// --resume id also enters the REPL to continue an existing session. No prompt
	// with a non-terminal stdout (pipe/CI) and no resume is an error, since there
	// is nothing to run and nothing to interact with.
	if opts.prompt == "" {
		if resumeID == "" && !ui.StdoutIsTerminal() {
			fmt.Fprintln(errOut, "pigo: no prompt (use -p \"...\" or positional args)")
			return 2
		}
		env, err := run.SetupEnv(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.noTools, opts.noSkills, opts.systemPrompt, opts.appendSystemPrompt)
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		if env.Plugins != nil {
			defer env.Plugins.Close()
		}
		thinking, err := run.ResolveThinkingLevel(opts.thinkingLevel)
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 2
		}
		if err := runInteractive(interactiveOptions{
			model:             opts.model,
			providerName:      env.ProviderName,
			provider:          env.Provider,
			baseURL:           opts.baseURL,
			apiKey:            opts.apiKey,
			protocol:          opts.protocol,
			thinkingLevel:     thinking,
			tools:             env.Tools,
			sysPrompt:         env.SysPrompt,
			resumeID:          resumeID,
			approve:           opts.approve,
			skills:            env.Skills,
			plugins:           env.Plugins,
			configPrompts:     opts.configPrompts,
			cliPrompts:        opts.promptTemplates,
			noPromptTemplates: opts.noPromptTemplates,
		}); err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		return 0
	}

	mode, err := parseOutputMode(opts.outputFmt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 2
	}

	env, err := run.SetupEnv(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.noTools, opts.noSkills, opts.systemPrompt, opts.appendSystemPrompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	if env.Plugins != nil {
		defer env.Plugins.Close()
	}
	// Best-effort plugin slash-command support in headless mode: if the prompt is
	// a "/cmd ..." naming a plugin command, invoke it, print its notifications to
	// errOut, and use the returned prompt for this run (appending the raw args if
	// the command produced no prompt). Headless has no turn injection, so
	// appending the returned prompt is the accepted behavior. A non-plugin prompt
	// or unknown command is left untouched.
	headlessPrompt := resolveHeadlessPluginCommand(opts.prompt, env.Plugins, errOut)
	promptContent, err := ui.BuildUserContent(headlessPrompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}

	// Back the headless run with a session so its id appears in the first
	// stream-json event and the run can be resumed with --resume/--continue,
	// matching the interactive REPL and pi/Claude Code. A resumed session seeds
	// its prior messages ahead of the new prompt.
	priorMsgs, hs, err := openHeadlessSession(resumeID, opts.model, env.ProviderName, env.SysPrompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	messages := append(priorMsgs, agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: promptContent})
	agentCtx := &agentcore.AgentContext{
		SystemPrompt: hs.header.SystemPrompt,
		Messages:     messages,
		Tools:        env.Tools,
	}

	// Resolve the effective reasoning-effort level through the layered config
	// chain (default < global < project < env < --thinking-level flag).
	thinking, err := run.ResolveThinkingLevel(opts.thinkingLevel)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 2
	}

	// Resolve the API key by provider name from the environment (never logged).
	// An explicit --api-key overrides env/config for the resolved provider.
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(env.ProviderName, opts.apiKey)
	runCfg := run.NewConfig(opts.model, env.ProviderName, thinking, env.Provider, creds, run.ToolRegistry(env.Tools), run.TodoReminders(env.Tools))
	runCfg.SessionID = hs.header.ID
	cfg := runtime.HeadlessConfig{
		Mode: mode,
		Out:  out,
		Run:  runCfg,
	}
	// Deliver agent lifecycle events to any subscribed plugin (US-017, #133).
	// NewEventNotifier returns nil when no plugin subscribes, so the OnEvent hook
	// stays unset in the common no-plugin case.
	if n := plugin.NewEventNotifier(env.Plugins, errOut); n != nil {
		cfg.OnEvent = n.Handle
	}
	runErr := runtime.RunHeadless(ctx, agentCtx, cfg)
	// Persist the run's messages regardless of run outcome so a partial run is
	// still resumable; a persistence failure is reported but does not mask a run
	// error.
	if perr := hs.persist(agentCtx); perr != nil {
		fmt.Fprintf(errOut, "pigo: warning: could not persist session %s: %v\n", hs.header.ID, perr)
	}
	if runErr != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", runErr)
		return 1
	}
	return 0
}

// resolveHeadlessPluginCommand gives the headless / print path best-effort
// support for plugin slash commands. When prompt is a "/cmd ..." naming a
// plugin command (from mgr.Commands()), it invokes the command, prints each
// returned notification to notifyOut, and returns the command's returned Prompt
// as the run's prompt. If the command returns no prompt, the raw argument text
// is used instead (so a bare "/cmd" with only notifications still runs
// something sensible rather than an empty prompt). Any other input — a
// non-command, an unknown command, or a call error — leaves prompt unchanged so
// the normal headless run proceeds. mgr may be nil (no plugins).
//
// Headless has no turn-injection loop, so "inject the returned prompt" degrades
// to "use the returned prompt for this run", which the acceptance criteria
// permit.
func resolveHeadlessPluginCommand(prompt string, mgr *plugin.Manager, notifyOut io.Writer) string {
	if mgr == nil || !strings.HasPrefix(strings.TrimLeft(prompt, " \t"), "/") {
		return prompt
	}
	trimmed := strings.TrimLeft(prompt, " \t")[1:]
	name := trimmed
	args := ""
	if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
		name = trimmed[:i]
		args = strings.TrimSpace(trimmed[i+1:])
	}
	for _, pc := range mgr.Commands() {
		if pc.Spec.Name != name {
			continue
		}
		// Encode the raw arg text as a JSON string (never null), matching the
		// host's CommandCallParams.Args contract.
		raw, _ := json.Marshal(args)
		res, err := pc.Plugin.CallCommand(context.Background(), name, json.RawMessage(raw))
		if err != nil {
			fmt.Fprintf(notifyOut, "pigo: plugin command %q failed: %v\n", name, err)
			return prompt
		}
		for _, n := range res.Notifications {
			if n.Type != "" {
				fmt.Fprintf(notifyOut, "[%s] %s\n", n.Type, n.Message)
			} else {
				fmt.Fprintln(notifyOut, n.Message)
			}
		}
		if res.Prompt != "" {
			return res.Prompt
		}
		return args
	}
	return prompt
}

// parseOutputMode maps the --output-format flag onto a HeadlessMode, erroring on
// an unknown value.
func parseOutputMode(outputFmt string) (runtime.HeadlessMode, error) {
	switch outputFmt {
	case "text", "":
		return runtime.PrintMode, nil
	case "stream-json":
		return runtime.StreamJSONMode, nil
	default:
		return 0, fmt.Errorf("unknown --output-format %q (want text|stream-json)", outputFmt)
	}
}
