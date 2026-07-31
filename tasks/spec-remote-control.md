# SPEC: Remote Control（手机端远程控制 CLI 会话）

> Technical specification derived from: `tasks/prd-remote-control.md`
> Generated: 2026-07-31 | Target branch: master | Commit: ad9c8b5

## 1. Summary

### 1.1 What This SPEC Covers
This SPEC specifies how to add a `/remote-control` built-in slash command that starts an in-process WebSocket-based web server, letting a phone on the same LAN mirror the CLI session output, inject prompts, and approve/reject risky tool calls. It covers the server, the pairing/auth flow, the REPL input/output seam that bridges terminal and remote, and lifecycle cleanup. It does not cover cloud relay, PWA, or multi-client control (see Non-Goals in the PRD).

### 1.2 PRD Reference
- Source: `tasks/prd-remote-control.md`
- User Stories covered: US-001 … US-008
- Functional Requirements covered: FR-1 … FR-19

### 1.3 Design Decisions Summary
| Decision | Choice | Rationale |
|----------|--------|-----------|
| Real-time channel | WebSocket via `github.com/coder/websocket` | User chose 1B; coder/websocket is context-native, zero transitive deps, idiomatic with the repo's `context`-driven loops |
| QR rendering | `github.com/skip2/go-qrcode` + custom Unicode half-block renderer | User chose 2A; pure-Go, pinned, terminal-scannable |
| REPL bridging | New `RemoteBridge` seam injected into `replDeps`: merged input source + `io.MultiWriter` output tee + confirmation router | User chose 3A; keeps the synchronous main-goroutine loop intact, minimizes blast radius |
| Code location | New `internal/remotecontrol` package; command registered via `AddBuiltin` at REPL assembly | User chose 4A; `AddBuiltin` closure can capture live bridge/session state |
| Auth model | One-time high-entropy pairing token in URL → server-issued session cookie | PRD FR-6/FR-8; token single-use + TTL, cookie for subsequent requests |
| Concurrency model | HTTP server on its own goroutine; communicates with the synchronous REPL loop over channels; bridge state guarded by a mutex | The REPL is a single-goroutine loop reading a shared `bufio.Reader`; remote input must be marshaled onto that loop without a data race |

---

## 2. Architecture

### 2.1 System Context
```
┌─────────────────────────── pigo process ───────────────────────────┐
│                                                                     │
│  main goroutine: REPL loop (repl.go)                                │
│    reads input ◀── inputSource (stdin ∪ remote) ── bufio.Reader     │
│    writes out  ──▶ io.MultiWriter(stdout, remoteSink)               │
│    confirm     ◀─▶ ConfirmRouter (terminal or remote)               │
│         ▲                                                           │
│         │ channels (lines in, frames out, approvals)                │
│         ▼                                                           │
│  server goroutine: internal/remotecontrol.Server (net/http + ws)    │
│    GET  /            → static SPA (embedded)                        │
│    GET  /pair?t=…    → validate token, set session cookie           │
│    WS   /ws          → bidirectional: output frames / input / approve│
└─────────────────────────────────────────────────────────────────────┘
             ▲ LAN (http://<lan-ip>:<port>)
             │
        📱 phone browser (responsive SPA)
```

### 2.2 Component Design
New package `internal/remotecontrol`:

- `Server` — owns the `*http.Server`, listener, token store, connected client, and the channels to the REPL. Lifecycle: `Start(ctx) (url string, err error)`, `Stop()`.
- `Bridge` — the REPL-facing seam. Implements:
  - `InputSource`: a reader the REPL loop consults; yields remote-submitted lines interleaved with local stdin.
  - `OutputSink`: an `io.Writer` the server subscribes to (fed via the `io.MultiWriter` tee).
  - `ConfirmRouter`: given a pending tool-call confirmation, decides terminal vs remote and returns `(allow, always)`.
- `tokenStore` — generates/validates one-time, TTL-bound pairing tokens and issued session credentials (in-memory only).
- `qr` — renders a URL as a Unicode half-block QR block for the terminal.
- Embedded SPA assets (`//go:embed web/*`).

