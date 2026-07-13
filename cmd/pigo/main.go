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
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
)

func main() {
	var opts cliOptions
	flag.StringVarP(&opts.prompt, "print", "p", "", "prompt to run in headless print mode")
	flag.StringVar(&opts.model, "model", "openrouter/free", "model id to run against")
	flag.StringVar(&opts.baseURL, "base-url", "", "override provider base URL (e.g. local Ollama)")
	flag.StringVar(&opts.outputFmt, "output-format", "text", "output format: text | stream-json")
	flag.BoolVar(&opts.noTools, "no-tools", false, "disable the built-in file/shell tools")
	flag.BoolVar(&opts.listSessions, "list-sessions", false, "list stored interactive sessions and exit")
	flag.StringVar(&opts.resumeID, "resume", "", "resume the interactive session with this id")
	flag.BoolVar(&opts.continueLast, "continue", false, "resume the most recent interactive session")
	flag.Parse()

	// A prompt may also be supplied as positional args.
	if opts.prompt == "" {
		opts.prompt = strings.TrimSpace(strings.Join(flag.Args(), " "))
	}

	os.Exit(dispatch(context.Background(), opts, os.Stdout, os.Stderr))
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
