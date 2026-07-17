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
// the REPL and headless drivers both need. It returns an error rather than
// exiting so the caller owns exit-code mapping.
func setupAgentEnv(model, baseURL, protocol string, noTools bool) (agentEnv, error) {
	cwd, _ := os.Getwd()
	prov, providerName, err := resolveProvider(model, baseURL, protocol)
	if err != nil {
		return agentEnv{}, err
	}
	sysPrompt, err := runtime.BuildSystemPrompt(runtime.PromptConfig{WorkingDir: cwd, Root: cwd})
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
}

// dispatch runs the resolved command and returns a process exit code, writing
// diagnostics to errOut. It is the run-assembly seam: every path (list, REPL,
// headless) is reached from here, so the CLI's behavior can be exercised without
// re-parsing flags. A returned code of 0 is success.
func dispatch(ctx context.Context, opts cliOptions, out, errOut io.Writer) int {
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
		env, err := setupAgentEnv(opts.model, opts.baseURL, opts.protocol, opts.noTools)
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

	env, err := setupAgentEnv(opts.model, opts.baseURL, opts.protocol, opts.noTools)
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
	agentCtx := &agentcore.AgentContext{
		SystemPrompt: env.sysPrompt,
		Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: promptContent},
		},
		Tools: env.tools,
	}

	// Resolve the API key by provider name from the environment (never logged).
	// An explicit --api-key overrides env/config for the resolved provider.
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(env.providerName, opts.apiKey)
	cfg := runtime.HeadlessConfig{
		Mode: mode,
		Out:  out,
		Run:  newRunConfig(opts.model, env.providerName, env.provider, creds, toolRegistry(env.tools)),
	}
	// Deliver agent lifecycle events to any subscribed plugin (US-017, #133).
	// NewEventNotifier returns nil when no plugin subscribes, so the OnEvent hook
	// stays unset in the common no-plugin case.
	if n := plugin.NewEventNotifier(env.plugins, errOut); n != nil {
		cfg.OnEvent = n.Handle
	}
	if err := runtime.RunHeadless(ctx, agentCtx, cfg); err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
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