`Bridge` deliberately does not change how the REPL runs a turn; it only changes where input comes from, where output goes, and who answers confirmations.

### 2.3 Module Interactions
```
/remote-control (AddBuiltin Action)
  → remotecontrol.NewServer(bridge, cfg)
  → server.Start(ctx)  ── binds LAN addr, picks port
  → returns URL + token; command prints URL + QR (qr.Render)

REPL turn (streamRun):
  agent output ── out (MultiWriter) ──▶ stdout
                                    └─▶ bridge.OutputSink ──▶ ws broadcast

  trust.BeforeToolCall ── ConfirmToolCall ──▶ bridge.ConfirmRouter
     remote client connected? ──yes──▶ send "confirm" frame, block on approval chan
                              ──no───▶ fall back to terminal ConfirmToolCall

remote client sends "input" frame:
  server ── bridge.inputCh ──▶ InputSource.Read unblocks main loop with the line
```

### 2.4 File Structure
```
internal/
├── remotecontrol/
│   ├── server.go            [NEW]  http.Server, routes, lifecycle
│   ├── server_test.go       [NEW]
│   ├── bridge.go            [NEW]  InputSource / OutputSink / ConfirmRouter
│   ├── bridge_test.go       [NEW]
│   ├── token.go             [NEW]  pairing token + session credential store
│   ├── token_test.go        [NEW]
│   ├── qr.go                [NEW]  Unicode QR renderer (wraps skip2/go-qrcode)
│   ├── qr_test.go           [NEW]
│   ├── lanaddr.go           [NEW]  pick a routable LAN IP + free port
│   ├── lanaddr_test.go      [NEW]
│   ├── protocol.go          [NEW]  ws frame types (shared with SPA contract)
│   └── web/                 [NEW]  embedded responsive SPA
│       ├── index.html
│       ├── app.js
│       └── style.css
├── cli/repl/
│   ├── repl.go              [MODIFY] wire Bridge into replDeps: input source,
│   │                                 MultiWriter out, confirm router
│   └── remote_command.go    [NEW]  AddBuiltin("/remote-control") assembly
└── trust/
    └── interactive.go       [MODIFY] route ConfirmToolCall through ConfirmRouter
```

---

## 3. Data Model

No database. All state is in-memory, process-scoped, and destroyed on exit.

### 3.1 Schema Changes
None.

### 3.2 Entity Definitions
```go
// protocol.go — WebSocket frame contract (JSON), shared with the SPA.
type FrameType string

const (
    FrameOutput  FrameType = "output"  // server→client: streamed session text
    FrameInput   FrameType = "input"   // client→server: a submitted prompt line
    FrameConfirm FrameType = "confirm" // server→client: risky-op approval request
    FrameDecide  FrameType = "decide"  // client→server: approve/reject
    FrameStatus  FrameType = "status"  // server→client: connected/ended/disconnected
)

type Frame struct {
    Type FrameType `json:"type"`
    // Output
    Text string `json:"text,omitempty"`
    // Confirm
    ConfirmID string `json:"confirmId,omitempty"`
    Tool      string `json:"tool,omitempty"`
    Summary   string `json:"summary,omitempty"`
    // Decide
    Approve bool `json:"approve,omitempty"`
    Always  bool `json:"always,omitempty"`
    // Status
    State string `json:"state,omitempty"` // "connected" | "ended" | "disconnected"
}

// token.go
type pairingToken struct {
    value     string    // 32 bytes hex (256-bit)
    expiresAt time.Time
    used      bool
}

type Config struct {
    PairTTL        time.Duration // default 10m  (PRD [Assumption])
    Host           string        // resolved LAN IP; "" → auto-detect
    Port           int           // 0 → auto-pick, fall back on conflict
    ConfirmTimeout time.Duration // 0 → wait forever (Open Question OQ-1 default)
}
```

### 3.3 Relationships
The `Bridge` holds a pointer to the active `*Server`; the `Server` holds a single `clientConn` (nil until paired). `replDeps` gains one field: `remote *remotecontrol.Bridge` (nil when remote control is not running).

