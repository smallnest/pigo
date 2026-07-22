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
	"io"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
)

// Build metadata, injected at release time via -ldflags by goreleaser
// (see .goreleaser.yaml). They keep their default values for `go build`/
// `go run` from source, so `pigo --version` still works without a release build.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Package-management subcommands (pigo install|list|uninstall|update ...) are
	// positional and distinct from the flag-driven agent modes, so peel them off
	// before pflag parsing — the agent flags don't apply to them.
	if len(os.Args) > 1 && packageSubcommands[os.Args[1]] {
		os.Exit(runPackageCommand(os.Args[1], os.Args[2:], os.Stdout, os.Stderr))
	}

	var opts cliOptions
	flag.StringVarP(&opts.prompt, "print", "p", "", "prompt to run in headless print mode")
	flag.StringVarP(&opts.model, "model", "m", "openrouter/free", "model id to run against")
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
	flag.StringVar(&opts.systemPrompt, "system-prompt", "", "system prompt to use instead of the default coding-assistant prompt (对标 pi --system-prompt)")
	flag.StringArrayVar(&opts.appendSystemPrompt, "append-system-prompt", nil, "append text or file contents to the system prompt; repeatable (对标 pi --append-system-prompt)")
	flag.BoolVar(&opts.subagentRPC, "subagent-rpc", false, "internal: run as a process-isolated sub-agent JSON-RPC server over stdio (US-019)")
	flag.BoolVarP(&opts.showVersion, "version", "v", false, "print version information and exit")
	// Extend the default pflag usage with a "Supported providers" block so
	// `--help` documents the values accepted by --provider (name → env var →
	// default base URL → protocol). The list is derived from the provider
	// registry, so it never drifts from the code.
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		printProviderHelp(out)
	}
	flag.Parse()

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

// printProviderHelp writes the "Supported providers" block appended to
// `--help` output. It enumerates the built-in provider registry so the list of
// values accepted by --provider (and their env vars / default base URLs /
// protocols) stays in sync with the code rather than being hand-maintained.
func printProviderHelp(w io.Writer) {
	fmt.Fprintf(w, "\nSupported --provider names (name: ENV_VARS -> default base URL [protocol]):\n")
	for _, spec := range provider.ProviderSpecs() {
		base := spec.DefaultBaseURL
		if strings.TrimSpace(base) == "" {
			base = "(composed from env)"
		}
		fmt.Fprintf(w, "  %s: %s -> %s [%s]\n",
			spec.Name, strings.Join(spec.EnvVars, ", "), base, spec.Protocol)
	}
	fmt.Fprintf(w, "\nBase URL override precedence: --base-url > <provider>-specific *_BASE_URL env > generic <PROVIDER>_BASE_URL env > registry default.\n")
	fmt.Fprintf(w, "API key env fallback: any provider also accepts the generic <PROVIDER>_API_KEY convention.\n")
}

// resolveProvider maps a model id to a built-in provider. An explicit
// --provider name wins over every other rule: it selects a built-in provider
// from the registry and constructs the matching wire driver (see
// resolveNamedProvider). When provider is empty, protocol and model-id
// heuristics apply as before.
//
// When protocol is a non-empty explicit selection ("openai" or "anthropic") it
// wins over the model-id heuristics: the provider is built directly for that
// wire format against baseURL, which is how a user points pigo at a self-hosted
// or third-party endpoint and says which protocol it speaks. An "anthropic"
// selection with no baseURL targets the public Anthropic API.
//
// When protocol is empty, resolution falls back to model-id heuristics:
//
//  1. If the id is in the preset catalog, use its declared provider (this is how
//     OpenRouter/NVIDIA/Ollama presets pick the right gateway).
//  2. An "ollama/" prefix (or a base URL on the Ollama port) → local Ollama.
//  3. An "nvidia/" prefix → NVIDIA NIM (strips the prefix for the wire id).
//  4. Everything else → OpenRouter, the reference OpenAI-compatible gateway.
//
// An unknown protocol value is an error, surfaced to the caller for exit-code
// mapping rather than silently falling back.
func resolveProvider(model, baseURL, protocol, providerName string) (provider.Provider, string, error) {
	// Explicit --provider selects a built-in provider from the registry and
	// wins over both --protocol inference and model-id heuristics.
	if strings.TrimSpace(providerName) != "" {
		return resolveNamedProvider(providerName, model, baseURL, protocol)
	}

	// 0. Explicit protocol selection wins over every heuristic.
	switch protocol {
	case "openai":
		if strings.TrimSpace(baseURL) == "" {
			return nil, "", fmt.Errorf("--protocol openai requires --base-url")
		}
		return provider.NewOpenAICompatibleProvider(baseURL, []provider.Model{{Provider: "openai", ID: model, SupportsImages: true}}), "openai", nil
	case "anthropic":
		return provider.NewAnthropicProvider(baseURL, []provider.Model{{Provider: "anthropic", ID: model, SupportsImages: true}}), "anthropic", nil
	case "":
		// fall through to heuristic resolution
	default:
		return nil, "", fmt.Errorf("unknown --protocol %q (want openai|anthropic)", protocol)
	}

	// 1. Preset catalog wins: a curated id knows its own provider.
	if p, ok := provider.LookupPreset(model); ok {
		switch p.Provider {
		case "nvidia":
			return provider.NewNvidiaProvider(baseURL, []provider.Model{{Provider: "nvidia", ID: model, SupportsImages: true}}), "nvidia", nil
		case "ollama":
			id := strings.TrimPrefix(model, "ollama/")
			return provider.NewOllamaProvider(baseURL, []provider.Model{{Provider: "ollama", ID: id, SupportsImages: true}}), "ollama", nil
		case "", "openrouter":
			return provider.NewOpenRouterProvider(baseURL, []provider.Model{{Provider: "openrouter", ID: model, SupportsImages: true}}), "openrouter", nil
		default:
			// Any other preset provider is a named built-in (e.g. deepseek,
			// qianfan, dashscope): build it from the registry so the correct
			// base URL, protocol, and API-key env var are used — not OpenRouter's.
			return resolveNamedProvider(p.Provider, model, baseURL, protocol)
		}
	}

	// 2. Local Ollama by prefix or port.
	if strings.HasPrefix(model, "ollama/") || strings.Contains(baseURL, "11434") {
		id := strings.TrimPrefix(model, "ollama/")
		return provider.NewOllamaProvider(baseURL, []provider.Model{{Provider: "ollama", ID: id, SupportsImages: true}}), "ollama", nil
	}
	// 3. NVIDIA NIM by prefix.
	if strings.HasPrefix(model, "nvidia/") {
		id := strings.TrimPrefix(model, "nvidia/")
		return provider.NewNvidiaProvider(baseURL, []provider.Model{{Provider: "nvidia", ID: id, SupportsImages: true}}), "nvidia", nil
	}
	// 4. Default: OpenRouter.
	return provider.NewOpenRouterProvider(baseURL, []provider.Model{{Provider: "openrouter", ID: model, SupportsImages: true}}), "openrouter", nil
}

