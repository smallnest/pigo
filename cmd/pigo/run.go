// This file holds the run-assembly seam (architecture deepening ②). main() used
// to build the provider, tool set, system prompt, and RunConfig separately in
// its REPL branch and its headless branch — the same assembly written twice, and
// a RunConfig literal duplicated between here and repl.go's streamRun. That setup
// now lives in one place: setupAgentEnv assembles the shared environment, and
// newRunConfig builds the loop configuration both drivers run. main() is left to
// parse flags, dispatch, and map the outcome to an exit code.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// agentEnv is the environment every run shares: the working directory, the tool
// set rooted at it, the resolved provider, and the system prompt. It is
// assembled once (setupAgentEnv) and consumed by whichever driver runs.
type agentEnv struct {
	cwd          string
	tools        []agentcore.AgentTool
	provider     provider.Provider
	providerName string
	sysPrompt    string

	// plugins holds any loaded external plugins so the caller can Close them
	// when the run ends. It is nil when no plugins were discovered.
	plugins *plugin.Manager
}

// setupAgentEnv resolves the provider for model/baseURL, builds the tool set
// rooted at the working directory, and constructs the system prompt — the setup
// the REPL and headless drivers both need. systemPrompt, when non-empty,
// replaces the default base instruction (对标 pi 的 --system-prompt);
// appendSystemPrompt entries are each resolved (a path to an existing file is
// read, otherwise the value is literal text) and layered onto the end of the
// prompt (对标 pi 的 --append-system-prompt). It returns an error rather than
// exiting so the caller owns exit-code mapping.
func setupAgentEnv(model, baseURL, protocol string, noTools bool, systemPrompt string, appendSystemPrompt []string) (agentEnv, error) {
	cwd, _ := os.Getwd()
	prov, providerName, err := resolveProvider(model, baseURL, protocol)
	if err != nil {
		return agentEnv{}, err
	}
	appends, err := resolveAppendInstructions(appendSystemPrompt)
	if err != nil {
		return agentEnv{}, err
	}
	sysPrompt, err := runtime.BuildSystemPrompt(runtime.PromptConfig{
		BaseInstruction:    systemPrompt,
		WorkingDir:         cwd,
		Root:               cwd,
		AppendInstructions: appends,
	})
	if err != nil {
		return agentEnv{}, err
	}
	tools := builtinTools(cwd, noTools)
	// Discover external plugins (US-016) and append their tools. Plugin loading
	// is fault-tolerant: a plugin that fails to start is logged and skipped, and
	// disabling tools (--no-tools) skips plugin discovery entirely.
	var mgr *plugin.Manager
	if !noTools {
		if m, err := plugin.Discover(pluginsDir(), os.Stderr, os.Stderr); err == nil {
			tools = append(tools, m.Tools()...)
			mgr = m
		} else {
			fmt.Fprintf(os.Stderr, "pigo: plugin discovery failed: %v\n", err)
		}
	}
	return agentEnv{
		cwd:          cwd,
		tools:        tools,
		provider:     prov,
		providerName: providerName,
		sysPrompt:    sysPrompt,
		plugins:      mgr,
	}, nil
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

// pluginsDir returns the directory external plugins are discovered from:
// $PIGO_HOME/plugins, or ~/.pigo/plugins by default. An empty string is returned
// when the home directory cannot be resolved and no override is set (Discover
// then treats it as "no plugins").
func pluginsDir() string {
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

// newRunConfig builds the loop configuration shared by every driver: the
// provider stream, the dynamic API-key resolver, and the tool registry. It is
// the single definition of "how a run is wired", so the REPL (streamRun) and the
// headless driver cannot drift apart.
func newRunConfig(model, providerName string, prov provider.Provider, creds *provider.CredentialStore, reg *agenttool.ToolRegistry) runtime.RunConfig {
	return runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:     model,
			Provider:  providerName,
			Stream:    provider.StreamFnFromProvider(prov),
			GetAPIKey: creds.GetAPIKey,
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg},
		},
	}
}

// cliOptions is the parsed command line, produced by main() and consumed by
// dispatch. Separating parse from dispatch makes the dispatch logic testable
// without touching the global flag set.
type cliOptions struct {
	prompt       string
	model        string
	baseURL      string
	apiKey       string
	protocol     string
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
	// subagentRPC selects the process-isolated sub-agent server mode (US-019,
	// #135): pigo reads JSON-RPC sub-agent run requests from stdin and writes
	// results to stdout. Internal, used by SubAgentTool's process mode.
	subagentRPC bool
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
		if resumeID == "" && !stdoutIsTerminal() {
			fmt.Fprintln(errOut, "pigo: no prompt (use -p \"...\" or positional args)")
			return 2
		}
		env, err := setupAgentEnv(opts.model, opts.baseURL, opts.protocol, opts.noTools, opts.systemPrompt, opts.appendSystemPrompt)
		if err != nil {
			fmt.Fprintf(errOut, "pigo: %v\n", err)
			return 1
		}
		if env.plugins != nil {
			defer env.plugins.Close()
		}
		if err := runInteractive(interactiveOptions{
			model:        opts.model,
			providerName: env.providerName,
			provider:     env.provider,
			baseURL:      opts.baseURL,
			apiKey:       opts.apiKey,
			protocol:     opts.protocol,
			tools:        env.tools,
			sysPrompt:    env.sysPrompt,
			resumeID:     resumeID,
			approve:      opts.approve,
			noSkills:     opts.noSkills,
			plugins:      env.plugins,
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

	env, err := setupAgentEnv(opts.model, opts.baseURL, opts.protocol, opts.noTools, opts.systemPrompt, opts.appendSystemPrompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	if env.plugins != nil {
		defer env.plugins.Close()
	}
	promptContent, err := buildUserContent(opts.prompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}

	// Back the headless run with a session so its id appears in the first
	// stream-json event and the run can be resumed with --resume/--continue,
	// matching the interactive REPL and pi/Claude Code. A resumed session seeds
	// its prior messages ahead of the new prompt.
	priorMsgs, hs, err := openHeadlessSession(resumeID, opts.model, env.providerName, env.sysPrompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	messages := append(priorMsgs, agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: promptContent})
	agentCtx := &agentcore.AgentContext{
		SystemPrompt: hs.header.SystemPrompt,
		Messages:     messages,
		Tools:        env.tools,
	}

	// Resolve the API key by provider name from the environment (never logged).
	// An explicit --api-key overrides env/config for the resolved provider.
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(env.providerName, opts.apiKey)
	runCfg := newRunConfig(opts.model, env.providerName, env.provider, creds, toolRegistry(env.tools))
	runCfg.SessionID = hs.header.ID
	cfg := runtime.HeadlessConfig{
		Mode: mode,
		Out:  out,
		Run:  runCfg,
	}
	// Deliver agent lifecycle events to any subscribed plugin (US-017, #133).
	// NewEventNotifier returns nil when no plugin subscribes, so the OnEvent hook
	// stays unset in the common no-plugin case.
	if n := plugin.NewEventNotifier(env.plugins, errOut); n != nil {
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