### 3.4 Migration Plan
No data migration. Additive-only: when `deps.remote == nil`, all code paths behave exactly as today (stdin reader, plain stdout, terminal confirm).

---

## 4. API Design

### 4.1 Endpoints

| Method | Path | Description | Auth | Request | Response |
|--------|------|-------------|------|---------|----------|
| GET | `/` | Serve responsive SPA | session cookie; if absent redirect to `/pair` behavior | — | `text/html` |
| GET | `/pair?t=<token>` | Validate one-time token, issue session cookie, redirect to `/` | one-time token | query `t` | 302 + `Set-Cookie`; 401 on invalid/expired/used |
| GET | `/ws` | Upgrade to WebSocket for bidirectional frames | session cookie | WS upgrade | WS stream of `Frame` JSON |
| GET | `/healthz` | Liveness (internal) | none | — | 200 `ok` |

### 4.2 Request/Response Schemas
- Pairing URL printed to terminal: `http://<lan-ip>:<port>/pair?t=<64-hex-chars>`.
- Session cookie: `pigo_rc=<opaque 256-bit>; HttpOnly; SameSite=Strict; Path=/`. (No `Secure` flag: LAN is plain HTTP; documented in §7.)
- WebSocket messages: `Frame` JSON (§3.2). Server→client: `output`, `confirm`, `status`. Client→server: `input`, `decide`.
- Input frame validation: `Text` trimmed; empty rejected server-side (mirrors SPA disabling send).

### 4.3 Error Responses
| Condition | HTTP | Body |
|-----------|------|------|
| Missing/invalid/expired/used pairing token | 401 | HTML "pairing link invalid or expired" |
| Missing session cookie on `/` or `/ws` | 302 → informative page (not auto-repair; token is one-time) | — |
| Session already ended (server stopped) | 410 | HTML "session ended" |
| WS upgrade without cookie | 401 | — |

### 4.4 Breaking Changes
None. Purely additive; no existing endpoint or CLI behavior changes when the command is not invoked.

---

## 5. Business Logic

### 5.1 Core Algorithms

**Start sequence (US-001, US-002, US-003, US-004 · FR-1..FR-9)**
```
on /remote-control [stop]:
  if arg == "stop":
     if bridge.server running: server.Stop(); return "remote control stopped"
     else: return "remote control not running"
  if bridge.server running:
     return "remote control already running at " + currentURL   // FR: repeat call
  host = cfg.Host or lanaddr.DetectRoutableIP()                  // FR-2
  ln, port = lanaddr.ListenFreePort(host, cfg.Port)              // FR-3 fallback loop
  token = tokenStore.NewPairing(cfg.PairTTL)                     // FR-6, FR-7
  server.Start(ctx, ln)                                          // FR-2
  url = "http://" + host + ":" + port + "/pair?t=" + token.value // FR-4
  print url; print qr.Render(url)                                // FR-4, FR-5
  return ""                                                      // non-blocking (Action returns immediately)
```

**Pairing (FR-6..FR-9)**
```
GET /pair?t=T:
  tok = tokenStore.Lookup(T)
  if tok == nil or tok.used or now > tok.expiresAt: 401           // FR-9
  tok.used = true                                                 // one-time
  cred = tokenStore.IssueSession()                                // FR-8
  Set-Cookie pigo_rc=cred; 302 → "/"
```

**Output mirroring (US-005 · FR-10, FR-17)**
```
REPL out = io.MultiWriter(stdout, bridge.OutputSink)
bridge.OutputSink.Write(p):
   append to ring buffer (for late-join replay of current turn)
   if client connected: client.Send(Frame{Output, string(p)})
```

**Remote input injection (US-006 · FR-12, FR-17)**
```
InputSource.Read merges two sources onto the main loop:
   local stdin lines (existing bufio.Reader) AND bridge.inputCh
Implementation: the REPL line editor's ReadLine is fed by a select over
   {stdin-line chan, bridge.inputCh}; whichever arrives first returns.
On remote line L:
   echo L into out (so terminal + remote both see it)   // FR-12 echo
   return L to the loop as if typed → runs a normal turn // FR-17 same session
```

