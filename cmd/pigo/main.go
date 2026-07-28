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

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/config"
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
	flag.StringVar(&opts.systemPrompt, "system-prompt", "", "system prompt to use instead of the default coding-assistant prompt (对标 pi --system-prompt)")
	flag.StringArrayVar(&opts.appendSystemPrompt, "append-system-prompt", nil, "append text or file contents to the system prompt; repeatable (对标 pi --append-system-prompt)")
	flag.StringArrayVar(&opts.promptTemplates, "prompt-template", nil, "load a prompt template from a file or directory (non-recursive); repeatable (对标 pi --prompt-template)")
	flag.StringVar(&opts.thinkingLevel, "thinking-level", "", "reasoning effort: off|minimal|low|medium|high|xhigh (overrides PIGO_THINKING_LEVEL and config; default medium)")
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
		cli.PrintProviderHelp(out)
	}
	flag.Parse()

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
