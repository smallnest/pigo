// Command pigo is the headless / stdio CLI entry point for the pigo agent
// (US-020). It runs the agent loop over a single prompt for scripting and CI:
//
//	pigo -p "read README and summarize"          # print mode: final text
//	pigo -p "..." --output-format stream-json     # line-delimited JSON events
//
// The provider is resolved from --model against the built-in OpenAI-compatible
// gateways (OpenRouter by default, Ollama for local models), with the API key
// taken from the environment. The process exit code reflects success (0) or
// failure (1), so the command composes cleanly in pipelines.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

func main() {
	var (
		prompt       string
		model        string
		baseURL      string
		outputFmt    string
		noTools      bool
		listSessions bool
		resumeID     string
		continueLast bool
	)
	flag.StringVarP(&prompt, "print", "p", "", "prompt to run in headless print mode")
	flag.StringVar(&model, "model", "openrouter/free", "model id to run against")
	flag.StringVar(&baseURL, "base-url", "", "override provider base URL (e.g. local Ollama)")
	flag.StringVar(&outputFmt, "output-format", "text", "output format: text | stream-json")
	flag.BoolVar(&noTools, "no-tools", false, "disable the built-in file/shell tools")
	flag.BoolVar(&listSessions, "list-sessions", false, "list stored interactive sessions and exit")
	flag.StringVar(&resumeID, "resume", "", "resume the interactive session with this id")
	flag.BoolVar(&continueLast, "continue", false, "resume the most recent interactive session")
	flag.Parse()

	// --list-sessions is a standalone action: print and exit.
	if listSessions {
		if err := printSessions(); err != nil {
			fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --continue resolves to the most recently updated session id.
	if continueLast && resumeID == "" {
		id, err := mostRecentSessionID()
		if err != nil {
			fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
			os.Exit(1)
		}
		if id == "" {
			fmt.Fprintln(os.Stderr, "pigo: no sessions to continue")
			os.Exit(1)
		}
		resumeID = id
	}

	// A prompt may also be supplied as positional args.
	if prompt == "" {
		prompt = strings.TrimSpace(strings.Join(flag.Args(), " "))
	}

	// No prompt + an interactive terminal → start the TUI (US-022). A --resume id
	// also enters the TUI to continue an existing session. No prompt with a
	// non-terminal stdout (pipe/CI) and no resume is an error, since there is
	// nothing to run and nothing to interact with.
	if prompt == "" {
		if resumeID == "" && !stdoutIsTerminal() {
			fmt.Fprintln(os.Stderr, "pigo: no prompt (use -p \"...\" or positional args)")
			os.Exit(2)
		}
		cwd, _ := os.Getwd()
		tools := builtinTools(cwd, noTools)
		provider, providerName, err := resolveProvider(model, baseURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
			os.Exit(1)
		}
		sysPrompt, err := runtime.BuildSystemPrompt(runtime.PromptConfig{WorkingDir: cwd, Root: cwd})
		if err != nil {
			fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
			os.Exit(1)
		}
		if err := runInteractive(interactiveOptions{
			model:        model,
			providerName: providerName,
			provider:     provider,
			baseURL:      baseURL,
			tools:        tools,
			sysPrompt:    sysPrompt,
			resumeID:     resumeID,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
			os.Exit(1)
		}
		return
	}

	mode := runtime.PrintMode
	switch outputFmt {
	case "text", "":
		mode = runtime.PrintMode
	case "stream-json":
		mode = runtime.StreamJSONMode
	default:
		fmt.Fprintf(os.Stderr, "pigo: unknown --output-format %q (want text|stream-json)\n", outputFmt)
		os.Exit(2)
	}

	prov, providerName, err := resolveProvider(model, baseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
		os.Exit(1)
	}

	cwd, _ := os.Getwd()
	tools := builtinTools(cwd, noTools)
	sysPrompt, err := runtime.BuildSystemPrompt(runtime.PromptConfig{WorkingDir: cwd, Root: cwd})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
		os.Exit(1)
	}
	agentCtx := &agentcore.AgentContext{
		SystemPrompt: sysPrompt,
		Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(prompt)}},
		},
		Tools: tools,
	}

	// Resolve the API key by provider name from the environment (never logged).
	creds := provider.NewCredentialStore(nil)

	cfg := runtime.HeadlessConfig{
		Mode: mode,
		Out:  os.Stdout,
		Run: runtime.RunConfig{
			LoopConfig: runtime.LoopConfig{
				Model:     model,
				Provider:  providerName,
				Stream:    provider.StreamFnFromProvider(prov),
				GetAPIKey: creds.GetAPIKey,
			},
			Batch: agenttool.BatchConfig{
				ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: toolRegistry(tools)},
			},
		},
	}

	if err := runtime.RunHeadless(context.Background(), agentCtx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
		os.Exit(1)
	}
}

