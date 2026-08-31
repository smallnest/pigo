package agent

// config is the resolved, unexported construction state for a Session. It is
// populated only through Option values, so callers never name or mutate it
// directly — the exported surface stays limited to With* constructors and the
// Session methods.
type config struct {
	model              string
	baseURL            string
	protocol           string
	provider           string
	apiKey             string
	systemPrompt       string
	appendSystemPrompt []string
	thinking           string
	noTools            bool
	allowedTools       []string
	disallowedTools    []string
	customTools        []Tool
	skills             bool
	memory             bool
}

// Option configures a Session at construction time. Options are applied in the
// order passed to New, so a later option overrides an earlier one that sets the
// same field. Because config is unexported, the only way to produce an Option is
// through the With* constructors below — which keeps the public surface free of
// internal types.
type Option func(*config)

// WithModel sets the model id, which also selects the provider the way the pigo
// CLI does (e.g. "claude-opus-4-8" → Anthropic, "openrouter/free" → OpenRouter).
// The default is "openrouter/free".
func WithModel(model string) Option {
	return func(c *config) { c.model = model }
}

// WithBaseURL points the session at a custom endpoint. Pair it with
// [WithProtocol] to say whether that endpoint speaks the OpenAI or Anthropic
// wire format.
func WithBaseURL(baseURL string) Option {
	return func(c *config) { c.baseURL = baseURL }
}

// WithProtocol selects the wire protocol for a custom endpoint: "openai" or
// "anthropic". It is only consulted when [WithBaseURL] is set.
func WithProtocol(protocol string) Option {
	return func(c *config) { c.protocol = protocol }
}

// WithProvider selects a named provider from your pigo configuration instead of
// inferring one from the model id.
func WithProvider(name string) Option {
	return func(c *config) { c.provider = name }
}

// WithAPIKey sets the API key for the resolved provider, overriding the
// provider's environment variable. When unset, the provider's usual environment
// variable is used (e.g. ANTHROPIC_API_KEY, OPENROUTER_API_KEY).
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key }
}

// WithSystemPrompt replaces pigo's built-in base instruction with prompt. Use
// this for full control over the agent's persona and rules; use
// [WithAppendSystemPrompt] instead to keep the built-in instruction and add to
// it.
func WithSystemPrompt(prompt string) Option {
	return func(c *config) { c.systemPrompt = prompt }
}

// WithAppendSystemPrompt appends one or more blocks to the system prompt,
// leaving pigo's built-in instruction in place. Repeated calls accumulate.
func WithAppendSystemPrompt(blocks ...string) Option {
	return func(c *config) {
		c.appendSystemPrompt = append(c.appendSystemPrompt, blocks...)
	}
}

// WithThinkingLevel sets the reasoning-effort level. Valid values are "off",
// "minimal", "low", "medium", "high", "xhigh", and "max". The default is
// "medium". An invalid value makes New return an error.
func WithThinkingLevel(level string) Option {
	return func(c *config) { c.thinking = level }
}

// WithTools restricts the session to the named built-in tools (an allowlist,
// e.g. WithTools("read", "grep")). Names are matched case-insensitively, so
// "Read" and "read" are equivalent. A name that matches no built-in makes New
// return an error rather than silently ignoring it. Combine with
// [WithDisallowedTools]; deny always wins over allow. Explicit custom tools
// registered with [WithCustomTools] are separate from this built-in allowlist.
func WithTools(names ...string) Option {
	return func(c *config) { c.allowedTools = append(c.allowedTools, names...) }
}

// WithDisallowedTools removes the named built-in tools (a denylist, e.g.
// WithDisallowedTools("bash")). Deny always wins: a built-in named here is
// removed even if it also appears in [WithTools]. As with WithTools, an unknown
// built-in name makes New return an error. Explicit custom tools registered with
// [WithCustomTools] are not filtered by this built-in denylist.
func WithDisallowedTools(names ...string) Option {
	return func(c *config) { c.disallowedTools = append(c.disallowedTools, names...) }
}

// WithoutTools removes every built-in tool. It overrides [WithTools] and
// [WithDisallowedTools], which become inert once the built-in set is empty.
// Explicit tools registered with [WithCustomTools] remain available, so
// WithoutTools plus WithCustomTools is the custom-only embedding pattern.
func WithoutTools() Option {
	return func(c *config) { c.noTools = true }
}

// WithCustomTools explicitly registers caller-owned tools. Custom tools are
// installed after built-in policy resolution, so they remain available when
// [WithoutTools] is used. New validates tool names, schemas, execution modes,
// duplicate names, and collisions with any surviving built-in tool.
func WithCustomTools(tools ...Tool) Option {
	return func(c *config) {
		c.customTools = append(c.customTools, tools...)
	}
}

// WithSkills enables discovery of on-disk skills, which are advertised to the
// model and loadable during a run. Skills are off by default so an embedded
// session stays independent of the machine's shared skills directory.
func WithSkills() Option {
	return func(c *config) { c.skills = true }
}

// WithMemory enables pigo's persistent memory store, letting the agent recall
// context saved by earlier runs and record new memories. Memory is off by
// default so an embedded session does not read or write shared state unless
// asked.
func WithMemory() Option {
	return func(c *config) { c.memory = true }
}
