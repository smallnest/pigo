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
	"runtime"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agent"
)

func main() {
	var (
		prompt    string
		model     string
		baseURL   string
		outputFmt string
		noTools   bool
	)
	flag.StringVar(&prompt, "p", "", "prompt to run in headless print mode")
	flag.StringVar(&prompt, "print", "", "prompt to run in headless print mode")
	flag.StringVar(&model, "model", "openai/gpt-4o", "model id to run against")
	flag.StringVar(&baseURL, "base-url", "", "override provider base URL (e.g. local Ollama)")
	flag.StringVar(&outputFmt, "output-format", "text", "output format: text | stream-json")
	flag.BoolVar(&noTools, "no-tools", false, "disable the built-in file/shell tools")
	flag.Parse()

	// A prompt may also be supplied as positional args.
	if prompt == "" {
		prompt = strings.TrimSpace(strings.Join(flag.Args(), " "))
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "pigo: no prompt (use -p \"...\" or positional args)")
		os.Exit(2)
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
	agentCtx := &agent.AgentContext{
		SystemPrompt: systemPrompt(cwd),
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

// systemPrompt assembles the base instruction plus environment info (cwd, OS,
// time), a minimal version of pi's system-prompt assembly (fuller context
// assembly lands in US-021).
func systemPrompt(cwd string) string {
	var b strings.Builder
	b.WriteString("You are pigo, a helpful coding agent running headlessly. ")
	b.WriteString("Use the available tools to inspect files and answer the user's request concisely.\n\n")
	b.WriteString("Environment:\n")
	fmt.Fprintf(&b, "- Working directory: %s\n", cwd)
	fmt.Fprintf(&b, "- OS: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "- Date: %s\n", time.Now().Format("2006-01-02"))
	return b.String()
}
