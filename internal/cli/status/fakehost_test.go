package status

// fakeHost satisfies cli.Host by embedding the interface (so every method is
// present) while overriding only the accessors RunStatus reads: Live, Header,
// AgentCtx, Cwd, Trust, Slash, Creds, Telemetry. The embedded nil interface
// would panic if any other method were called, which these tests never do.
// This lets the status tests run without the package-main REPL harness.

import (
	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/trust"
)

type fakeHost struct {
	cli.Host
	live      *cli.LiveConfig
	header    session.SessionHeader
	agentCtx  *agentcore.AgentContext
	cwd       string
	trust     *trust.Manager
	slash     *runtime.SlashRegistry
	creds     *provider.CredentialStore
	telemetry *cli.TelemetryHolder
}

func (f *fakeHost) Live() *cli.LiveConfig            { return f.live }
func (f *fakeHost) Header() session.SessionHeader    { return f.header }
func (f *fakeHost) AgentCtx() *agentcore.AgentContext { return f.agentCtx }
func (f *fakeHost) Cwd() string                      { return f.cwd }
func (f *fakeHost) Trust() *trust.Manager            { return f.trust }
func (f *fakeHost) Slash() *runtime.SlashRegistry    { return f.slash }
func (f *fakeHost) Creds() *provider.CredentialStore { return f.creds }
func (f *fakeHost) Telemetry() *cli.TelemetryHolder  { return f.telemetry }

// newFakeHost builds a fakeHost with empty-but-non-nil live config, agent
// context, slash registry and credential store, mirroring a fresh session.
// Tests customize the returned host's fields before calling RunStatus.
func newFakeHost() *fakeHost {
	return &fakeHost{
		live:     &cli.LiveConfig{},
		agentCtx: &agentcore.AgentContext{},
		slash:    runtime.NewSlashRegistry(),
		creds:    provider.NewCredentialStore(nil),
	}
}
