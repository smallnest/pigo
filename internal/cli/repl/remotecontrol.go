// This file wires the remote-control bridge (internal/remotecontrol, #442) into
// the interactive REPL (#443). It adds the "/remote-control" command that
// starts/stops an in-process HTTP+WebSocket server mirroring the session to a
// paired browser on the LAN, tees REPL output to that browser, merges
// browser-submitted prompts into the input loop, and routes tool-call
// confirmations to the browser while a client is connected.
//
// The design keeps the non-remote path byte-identical: when no remote session
// is active, teeWriter forwards only to stdout, the input select degenerates to
// a plain editor read (the remote channel is nil), and confirmations use the
// local stdin prompt unchanged.
package repl

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/remotecontrol"
	"github.com/smallnest/pigo/internal/trust"
)

// teeWriter is an io.Writer that always forwards to a primary writer (the
// terminal) and, when a secondary is set, mirrors the same bytes to it (the
// remote browser). It is safe for concurrent Write/setSecondary because the
// bridge output writer is fed from the REPL goroutine while the secondary is
// toggled by the /remote-control command on the same goroutine, but writes to
// the WebSocket happen on other goroutines; the mutex keeps the swap atomic.
type teeWriter struct {
	primary io.Writer
	mu      sync.Mutex
	second  io.Writer
}

func newTeeWriter(primary io.Writer) *teeWriter { return &teeWriter{primary: primary} }

func (t *teeWriter) setSecondary(w io.Writer) {
	t.mu.Lock()
	t.second = w
	t.mu.Unlock()
}

func (t *teeWriter) Write(p []byte) (int, error) {
	// The primary write is authoritative for the returned count/err so terminal
	// behavior is unchanged; a mirror failure never breaks the local session.
	n, err := t.primary.Write(p)
	t.mu.Lock()
	second := t.second
	t.mu.Unlock()
	if second != nil {
		_, _ = second.Write(p)
	}
	return n, err
}

// remoteSession owns the running server + bridge for one /remote-control
// activation. It is nil in deps until the command starts a session, and is
// cleared on stop.
type remoteSession struct {
	server *remotecontrol.Server
	bridge *remotecontrol.Bridge
	url    string
}

// inputChan returns the bridge's remote-input channel while a session is
// active, or nil when inactive. A nil channel blocks forever in a select, so
// the input loop transparently ignores remote input when remote control is off.
func (rs *remoteSession) inputChan() <-chan string {
	if rs == nil || rs.bridge == nil {
		return nil
	}
	return rs.bridge.RemoteInput()
}

// hasClient reports whether a browser is currently paired and connected.
func (rs *remoteSession) hasClient() bool {
	return rs != nil && rs.bridge != nil && rs.bridge.Enabled()
}

// runRemoteControl handles the "/remote-control" command and its "stop"/"status"
// subcommands. It mutates deps in place (deps.remote, deps.tee) so the input
// loop and output tee pick up the change on the next iteration.
func runRemoteControl(out io.Writer, deps *replDeps, line string) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/remote-control"))
	switch arg {
	case "stop":
		stopRemoteControl(out, deps)
	case "status", "":
		if arg == "status" {
			remoteControlStatus(out, deps)
			return
		}
		startRemoteControl(out, deps)
	default:
		fmt.Fprintf(out, "usage: /remote-control [stop|status]\n")
	}
}

func startRemoteControl(out io.Writer, deps *replDeps) {
	if deps.remote != nil {
		fmt.Fprintf(out, "remote control already running: %s\n", deps.remote.url)
		return
	}
	// Handler is set after the server is built (SetHandler), but NewServer takes
	// it up front; the bridge's Sink is the server itself, so build the server
	// first with the bridge as handler once the bridge exists. To break the
	// cycle we construct the server, then the bridge (Sink=server), then tell the
	// server to route client frames to the bridge.
	// The connect/disconnect callbacks print a terminal notice so the operator
	// sees when a browser gains or loses remote access to this session (§7.3).
	// They run on the server's WebSocket goroutine and only write a line, so they
	// don't block.
	cfg := remotecontrol.Config{
		OnClientConnect: func(remoteAddr string) {
			fmt.Fprintf(out, "\n[remote-control] browser connected from %s\n", remoteAddr)
		},
		OnClientDisconnect: func() {
			fmt.Fprintf(out, "\n[remote-control] browser disconnected\n")
		},
	}
	srv := remotecontrol.NewServer(cfg, nil)
	bridge := remotecontrol.NewBridge(srv)
	srv.SetHandler(bridge)

	url, err := srv.Start()
	if err != nil {
		fmt.Fprintf(out, "remote control: %v\n", err)
		return
	}
	rs := &remoteSession{server: srv, bridge: bridge, url: url}
	deps.remote = rs
	if deps.tee != nil {
		deps.tee.setSecondary(bridge.OutputWriter())
	}

	fmt.Fprintf(out, "\nRemote control started. Open this URL on a device on the same network:\n\n  %s\n\n", url)
	if qr, qerr := remotecontrol.Render(url); qerr == nil {
		fmt.Fprintln(out, qr)
	}
	fmt.Fprintln(out, "Run /remote-control stop to end the session.")
}