// resolveProvider maps a model id to a built-in provider. Resolution order:
//
//  1. If the id is in the preset catalog, use its declared provider (this is how
//     OpenRouter/NVIDIA/Ollama presets pick the right gateway).
//  2. An "ollama/" prefix (or a base URL on the Ollama port) → local Ollama.
//  3. An "nvidia/" prefix → NVIDIA NIM (strips the prefix for the wire id).
//  4. Everything else → OpenRouter, the reference OpenAI-compatible gateway.
func resolveProvider(model, baseURL string) (provider.Provider, string, error) {
	// 1. Preset catalog wins: a curated id knows its own provider.
	if p, ok := provider.LookupPreset(model); ok {
		switch p.Provider {
		case "nvidia":
			return provider.NewNvidiaProvider(baseURL, []provider.Model{{Provider: "nvidia", ID: model}}), "nvidia", nil
		case "ollama":
			id := strings.TrimPrefix(model, "ollama/")
			return provider.NewOllamaProvider(baseURL, []provider.Model{{Provider: "ollama", ID: id}}), "ollama", nil
		default: // openrouter and any OpenAI-compatible upstream
			return provider.NewOpenRouterProvider(baseURL, []provider.Model{{Provider: "openrouter", ID: model}}), "openrouter", nil
		}
	}

	// 2. Local Ollama by prefix or port.
	if strings.HasPrefix(model, "ollama/") || strings.Contains(baseURL, "11434") {
		id := strings.TrimPrefix(model, "ollama/")
		return provider.NewOllamaProvider(baseURL, []provider.Model{{Provider: "ollama", ID: id}}), "ollama", nil
	}
	// 3. NVIDIA NIM by prefix.
	if strings.HasPrefix(model, "nvidia/") {
		id := strings.TrimPrefix(model, "nvidia/")
		return provider.NewNvidiaProvider(baseURL, []provider.Model{{Provider: "nvidia", ID: id}}), "nvidia", nil
	}
	// 4. Default: OpenRouter.
	return provider.NewOpenRouterProvider(baseURL, []provider.Model{{Provider: "openrouter", ID: model}}), "openrouter", nil
}

// builtinTools returns the default file/shell tool set rooted at cwd, or nil
// when tools are disabled.
func builtinTools(cwd string, disabled bool) []agentcore.AgentTool {
	if disabled {
		return nil
	}
	return []agentcore.AgentTool{
		&agenttool.ReadTool{Root: cwd},
		&agenttool.WriteTool{Root: cwd},
		&agenttool.EditTool{Root: cwd},
		&agenttool.GrepTool{Root: cwd},
		&agenttool.FindTool{Root: cwd},
		&agenttool.BashTool{Dir: cwd},
	}
}

// toolRegistry builds a registry from the given tools (skipping any that fail
// to register, e.g. a bad schema, which should not happen for built-ins).
func toolRegistry(tools []agentcore.AgentTool) *agenttool.ToolRegistry {
	reg := agenttool.NewToolRegistry()
	for _, t := range tools {
		_ = reg.Register(t)
	}
	return reg
}
