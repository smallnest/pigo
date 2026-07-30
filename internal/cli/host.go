// This file defines the Host and Editor contracts that let the /goal, /btw,
// /status and REPL subpackages read and mutate a session's live state without
// importing the concrete replDeps aggregate that assembles it (see doc.go). The
// aggregate implements Host by exposing accessor and mutator methods over its
// fields; the line editor implements Editor.
package cli

import (
	"bufio"
	"errors"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/trust"
)

// ErrLineInterrupted is returned by an Editor.ReadLine when the user hits an
// idle Ctrl+C at the prompt (rather than during a run). Control commands that
// run their own follow-up loop (e.g. /btw) check for it with errors.Is to leave
// the loop cleanly. It lives here, beside the Editor contract, so a subpackage
// can recognize the interrupt without importing the concrete line editor.
var ErrLineInterrupted = errors.New("line input interrupted")

// Host is the seam through which a control command (/goal, /btw, /status) and
// the REPL loop access the collaborators and mutable state of a session. It is
// satisfied by the replDeps aggregate assembled once per session.
//
// Getters return the shared collaborators (never copies, except where noted).
// The setters cover the fields a command may advance mid-session: the active
// session leaf, the persisted-message count, and the last /btw side thread.
type Host interface {
	// Session collaborators.
	Store() *session.Store
	Header() session.SessionHeader
	AgentCtx() *agentcore.AgentContext
	Live() *LiveConfig
	Registry() *agenttool.ToolRegistry
	Reminders() *runtime.ReminderRegistry
	Slash() *runtime.SlashRegistry
	Creds() *provider.CredentialStore
	Notifier() *plugin.EventNotifier
	// NotifierHandle returns the plugin event-delivery callback for this
	// session's runs, or nil when no plugin subscribed.
	NotifierHandle() func(agentcore.AgentEvent)
	Trust() *trust.Manager
	Goal() *agenttool.GoalState
	Telemetry() *TelemetryHolder

	// Dispatcher returns the session's hook dispatcher, or nil when no hooks are
	// configured (FR-18). Side-runs (/goal, /btw) install the per-turn seams onto
	// their own cfg via run.InstallSeams(cfg, host.Dispatcher(), host.HookDeps()).
	Dispatcher() *hooks.Dispatcher
	// HookDeps carries the session id / project dir stamped onto every HookInput.
	HookDeps() run.HookDeps

	// Cwd is the directory pigo was launched in; it does not change during a
	// session and gates side-effect tools.
	Cwd() string
	// Input is the shared buffered stdin reader used by both the main loop and
	// the tool-call confirmation prompt.
	Input() *bufio.Reader
	// ConfirmMu serializes tool-call confirmation prompts across concurrent
	// side-effect tool calls.
	ConfirmMu() *sync.Mutex

	// Session-tree cursor and persistence bookkeeping.
	CurLeaf() string
	SetCurLeaf(id string)
	Persisted() int
	SetPersisted(n int)

	// Last /btw side thread from this process and its background base index.
	LastBtw() *agentcore.AgentContext
	SetLastBtw(ctx *agentcore.AgentContext)
	LastBtwBase() int
	SetLastBtwBase(n int)
}

// Editor is the line-input contract a control command uses to read a follow-up
// line from the user (e.g. the /btw follow-up loop). It is satisfied by the
// REPL's line editor.
type Editor interface {
	ReadLine(prompt string) (string, error)
}
