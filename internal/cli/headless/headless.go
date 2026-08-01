// This file is the headless run driver: the print / stream-json run path
// (US-020) extracted from the CLI dispatch seam (#363). dispatch resolves the
// output mode and the run environment, then hands off to Run, which wires the
// session, prompt, thinking level, and provider credentials into a
// runtime.HeadlessConfig and executes one run. Plugin slash commands and output
// mode parsing live here because they are specific to the headless path.
package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// RunParams carries the resolved inputs for one headless run. Mode and Env are
// resolved by the caller (dispatch) — Mode via ParseOutputMode, Env via
// run.SetupEnv — so their distinct exit codes stay at the call site; Run owns
// the rest of the run lifecycle.
type RunParams struct {
	Mode          runtime.HeadlessMode
	Env           run.Env
	Prompt        string
	Model         string
	APIKey        string
	ThinkingLevel string
	ResumeID      string
}

// Run executes one headless run over p.Prompt, writing agent output to out and
// diagnostics to errOut, and returns a process exit code (0 = success). The run
// is backed by a session so its id appears in the first stream-json event and it
// can be resumed with --resume/--continue; a resumed session seeds its prior
// messages ahead of the new prompt.
func Run(ctx context.Context, p RunParams, out, errOut io.Writer) int {
	env := p.Env
	// Best-effort plugin slash-command support in headless mode: if the prompt is
	// a "/cmd ..." naming a plugin command, invoke it, print its notifications to
	// errOut, and use the returned prompt for this run (appending the raw args if
	// the command produced no prompt). Headless has no turn injection, so
	// appending the returned prompt is the accepted behavior. A non-plugin prompt
	// or unknown command is left untouched.
	headlessPrompt := resolveHeadlessPluginCommand(p.Prompt, env.Plugins, errOut)
	promptContent, err := ui.BuildUserContent(headlessPrompt)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}

	// Back the headless run with a session so its id appears in the first
	// stream-json event and the run can be resumed with --resume/--continue,
	// matching the interactive REPL and pi/Claude Code. A resumed session seeds
	// its prior messages ahead of the new prompt.
	priorMsgs, hs, err := openHeadlessSession(p.ResumeID, p.Model, env.ProviderName, env.SysPrompt)
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
	thinking, err := run.ResolveThinkingLevel(p.ThinkingLevel)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 2
	}

	// Resolve the API key by provider name from the environment (never logged).
	// An explicit --api-key overrides env/config for the resolved provider.
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(env.ProviderName, p.APIKey)
	runCfg := run.NewConfig(p.Model, env.ProviderName, thinking, env.Provider, creds, run.ToolRegistry(env.Tools), run.TodoReminders(env.Tools))
	runCfg.SessionID = hs.header.ID
	// Route auto-compaction checkpoints to the shared memory root so a rebuild can
	// recover the pre-watermark prefix (no-op when memory is disabled → empty root).
	runCfg.MemoryRoot = run.MemoryRootFromTools(env.Tools)

	// Wire hooks uniformly with every other driver (#425): resolve the trust-gated
	// hook set, install the tool-execution + Stop seams, dispatch SessionStart, and
	// chain the SessionEnd/PreCompact observer onto the plugin event notifier. A
	// malformed hook layer is a config error (exit 2), matching thinking-level.
	source := "startup"
	if p.ResumeID != "" {
		source = "resume"
	}
	set, herr := run.ResolveHookSet(env.Cwd, run.Trusted(env.Cwd))
	if herr != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", herr)
		return 2
	}
	hookDeps := run.HookDeps{SessionID: hs.header.ID, ProjectDir: env.Cwd, WarnLog: errOut}
	// Deliver agent lifecycle events to any subscribed plugin (US-017, #133).
	// NewEventNotifier returns nil when no plugin subscribes, so the base handler
	// stays nil in the common no-plugin case.
	var baseOnEvent func(agentcore.AgentEvent)
	if n := plugin.NewEventNotifier(env.Plugins, errOut); n != nil {
		baseOnEvent = n.Handle
	}
	d, onEvent := run.InstallDriverHooks(ctx, &runCfg, set, hookDeps, source, baseOnEvent)
	// UserPromptSubmit runs before the prompt is handed to the loop: a block aborts
	// the headless run non-zero; additionalContext is injected into this run only.
	if d != nil {
		if block, reason := run.DispatchUserPromptSubmit(ctx, d, &runCfg, hookDeps, headlessPrompt); block {
			fmt.Fprintf(errOut, "pigo: prompt blocked by hook: %s\n", reason)
			return 1
		}
	}

	cfg := runtime.HeadlessConfig{
		Mode: p.Mode,
		Out:  out,
		Run:  runCfg,
	}
	cfg.OnEvent = onEvent
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

// ParseOutputMode maps the --output-format flag onto a HeadlessMode, erroring on
// an unknown value.
func ParseOutputMode(outputFmt string) (runtime.HeadlessMode, error) {
	switch outputFmt {
	case "text", "":
		return runtime.PrintMode, nil
	case "stream-json":
		return runtime.StreamJSONMode, nil
	default:
		return 0, fmt.Errorf("unknown --output-format %q (want text|stream-json)", outputFmt)
	}
}
