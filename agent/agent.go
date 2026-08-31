package agent

import (
	"context"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// Session is a single, stateful pigo agent conversation. Create one with New,
// drive it with Prompt or Stream, and release its resources with Close. The
// conversation history accumulates across calls, so follow-up prompts see the
// earlier exchange; call Reset to start over on the same session.
//
// A Session is not safe for concurrent use. Drive it from a single goroutine, or
// give each goroutine its own Session.
type Session struct {
	env      run.Env
	runCfg   runtime.RunConfig
	agentCtx *agentcore.AgentContext
	model    string
}

// New builds a Session from the given options. It resolves the provider and
// credentials, assembles the tool set, and validates the tool policy and
// thinking level up front, so a configuration mistake (an unknown tool name, an
// invalid thinking level, an unresolvable provider) is returned here rather than
// surfacing on the first Prompt.
//
// No network call is made by New: the provider is only contacted when you call
// Prompt or Stream. This makes New cheap and safe to use in tests.
//
// See the package documentation for the default tool, skill, and memory
// behavior — in particular, that tools are enabled and auto-executed by default.
func New(opts ...Option) (*Session, error) {
	c := config{model: "openrouter/free"}
	for _, o := range opts {
		o(&c)
	}

	// Validate the reasoning-effort level through the same layered config chain
	// the CLI uses, so an invalid WithThinkingLevel value fails fast here.
	thinking, err := run.ResolveThinkingLevel(c.thinking)
	if err != nil {
		return nil, err
	}

	// One ToolPolicy value carries both lists so they cannot be swapped; deny
	// always wins over allow inside run.ApplyToolPolicy.
	policy := run.NewToolPolicy(c.allowedTools, c.disallowedTools)

	// SetupEnv resolves the provider, assembles the (policy-filtered) tool set,
	// builds the system prompt, and — because skills/memory are opt-in here —
	// leaves the machine's shared state untouched unless WithSkills/WithMemory
	// were passed. It also validates the tool policy against the real tool set,
	// so an unknown tool name is reported as an error.
	env, err := run.SetupEnv(
		c.model, c.baseURL, c.protocol, c.provider, c.apiKey,
		c.noTools, !c.skills, c.systemPrompt, c.appendSystemPrompt, c.memory, policy,
	)
	if err != nil {
		return nil, err
	}

	allTools, err := adaptCustomTools(c.customTools, env.Tools)
	if err != nil {
		if env.Plugins != nil {
			_ = env.Plugins.Close()
		}
		if env.Memory != nil {
			_ = env.Memory.Close()
		}
		return nil, err
	}
	env.Tools = allTools

	// Resolve the API key by provider name: an explicit WithAPIKey overrides the
	// provider's environment variable. The key is held only in the credential
	// store and never logged.
	creds := provider.NewCredentialStore(nil)
	if c.apiKey != "" {
		creds.SetOverride(env.ProviderName, c.apiKey)
	}

	runCfg := run.NewConfig(
		c.model, env.ProviderName, thinking, env.Provider, creds,
		run.ToolRegistry(env.Tools), run.TodoReminders(env.Tools),
	)

	return &Session{
		env:    env,
		runCfg: runCfg,
		agentCtx: &agentcore.AgentContext{
			SystemPrompt: env.SysPrompt,
			Tools:        env.Tools,
		},
		model: c.model,
	}, nil
}

func structuredStreamHandler(onEvent func(Event)) runtime.StreamHandler {
	if onEvent == nil {
		return runtime.StreamHandler{}
	}
	mapper := &eventMapper{emit: onEvent}
	return runtime.StreamHandler{OnEvent: mapper.handle}
}

// Prompt sends one user message, runs the agent loop to completion (executing
// any tool calls the model makes along the way), and returns the assistant's
// final text. The exchange is appended to the session history so later prompts
// have this context.
func (s *Session) Prompt(ctx context.Context, prompt string) (string, error) {
	return s.Stream(ctx, prompt, nil)
}

// Stream is Prompt with incremental output: onText, if non-nil, is called with
// each chunk of assistant text as it arrives, and the complete final text is
// also returned. Tool calls still run automatically between text chunks. A nil
// onText makes Stream behave exactly like Prompt.
func (s *Session) Stream(ctx context.Context, prompt string, onText func(string)) (string, error) {
	return s.run(ctx, prompt, runtime.StreamHandler{OnText: onText})
}

// StreamEvents is Prompt with stable structured events. onEvent, if non-nil,
// receives message/thinking deltas, tool execution lifecycle events, message
// completion, and usage in agent-loop order. The complete final assistant text
// is also returned. A nil onEvent behaves exactly like Prompt.
func (s *Session) StreamEvents(ctx context.Context, prompt string, onEvent func(Event)) (string, error) {
	return s.run(ctx, prompt, structuredStreamHandler(onEvent))
}

func (s *Session) run(ctx context.Context, prompt string, handler runtime.StreamHandler) (string, error) {
	// The loop expects the initiating user message already appended; it then
	// mutates agentCtx.Messages in place (assistant + tool results), which is
	// what carries the conversation forward across calls.
	s.agentCtx.Messages = append(s.agentCtx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(prompt)},
	})

	stream := runtime.StartRun(ctx, s.agentCtx, s.runCfg)
	final, err := runtime.DrainStream(ctx, stream, handler)
	if err != nil {
		return "", err
	}
	if final == nil {
		return "", nil
	}
	return agentcore.ContentToText(final.Content), nil
}

// Reset clears the conversation history, so the next Prompt starts a fresh
// exchange. The provider, tool set, and system prompt are unchanged.
func (s *Session) Reset() {
	s.agentCtx.Messages = nil
}

// ToolNames returns the names of the tools available to this session, in the
// order they are advertised to the model. It reflects both the applied built-in
// tool policy and explicitly registered custom tools. A WithoutTools session is
// empty unless WithCustomTools supplied an explicit custom set.
func (s *Session) ToolNames() []string {
	names := make([]string, len(s.env.Tools))
	for i, t := range s.env.Tools {
		names[i] = t.Name()
	}
	return names
}

// Model returns the model id the session was created with.
func (s *Session) Model() string { return s.model }

// Provider returns the resolved provider name (e.g. "anthropic", "openrouter"),
// which is inferred from the model id unless WithProvider was set.
func (s *Session) Provider() string { return s.env.ProviderName }

// Close releases resources held by the session: any loaded plugin manager and
// the persistent memory store (when WithMemory was used). It is safe to call
// once, and safe to call on a session that holds neither. After Close the
// session must not be used again.
func (s *Session) Close() error {
	var firstErr error
	if s.env.Plugins != nil {
		if err := s.env.Plugins.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.env.Memory != nil {
		if err := s.env.Memory.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
