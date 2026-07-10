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
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/smallnest/pigo/internal/agent"
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
	flag.StringVar(&prompt, "p", "", "prompt to run in headless print mode")
	flag.StringVar(&prompt, "print", "", "prompt to run in headless print mode")
	flag.StringVar(&model, "model", "openai/gpt-4o", "model id to run against")
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
		sysPrompt, err := agent.BuildSystemPrompt(agent.PromptConfig{WorkingDir: cwd, Root: cwd})
		if err != nil {
			fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
			os.Exit(1)
		}
		if err := runInteractive(interactiveOptions{
			model:        model,
			providerName: providerName,
			provider:     provider,
			tools:        tools,
			sysPrompt:    sysPrompt,
			resumeID:     resumeID,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
			os.Exit(1)
		}
		return
	}

	mode := agent.PrintMode
	switch outputFmt {
	case "text", "":
		mode = agent.PrintMode
	case "stream-json":
		mode = agent.StreamJSONMode
	default:
		fmt.Fprintf(os.Stderr, "pigo: unknown --output-format %q (want text|stream-json)\n", outputFmt)
		os.Exit(2)
	}

	provider, providerName, err := resolveProvider(model, baseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
		os.Exit(1)
	}

	cwd, _ := os.Getwd()
	tools := builtinTools(cwd, noTools)
	sysPrompt, err := agent.BuildSystemPrompt(agent.PromptConfig{WorkingDir: cwd, Root: cwd})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
		os.Exit(1)
	}
	agentCtx := &agent.AgentContext{
		SystemPrompt: sysPrompt,
		Messages: agent.MessageList{
			agent.UserMessage{RoleField: agent.RoleUser, Content: agent.ContentList{agent.NewTextContent(prompt)}},
		},
		Tools: tools,
	}

	// Resolve the API key by provider name from the environment (never logged).
	creds := agent.NewCredentialStore(nil)

	cfg := agent.HeadlessConfig{
		Mode: mode,
		Out:  os.Stdout,
		Run: agent.RunConfig{
			LoopConfig: agent.LoopConfig{
				Model:     model,
				Provider:  providerName,
				Stream:    agent.StreamFnFromProvider(provider),
				GetAPIKey: creds.GetAPIKey,
			},
			Batch: agent.BatchConfig{
				ToolExecutorConfig: agent.ToolExecutorConfig{Registry: toolRegistry(tools)},
			},
		},
	}

	if err := agent.RunHeadless(context.Background(), agentCtx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "pigo: %v\n", err)
		os.Exit(1)
	}
}

// resolveProvider maps a model id to a built-in provider. A model id prefixed
// with "ollama/" (or an explicit local base URL) uses the local Ollama gateway;
// everything else defaults to OpenRouter, the reference OpenAI-compatible layer.
func resolveProvider(model, baseURL string) (agent.Provider, string, error) {
	models := []agent.Model{{ID: model}}
	if strings.HasPrefix(model, "ollama/") || strings.Contains(baseURL, "11434") {
		id := strings.TrimPrefix(model, "ollama/")
		return agent.NewOllamaProvider(baseURL, []agent.Model{{Provider: "ollama", ID: id}}), "ollama", nil
	}
	for i := range models {
		models[i].Provider = "openrouter"
	}
	return agent.NewOpenRouterProvider(baseURL, models), "openrouter", nil
}

// builtinTools returns the default file/shell tool set rooted at cwd, or nil
// when tools are disabled.
func builtinTools(cwd string, disabled bool) []agent.AgentTool {
	if disabled {
		return nil
	}
	return []agent.AgentTool{
		&agent.ReadTool{Root: cwd},
		&agent.WriteTool{Root: cwd},
		&agent.EditTool{Root: cwd},
		&agent.GrepTool{Root: cwd},
		&agent.FindTool{Root: cwd},
		&agent.BashTool{Dir: cwd},
	}
}

// toolRegistry builds a registry from the given tools (skipping any that fail
// to register, e.g. a bad schema, which should not happen for built-ins).
func toolRegistry(tools []agent.AgentTool) *agent.ToolRegistry {
	reg := agent.NewToolRegistry()
	for _, t := range tools {
		_ = reg.Register(t)
	}
	return reg
}
