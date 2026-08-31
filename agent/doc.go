// Package agent is the public, embeddable SDK for driving a pigo agent from
// your own Go program. It wraps pigo's internal run-assembly, provider, and
// agent-loop packages behind a stable public surface of SDK-owned values and
// callbacks. No exported signature mentions a pigo internal type, so pigo can
// evolve its implementation packages without forcing consumers to depend on
// them.
//
// # Quick start
//
//	sess, err := agent.New(
//		agent.WithModel("claude-opus-4-8"),
//		agent.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
//	)
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer sess.Close()
//
//	reply, err := sess.Prompt(context.Background(), "Say hello in one word.")
//	fmt.Println(reply)
//
// # Model, provider, credentials
//
// The model id selects the provider the same way the pigo CLI does:
// "claude-opus-4-8" resolves to Anthropic, "openrouter/free" to OpenRouter,
// and so on. Point at any OpenAI- or Anthropic-compatible endpoint with
// [WithBaseURL] + [WithProtocol], or a named provider from your config with
// [WithProvider]. The API key comes from [WithAPIKey] or, if unset, the
// provider's usual environment variable (e.g. ANTHROPIC_API_KEY). Keys are
// never logged.
//
// # Tools run automatically — read this
//
// By default a session is created with pigo's full built-in tool set (read,
// write, edit, bash, find, grep, and more) and those tools are executed WITHOUT
// any per-call confirmation prompt — equivalent to running the CLI with
// --approve. An agent can therefore read, modify, and delete files under its
// working directory and run shell commands on the host. This is the right
// default for an automated SDK, but it means you should only send prompts you
// trust, and run in a directory (and, ideally, a sandbox) you are willing to let
// the agent modify. To constrain or remove built-ins use [WithTools] (an
// allowlist), [WithDisallowedTools] (a denylist, which always wins), or
// [WithoutTools]. Explicit caller-owned tools can be added with [WithCustomTools];
// combining WithoutTools with WithCustomTools creates a custom-only session.
//
// # Streaming
//
// [Session.Stream] reports incremental assistant text. [Session.StreamEvents]
// reports SDK-owned structured message, thinking, tool, and usage events while
// returning the same final assistant text as Prompt.
//
// # Conversation state
//
// A [Session] keeps the running conversation: each [Session.Prompt],
// [Session.Stream], or [Session.StreamEvents] call appends to the same history,
// so follow-up prompts see
// what came before. Call [Session.Reset] to start a fresh conversation on the
// same session, or [Session.Close] when you are done. A Session is NOT safe for
// concurrent use — drive it from one goroutine, or create one Session per
// goroutine.
//
// # Defaults
//
//   - Tools: on (full built-in set, auto-executed; see the safety note above).
//   - Skills: off — enable discovery of on-disk skills with [WithSkills].
//   - Memory: off — enable the persistent memory store with [WithMemory].
//   - Thinking: "medium" — override with [WithThinkingLevel].
//
// Skills and memory are off by default so an embedded session is hermetic: it
// does not read or write the machine's shared pigo state unless you ask it to.
package agent