**Risky-op confirmation routing (US-007 · FR-13..FR-16)**
```
trust.ConfirmToolCall now calls bridge.ConfirmRouter(call):
  if bridge != nil and client connected:
     id = newID()
     client.Send(Frame{Confirm, id, call.Name, summary})       // FR-13
     select {
       case d := <-approvalCh[id]:   return d.Approve, d.Always // FR-14/15
       case <-confirmTimeout:        // OQ-1: default = no timeout (wait)
     }
     mirror decision to terminal out: "remote approved/rejected" // FR-16
  else:
     fall back to terminal prompt (existing code)               // unchanged
```

**Shutdown (US-008 · FR-18, FR-19)**
```
on CLI exit (defer in runREPL) OR /remote-control stop:
  server.Send(Frame{Status,"ended"}); close WS; http.Server.Shutdown(ctx)
  tokenStore.Clear(); release listener                          // FR-18
```

### 5.2 Validation Rules
- Pairing token: 256-bit, `crypto/rand`, hex-encoded; single-use; TTL default 10m.
- Session credential: 256-bit, `crypto/rand`; compared with `subtle.ConstantTimeCompare`.
- Input frame `Text`: reject empty/whitespace-only server-side.
- Port auto-pick: try `cfg.Port`, then bind `:0` if occupied (kernel-assigned free port).

### 5.3 State Machine
Server states: `idle → listening → paired → (ended)`.
- `listening`: server up, no client, valid pairing token outstanding.
- `paired`: cookie issued, WS may connect/reconnect within the same process session.
- `ended`: `Stop()` called or process exiting; all requests → 410 / closed WS.

Confirmation states per request: `pending → approved | rejected | (superseded-by-terminal on disconnect)`.

