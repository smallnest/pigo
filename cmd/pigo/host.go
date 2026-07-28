// This file makes replDeps satisfy cli.Host: the accessor and mutator methods
// let the /goal, /btw, /status and REPL logic reach the session's collaborators
// and mutable state through the cli.Host contract rather than the concrete
// aggregate. The compile-time assertion below fails the build if replDeps drifts
// out of conformance.
package main

import (
	"bufio"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/trust"
)

var _ cli.Host = (*replDeps)(nil)

func (d *replDeps) Store() *session.Store                      { return d.store }
func (d *replDeps) Header() session.SessionHeader              { return d.header }
func (d *replDeps) AgentCtx() *agentcore.AgentContext          { return d.agentCtx }
func (d *replDeps) Live() *cli.LiveConfig                      { return d.live }
func (d *replDeps) Registry() *agenttool.ToolRegistry          { return d.reg }
func (d *replDeps) Reminders() *runtime.ReminderRegistry       { return d.reminders }
func (d *replDeps) Slash() *runtime.SlashRegistry              { return d.slash }
func (d *replDeps) Creds() *provider.CredentialStore           { return d.creds }
func (d *replDeps) Notifier() *plugin.EventNotifier            { return d.notifier }
func (d *replDeps) NotifierHandle() func(agentcore.AgentEvent) { return d.notifierHandle() }
func (d *replDeps) Trust() *trust.Manager                      { return d.trust }
func (d *replDeps) Goal() *agenttool.GoalState                 { return d.goal }
func (d *replDeps) Telemetry() *cli.TelemetryHolder            { return d.telemetry }
func (d *replDeps) Cwd() string                                { return d.cwd }
func (d *replDeps) Input() *bufio.Reader                       { return d.in }
func (d *replDeps) ConfirmMu() *sync.Mutex                     { return d.confirmMu }

func (d *replDeps) CurLeaf() string      { return d.curLeaf }
func (d *replDeps) SetCurLeaf(id string) { d.curLeaf = id }
func (d *replDeps) Persisted() int       { return d.persisted }
func (d *replDeps) SetPersisted(n int)   { d.persisted = n }

func (d *replDeps) LastBtw() *agentcore.AgentContext       { return d.lastBtw }
func (d *replDeps) SetLastBtw(ctx *agentcore.AgentContext) { d.lastBtw = ctx }
func (d *replDeps) LastBtwBase() int                       { return d.lastBtwBase }
func (d *replDeps) SetLastBtwBase(n int)                   { d.lastBtwBase = n }
