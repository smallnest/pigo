// This file holds the run dispatch seam. main() parses flags and calls dispatch,
// which resolves the command (list / REPL / headless / subagent-rpc) and maps
// the outcome to an exit code. The shared run-assembly setup (provider, tools,
// system prompt, RunConfig) moved to internal/cli/run (US-005, #362); dispatch
// wires those pieces together for the driver each path needs.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/smallnest/pigo/internal/cli/headless"
	"github.com/smallnest/pigo/internal/cli/repl"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/cli/ui"
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
		return headless.RunSubAgentRPC(ctx, os.Stdin, out, errOut)
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

	env, err := run.SetupEnv(opts.model, opts.baseURL, opts.protocol, opts.provider, opts.noTools, opts.noSkills, opts.systemPrompt, opts.appendSystemPrompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	if env.Plugins != nil {
		defer env.Plugins.Close()
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