// resolveNamedProvider builds the driver for an explicit --provider selection.
// It looks the name up in the built-in registry and constructs the wire driver
// matching the spec's Protocol: "openai" → an OpenAI-compatible (Bearer) driver,
// "anthropic" → an Anthropic-Messages driver. The base URL follows the override
// precedence in resolveBaseURL (--base-url > provider-specific env > generic
// <PROVIDER>_BASE_URL > spec default). The returned provider-name string is the
// spec name, so downstream API-key resolution reads the provider's own env var
// (spec.EnvVars).
//
// Special providers with bespoke auth (azure/bedrock/vertex/cloudflare —
// AuthScheme aws/azure/special, or the cloudflare-* names) are routed to
// provider.ResolveSpecialProvider, which validates their required env vars and
// composes the concrete endpoint (node #188).
func resolveNamedProvider(name, model, baseURL, protocol string) (provider.Provider, string, error) {
	spec, ok := provider.LookupProviderSpec(name)
	if !ok {
		return nil, "", fmt.Errorf("unknown --provider %q (available: %s)", name, strings.Join(provider.ProviderNames(), ", "))
	}
	// A concurrently-set --protocol must agree with the provider's own protocol;
	// an incompatible pair is a user error naming both flags.
	if p := strings.TrimSpace(protocol); p != "" && p != spec.Protocol {
		return nil, "", fmt.Errorf("--provider %q speaks the %q protocol, which conflicts with --protocol %q; drop --protocol or set it to %q", name, spec.Protocol, p, spec.Protocol)
	}
	// Special-auth providers (Azure / Bedrock / Vertex / Cloudflare) compose
	// their endpoint from several env vars and/or need non-standard credential
	// validation, so route them to the dedicated resolver (US-007 / node #188).
	// It performs its own base-URL composition (honoring the --base-url override)
	// and returns a clear error naming any absent required env var.
	if provider.IsSpecialAuthProvider(spec) {
		p, err := provider.ResolveSpecialProvider(spec, model, baseURL, os.Getenv)
		if err != nil {
			return nil, "", err
		}
		return p, spec.Name, nil
	}
	// Base-URL precedence (US-004 / FR-8, FR-9): --base-url flag > provider-
	// specific base-url env var(s) > generic <PROVIDER>_BASE_URL > spec default.
	url := resolveBaseURL(spec, baseURL)
	models := []provider.Model{{Provider: spec.Name, ID: model, SupportsImages: true}}
	// Note: spec.ExtraHeaders would be attached here, but the exported generic
	// constructors do not yet accept custom headers; all built-in specs currently
	// carry no ExtraHeaders, so this is a no-op today (refined alongside #188).
	switch spec.Protocol {
	case provider.ProtocolAnthropic:
		// Auth header follows the spec's AuthScheme (x-api-key + anthropic-version
		// for anthropic/minimax/minimax-cn; Bearer for any anthropic-protocol
		// gateway that authenticates with a plain bearer token). The driver name is
		// the spec name so errors reference the selected provider.
		return provider.NewAnthropicProtocolProvider(spec.Name, url, spec.AuthScheme, models), spec.Name, nil
	case provider.ProtocolOpenAI:
		return provider.NewOpenAICompatibleProvider(url, models), spec.Name, nil
	default:
		// The registry only ever stores openai/anthropic; guard anyway so an
		// unexpected value is a clear error rather than a nil provider.
		return nil, "", fmt.Errorf("--provider %q has unsupported protocol %q", name, spec.Protocol)
	}
}

// builtinTools returns the default file/shell tool set rooted at cwd, or nil
// when tools are disabled. The todo tool is stateful: a single TodoStore is
// created here and held by the one TodoTool instance, so the task list persists
// across calls within a run (a later write replaces the plan).
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
		&agenttool.TodoTool{Store: agenttool.NewTodoStore()},
		&agenttool.WebFetchTool{},
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