### 5.4 Edge Cases
- Client disconnects mid-confirmation → router falls back to terminal prompt for that pending request; new WS frames stop.
- Multiple browsers hit `/pair` → only the first consumes the one-time token; others 401. Single active client (PRD Non-Goal); a second cookie-bearing WS is refused with `status: "disconnected"` reason "another client active".
- No routable non-loopback interface found → command fails with a clear message (do not silently bind loopback, which the phone can't reach).
- SIGINT during a remote-run turn → existing cancel path applies; server stays up; terminal and remote both return to prompt.
- Very long output bursts → `OutputSink` is non-blocking; if the WS send buffer is full, drop-to-latest is NOT acceptable for a terminal mirror, so use a bounded queue with backpressure and coalescing of adjacent `output` frames.

---

## 6. Error Handling

### 6.1 Error Taxonomy
| Error Code | HTTP Status | Condition | User Message |
|------------|-------------|-----------|--------------|
| `rc_token_invalid` | 401 | token missing/expired/used | "pairing link invalid or expired" |
| `rc_no_cookie` | 302 | no session cookie | info page "open the pairing link from your terminal" |
| `rc_ended` | 410 | server stopped | "session ended" |
| `rc_client_busy` | ws close | second client while one active | "another device is controlling this session" |
| `rc_no_lan` | (CLI, not HTTP) | no routable IP | "no LAN address found; are you connected to Wi‑Fi?" |

### 6.2 Retry Strategy
- WS client auto-reconnects with capped backoff (SPA side) while the cookie is valid and server is `paired`; server treats reconnect as the same client.
- Pairing token is never retried (one-time by design); user re-runs `/remote-control`.

### 6.3 Failure Modes
- Server fails to start (port/interface) → `/remote-control` returns an error string; REPL continues normally (no remote).
- WS drops → REPL keeps running against stdout/terminal; confirmations fall back to terminal (§5.1).
- go-qrcode failure → print URL only, warn once (graceful degradation, satisfies FR-4 even if FR-5 degrades).

---

## 7. Security

### 7.1 Authentication & Authorization
- Access gated by one-time, TTL-bound pairing token → HttpOnly session cookie (FR-6/FR-8/FR-9). No fixed password (PRD Non-Goal).
- Single active controlling client (PRD Non-Goal: no multi-client).
- Remote approval of a risky op is treated as **equivalent to a local approval**; it does not grant persistent project trust unless `Always` is chosen (mirrors terminal `[a]lways`).

### 7.2 Input Validation
- All frames JSON-decoded into typed `Frame`; unknown types ignored. Reject oversized frames (cap message size, e.g. 64 KiB) to prevent memory abuse.
- Remote-injected prompt text is passed to the same turn pipeline as terminal input — it is subject to the same trust gating on any tools it triggers (so a remote prompt can't bypass confirmation).

### 7.3 Data Protection
- Plain HTTP over LAN (no TLS); cookie lacks `Secure`. **Security note:** anyone on the same LAN who obtains the one-time link before it's used, or who steals the cookie via a MITM on the local network, could control the session. This is an unauthenticated-transport tradeoff of the LAN-only design — flagged explicitly; TLS/relay is out of scope per PRD.
- Tokens/credentials live only in memory and are zeroed/cleared on `Stop()`; never logged, never written to the session file.
- Optional (recommend): print a one-line terminal notice when a client pairs/connects, so the operator notices unexpected access (addresses PRD Open Question about visibility).

---

## 8. Performance

### 8.1 Expected Load
Single user, single phone. Traffic = one session's streamed text + occasional input/approval frames. Negligible QPS; latency-sensitive, not throughput-sensitive.

### 8.2 Optimization Strategy
- Coalesce adjacent `output` writes into batched frames (flush on ~16–32ms tick or buffer threshold) to hit the PRD <1s (target «100ms) end-to-end latency without per-byte frames.
- Ring buffer of the current turn's output for late WS join/reconnect replay (bounded, e.g. last 256 KiB).

### 8.3 Database Considerations
N/A.

---

## 9. Testing Strategy

### 9.1 Unit Tests
- `token_test.go`: token is 256-bit, single-use, expires after TTL; session credential constant-time compare.
- `lanaddr_test.go`: picks a non-loopback routable IP; free-port fallback when a port is occupied; error when no routable interface.
- `qr_test.go`: renders deterministic Unicode block for a known URL; degrades to URL-only on error.
- `bridge_test.go`: `OutputSink` tees to stdout + sink; empty remote input rejected; `ConfirmRouter` falls back to terminal when no client.

### 9.2 Integration Tests
- `server_test.go` (httptest + real WS dial via coder/websocket):
  - `/pair` with valid token sets cookie + 302; invalid/expired/used → 401.
  - `/ws` without cookie → 401; with cookie → receives `output` frames.
  - client sends `input` frame → bridge delivers the line to a fake REPL input consumer.
  - `confirm` round-trip: server sends `confirm`, client `decide{approve:true}` → router returns `(true,false)` and terminal mirror line written.
  - second client rejected while one active.
  - `Stop()` closes WS with `ended` and subsequent requests 410.
- REPL wiring test (`internal/cli/repl`): with `deps.remote == nil`, behavior byte-identical to today (regression guard for additive design).

### 9.3 Edge Case Tests
- Client disconnect during pending confirmation → terminal fallback engaged.
- No routable IP (inject a fake interface lister) → command returns `rc_no_lan` message, REPL unaffected.
- Output backpressure: fast producer, slow consumer → frames coalesced, no goroutine leak, no dropped bytes.

### 9.4 Acceptance Criteria Mapping
| US/FR | Test | Type | Description |
|-------|------|------|-------------|
| US-001 / FR-1 | repl remote_command_test | unit | `/remote-control` recognized, non-blocking, repeat-call message |
| US-002 / FR-2,FR-3 | lanaddr_test, server_test | unit+integration | LAN bind + free-port fallback |
| US-003 / FR-4,FR-5 | qr_test, remote_command_test | unit | URL + Unicode QR printed |
| US-004 / FR-6..FR-9 | token_test, server_test | unit+integration | one-time TTL token, cookie issue, 401 paths |
| US-005 / FR-10,FR-17 | bridge_test, server_test | unit+integration | output tee + WS stream |
| US-006 / FR-12 | server_test, bridge_test | integration | input frame injected + echoed |
| US-007 / FR-13..FR-16 | server_test | integration | confirm round-trip + terminal mirror |
| US-008 / FR-18,FR-19 | server_test | integration | stop + exit cleanup, 410 on stale URL |

---

## 10. Implementation Plan

### 10.1 Phases
1. **Foundations** (no REPL changes): `protocol.go`, `token.go`, `lanaddr.go`, `qr.go` + tests. Add deps `coder/websocket`, `skip2/go-qrcode`.
2. **Server**: `server.go` routes, pairing, WS, single-client, `Start/Stop` + `server_test.go`.
3. **Bridge seam**: `bridge.go` (InputSource/OutputSink/ConfirmRouter) + tests, no REPL wiring yet.
4. **REPL wiring**: `remote_command.go` (`AddBuiltin`), `repl.go` MultiWriter + merged input + exit cleanup; `trust/interactive.go` confirm routing.
5. **SPA**: embedded responsive `web/` (output view, input box, confirm modal, status bar, reconnect).
6. **Hardening**: backpressure/coalescing, connect notice, docs.

### 10.2 Issue Mapping
| Issue | SPEC Sections | Priority | Depends On |
|-------|--------------|----------|------------|
| #A | 3.2, 5.2 (token) | high | — |
| #B | 2.2, 5.1 (lanaddr) | high | — |
| #C | 2.2, 6.3 (qr) | medium | — |
| #D | 4.x, 5.1, 5.3 (server+ws) | high | #A, #B |
| #E | 2.2, 5.1 (bridge) | high | #A |
| #F | 2.4 repl.go, 5.1 (wiring + confirm) | high | #D, #E |
| #G | 2.4 web/ (SPA) | high | #D |
| #H | 5.4, 8.2, 7.3 (hardening) | medium | #F, #G |

### 10.3 Incremental Delivery
Feature is inert until `/remote-control` is invoked, so it can merge behind no flag. Ship phases 1–4 to get a URL-only, output+input working path first; SPA confirm modal (phase 5) and hardening (phase 6) can follow. The `deps.remote == nil` regression test guarantees zero impact on existing sessions at every phase.

---

## 11. Open Questions & Risks

### 11.1 Unresolved Questions
- **OQ-1 (from PRD):** Confirmation timeout when the phone doesn't respond. SPEC default = **wait forever** (`ConfirmTimeout=0`), with terminal fallback on disconnect. Confirm whether a timeout-then-terminal policy is preferred.
- **OQ-2:** Pairing token TTL default of 10m — acceptable?
- **OQ-3:** Should pairing/connection print a visible terminal notice by default (recommended in §7.3) or be opt-in?

### 11.2 Technical Risks
| Risk | Impact | Mitigation |
|------|--------|-----------|
| Plain-HTTP LAN transport (no TLS) | Session hijack on hostile LAN | One-time short-TTL token, single client, HttpOnly cookie, connect notice; document limitation; relay/TLS deferred |
| Merging remote input into a synchronous stdin loop | Data race / input interleaving | Channel `select` in the line editor on the single main goroutine; no shared mutable read state; regression test for `remote==nil` |
| Output backpressure on slow phone | Lag or memory growth | Bounded coalescing queue + ring-buffer replay; no unbounded buffering |
| New deps (`coder/websocket`, `skip2/go-qrcode`) in a minimal-dep repo | Supply-chain / maintenance | Pin exact versions; both are small, popular, permissively licensed; QR is optional-degradable |
| `go 1.27rc1` toolchain | Dep compatibility | Both deps support modern Go; verify `go build` in phase 1 |

### 11.3 Assumptions
- Phone and computer are on the same L2/L3-reachable LAN with client isolation off.
- A single routable non-loopback IPv4 is sufficient for the printed URL (multi-NIC: pick first routable; may need override via `Config.Host`).
- The existing `trust.ConfirmToolCall` is the only confirmation surface that needs remote routing (no other blocking stdin prompts occur mid-turn).
