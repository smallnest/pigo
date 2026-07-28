// This file defines LiveConfig, the mutable run configuration a control command
// may change mid-session. It was moved verbatim from cmd/pigo (the former
// liveRunConfig) and exported so the run, repl, btw, status and goal
// subpackages can read and mutate it through the Host contract. The run closure
// reads it on every prompt, so a /model switch takes effect on the next turn.
// It carries no lock: it is read and written only on the REPL's single main
// goroutine (slash actions and the run are both invoked synchronously from the
// REPL loop, never concurrently).
package cli

import (
	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

// LiveConfig is the mutable run configuration a control command may change
// mid-session.
type LiveConfig struct {
	Model        string
	ProviderName string
	Provider     provider.Provider
	BaseURL      string
	Protocol     string
	// ThinkingLevel is the reasoning-effort level applied to each turn. It is
	// seeded from the resolved config chain and read on every prompt.
	ThinkingLevel agentcore.ThinkingLevel
	// ContextWindow is the model's total context-token budget, used to gate
	// automatic compaction. When 0 the window is unknown and auto-compaction is
	// disabled; the REPL seeds it with a conservative default so long sessions
	// still compact rather than overflow.
	ContextWindow int
}

// DefaultContextWindow is the fallback context-token budget used when a model's
// true window is unknown. It is deliberately large so auto-compaction only fires
// on genuinely long sessions (threshold = window - ReserveTokens), never on
// ordinary short exchanges.
const DefaultContextWindow = 128000