func stopRemoteControl(out io.Writer, deps *replDeps) {
	if deps.remote == nil {
		fmt.Fprintln(out, "remote control is not running")
		return
	}
	if deps.tee != nil {
		deps.tee.setSecondary(nil)
	}
	_ = deps.remote.server.Stop(context.Background())
	deps.remote = nil
	fmt.Fprintln(out, "remote control stopped")
}

func remoteControlStatus(out io.Writer, deps *replDeps) {
	if deps.remote == nil {
		fmt.Fprintln(out, "remote control: off")
		return
	}
	state := "waiting for a browser to connect"
	if deps.remote.hasClient() {
		state = "browser connected"
	}
	fmt.Fprintf(out, "remote control: on (%s)\n  %s\n", state, deps.remote.url)
}

// beforeToolCall builds the tool-call confirmation seam for a turn. It always
// constructs the local stdin prompt (trust.BeforeToolCall) and, when a remote
// session exists, wraps it with bridgeBeforeToolCall so confirmations route to a
// paired browser while one is connected. When deps.remote is nil the wrapper is
// skipped entirely, so the returned func is exactly the local seam — the
// non-remote path is byte-identical to before (#443).
func beforeToolCall(deps replDeps, out io.Writer) agentcore.BeforeToolCallFunc {
	local := trust.BeforeToolCall(deps.trust, deps.cwd, deps.in, out, deps.confirmMu)
	if deps.remote == nil {
		return local
	}
	return bridgeBeforeToolCall(deps.trust, deps.cwd, deps.remote, out, deps.confirmMu, local)
}

// bridgeBeforeToolCall wraps the local stdin confirmation seam so that while a
// browser is connected, side-effect tool-call confirmations are routed to the
// browser instead of blocking on the local terminal. When no browser is
// connected it delegates to the local prompt so behavior is unchanged.
//
// This mirrors trust.BeforeToolCall's gating (side-effect tools only, honoring
// session trust) but delegates the allow/always decision to the remote client
// via Bridge.Confirm. A ctx cancellation (e.g. SIGINT) makes Confirm return
// remote=false, which we treat as a denial so an interrupted run does not
// silently proceed. Refinements (local-answer race, timeouts) are the hardening
// node's job (#445).
func bridgeBeforeToolCall(mgr *trust.Manager, cwd string, rs *remoteSession, out io.Writer, mu *sync.Mutex, local agentcore.BeforeToolCallFunc) agentcore.BeforeToolCallFunc {
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if !rs.hasClient() || mgr == nil {
			if local != nil {
				return local(ctx, call)
			}
			return nil
		}
		if !trust.SideEffectTools[call.Name] {
			return nil
		}
		if mu != nil {
			mu.Lock()
			defer mu.Unlock()
		}
		if mgr.IsTrusted(cwd) {
			return nil
		}
		summary := trust.ToolCallSummary(call)
		fmt.Fprintf(out, "\npigo wants to run %q — approve on the paired device…\n", call.Name)
		d, remote := rs.bridge.Confirm(ctx, call.Name, summary)
		if !remote {
			// Interrupted / cancelled before the browser answered: deny.
			return blockToolCall(call, cwd)
		}
		if d.Always {
			mgr.SetSessionTrust(cwd)
		}
		if !d.Approve {
			return blockToolCall(call, cwd)
		}
		return nil
	}
}

func blockToolCall(call agentcore.AgentToolCall, cwd string) *agentcore.BeforeToolCallDecision {
	msg := fmt.Sprintf("tool %q blocked: %s is not trusted (use /trust to trust this project)", call.Name, cwd)
	return &agentcore.BeforeToolCallDecision{
		Block:   true,
		Content: &agentcore.ContentList{agentcore.NewTextContent(msg)},
	}
}
