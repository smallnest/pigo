# Design: pi-extension host for pigo

Date: 2026-07-24
Status: Approved (design), pending implementation

## Problem

`pigo` loads plugins by discovering every executable regular file in
`$PIGO_HOME/plugins` (`internal/plugin.Discover`) and speaking line-delimited
JSON-RPC 2.0 to it: it spawns the executable, calls `initialize`, and expects a
`Manifest{name, version, tools[], commands[]}` back (`internal/plugin.Load`).

`internal/pkgmgr.DistributeExtension` installs an npm **pi extension** by
extracting the package to `plugins/<name>.pkg/` and writing a launcher
`plugins/<name>` that is just `#!/bin/sh\nexec <binAbs> "$@"`, execing the
package's entrypoint directly.

That works for a native binary or a genuine JSON-RPC server, but a pi extension
entrypoint is neither. A pi extension is an ESM module whose default export is a
factory `(pi) => { pi.registerCommand(...); pi.registerTool(...) }`. So the
current launcher fails two ways:

- **pi-simplify** — `dist/index.js` has no shebang, so `/bin/sh` executes JS as
  shell: `syntax error near unexpected token '('`.
- **pi-agent-browser-native** — its `bin` is a real Node script (`config.mjs`)
  but not a JSON-RPC server, so `initialize` never gets answered:
  `jsonrpc: reader stopped: EOF`.

A third symptom, `openai stream error: ... model emitted an undeclared tool
call`, is **downstream**: because the plugins failed to load, their
tools/commands were never registered, yet the model attempted a call. That is a
provider-robustness concern, tracked separately and **out of scope** here.

Additionally, `plugin.Manifest.Commands` is declared in the wire protocol but
**no pigo code consumes it** — nothing turns a plugin-declared command into a
slash command. So even a correctly loaded pi extension would not surface
`/simplify` today. This design fixes both the host and the command wiring.

## Goals

- Run JS pi extensions (`export default (pi) => {...}`) under pigo with faithful
  `pi` API behavior, reusing pi's real runtime (`@earendil-works/pi-coding-agent`).
- Surface a pi extension's `registerTool` tools as pigo agent tools and its
  `registerCommand` commands as pigo slash commands.
- Preserve pigo's existing fault isolation: a broken or missing extension is
  logged and skipped, never fatal.
- Require `node` on PATH for pi extensions; native/JSON-RPC plugins are
  unaffected.

## Non-goals

- Full fidelity of the pi command context (model switching, session
  fork/navigate, custom TUI widgets/dialogs). Initial command support is
  **prompt + notifications only**.
- Bundling a Node runtime. If `node` is absent, pi extensions are skipped with a
  diagnostic.
- Fixing the provider "undeclared tool call" error (#3). Noted, tracked
  elsewhere.
- Windows support for pi extensions (matches the existing
  `DistributeExtension` Windows limitation).

## Architecture

Introduce a **pi-extension host**: a self-contained Node program that pigo
ships, embedded into the Go binary via `go:embed`. The host loads a pi extension
using pi's real runtime and re-exposes it over pigo's existing JSON-RPC plugin
protocol. `internal/plugin.Load` then talks to the host exactly as it talks to
any other plugin — no change to the plugin transport or handshake.

```
pigo (Go)                          node pihost.mjs (ships with pigo)
---------                          --------------------------------
plugin.Discover ──exec launcher──▶ #!/bin/sh
                                   exec node <pihost.mjs> <pkgDir> "$@"
                                        │
plugin.Load ──initialize──────────────▶│ discoverAndLoadExtensions([pkgDir], cwd)
            ◀──Manifest{tools,commands}─│ build ExtensionRunner, collect
                                        │   getAllRegisteredTools() + getRegisteredCommands()
tools/call {name,args} ───────────────▶│ runner tool.execute(...)  → {content,isError}
commands/call {name,args} ─────────────▶│ command.handler(args, capturingCtx)
            ◀──{prompt,notifications}───│   captures sendUserMessage / ui.notify
event / shutdown (notify) ─────────────▶│ runner.emit* / graceful exit
```

Two launcher shapes result from `DistributeExtension`:

- **pi extension** (entrypoint is a JS module from `pi.extensions`, or the
  package declares `pi.extensions`): launcher execs the Node host.
- **native/JSON-RPC plugin** (entrypoint is a binary or self-hosted protocol
  server, i.e. no `pi.extensions`): launcher keeps the current direct-exec form.

## Components

### 1. The Node host — `internal/pihost/pihost.mjs` (embedded)

A single ESM file, `go:embed`ed and written to disk at install time next to the
payload (e.g. `plugins/<name>.pkg/.pihost.mjs`). Responsibilities:

1. Parse argv: `pihost.mjs <pkgDir> [extra args...]`.
2. Import pi's runtime from the public entry `@earendil-works/pi-coding-agent`
   (resolved from the global npm root; see "SDK resolution"). Use the public
   `discoverAndLoadExtensions([pkgDir], cwd)` — it reads the package's
   `pi.extensions` field and loads the declared entrypoints. (`loadExtensions`
   is not a public export; `discoverAndLoadExtensions` is.)
3. Build an `ExtensionRunner` from the load result and `bindCore` /
   `bindCommandContext` with pigo-appropriate action implementations
   (stubs/bridges — see "Action bridging").
4. Collect the manifest:
   - `runner.getAllRegisteredTools()` → `tools[]` (`{name, description, schema}`),
     converting each tool's TypeBox `parameters` to a JSON Schema
     `RawMessage`.
   - `runner.getRegisteredCommands()` → `commands[]`
     (`{name, description, prompt:""}`; the prompt is produced at call time, not
     declared).
5. Serve JSON-RPC 2.0 over stdio (newline-delimited), matching
   `internal/jsonrpc` framing:
   - `initialize` → `{name, version, tools, commands}`. `name` is the package
     name; if multiple extensions load, tools/commands are merged (first
     registration per name wins, mirroring `getAllRegisteredTools`).
   - `tools/call {name, arguments}` → look up the tool, run
     `execute(callId, params, signal, onUpdate, ctx)`, map its
     `AgentToolResult` to `{content, isError}`. `content` is the tool result's
     text; a thrown error or `isError` result maps to `{isError:true}`.
   - `commands/call {name, args}` → run `command.handler(args, cmdCtx)` where
     `cmdCtx` captures `sendUserMessage(content)` and `ui.notify(msg,type)` into
     buffers. Return `{prompt, notifications:[{message,type}]}`. `prompt` is the
     concatenation of captured user messages (empty if none).
   - `event {type, data}` (notification) → `runner.emit(...)` for the matching
     event type, best-effort.
   - `shutdown` (notification) → resolve pending work and `process.exit(0)`.

The host is transport-symmetric with pigo's Go client: read lines from stdin,
write one JSON object + `\n` per response to stdout. Diagnostics go to stderr
(pigo pipes plugin stderr through).

### 2. Action bridging (host side)

`ExtensionRunner.bindCore` / `bindCommandContext` require action
implementations. Since pigo drives the agent loop (not pi), these are stubs or
capture buffers:

- `exec(command, args, opts)` → real child-process exec in the host (pi
  extensions like pi-simplify call `pi.exec("git", ...)`); returns
  `{stdout, stderr, code, killed}`.
- `sendUserMessage(content, opts)` → capture into the current command's prompt
  buffer.
- `ui.notify(message, type)` → capture into the current command's notification
  buffer. Outside a command call, forward as a stderr line.
- `ui.select/confirm/input` → return `undefined`/`false` (no interactive UI in
  this bridge); documented limitation.
- Read-only context (`cwd`, `isIdle`, `isProjectTrusted`, `getModel`, ...) →
  sensible constants: `cwd` = process cwd (pigo launches the host in the
  session cwd), `isIdle` = true, `isProjectTrusted` = true (pigo already gates
  trust before running tools).
- Session/model mutation (`setModel`, `newSession`, `fork`, `navigateTree`,
  `compact`, ...) → no-op stubs. A command relying on these degrades gracefully
  rather than crashing.
- `registerProvider` / `getFlag` / renderers / shortcuts / widgets → accepted
  but inert (recorded, not acted on).

Any action a factory calls that is genuinely unsupported must **not throw** —
the goal is that loading and basic tool/command use never crash the host.

### 3. SDK resolution (host side)

The host must import `@earendil-works/pi-coding-agent` without assuming pigo's
plugin dir is inside a node_modules tree. Resolution order:

1. `import(...)` directly (works if the package is resolvable from the host
   file's location or NODE_PATH).
2. Fall back to the global npm root: run `npm root -g` (or read `NODE_PATH`),
   then import the absolute path to the package's entry.

If resolution fails, the host writes a clear stderr message and exits non-zero
before answering `initialize`, so `plugin.Load` logs and skips it.

The peer-dependency floor observed in installed extensions is `>=0.74.0`; the
locally installed SDK is `0.80.3`. The host targets the public API surface
(`discoverAndLoadExtensions`, `ExtensionRunner`, `createEventBus`,
`createExtensionRuntime`), which is stable across that range.

### 4. pigo-side: distribution — `internal/pkgmgr/distribute.go`

`DistributeExtension` gains a branch on entrypoint kind:

- Determine whether the resolved entrypoint is a **pi extension**: true when the
  package.json has a non-empty `pi.extensions`, OR the resolved bin ends in
  `.js`/`.mjs`/`.cjs` and is an ESM module (no protocol shebang). The
  `pi.extensions` signal is authoritative and checked first.
- **pi extension** → write the embedded host to `plugins/<name>.pkg/.pihost.mjs`
  (recorded in the created-files list for uninstall), and write the launcher as:
  ```sh
  #!/bin/sh
  exec node '<.pihost.mjs abs>' '<pkgDir abs>' "$@"
  ```
- **otherwise** → keep the current direct-exec launcher.

The chosen shell quoting (`shellQuote`) is reused. `node` is assumed on PATH;
if absent at run time the exec fails and the plugin is skipped (fault-isolated),
with a readable stderr hint emitted by a small guard in the launcher.

### 5. pigo-side: plugin command support — `internal/plugin`

- `manifest.go`: add `CommandCallParams{Name, Args}` and
  `CommandCallResult{Prompt string, Notifications []CommandNotification}` where
  `CommandNotification{Message, Type string}`.
- `plugin.go`: add `Plugin.CallCommand(ctx, name, args) (CommandCallResult,
  error)` that RPCs `commands/call`. Transport errors degrade to an error the
  caller can surface, never a panic (mirrors `pluginTool.Execute`).
- Add `Manager.Commands()` returning the aggregated
  `[]struct{Plugin *Plugin; Spec CommandSpec}` (or a small `PluginCommand`
  type) across loaded plugins, in load order.

### 6. pigo-side: slash registration — `cmd/pigo/interactive.go`

After plugin discovery (both interactive and, where applicable, headless),
register each plugin command as a `runtime.SlashCommand`:

- `Name` = command name, `Description` = spec description, source = user-level
  (built-ins still win on name collision, matching existing precedence).
- `Action(args)`: call `Plugin.CallCommand`; print each notification to the
  user; if a non-empty `Prompt` is returned, deliver it as the next agent turn.

  Note: the current `SlashCommand` split is `Expand` (pure prompt producer) vs
  `Action` (side effect, no run). A plugin command needs both a side effect (the
  RPC + notifications) *and* to start a run with the returned prompt. The
  cleanest fit is a small extension to the REPL's slash handling: allow an
  `Action` to *also* return prompt text to run, or add a third command kind
  (`SlashPluginCommand`) carrying a closure that returns `(prompt, message)`.
  The plan will pick one; the design constraint is: **run the returned prompt as
  a normal turn, after printing notifications.** For the headless path (no REPL
  turn injection), plugin commands are surfaced but invoking one prints its
  notifications and, if a prompt is returned, appends it as the prompt for that
  run.

## Data flow (example: `/simplify`)

1. User types `/simplify` in the REPL.
2. Slash registry resolves it to the plugin command action.
3. `Plugin.CallCommand(ctx, "simplify", "")` → host `commands/call`.
4. Host runs pi-simplify's handler: it calls `pi.exec("git", ["diff", ...])`
   (bridged to real exec) to find changed files, builds a prompt, and calls
   `pi.sendUserMessage(prompt, {deliverAs:"followUp"})` (captured).
5. Host returns `{prompt:<simplify prompt>, notifications:[]}` (or a "no changed
   files" notification and empty prompt).
6. pigo prints notifications; if a prompt is present, runs it as the next turn.

## Error handling

- **Extension fails to load / SDK missing / node missing**: host exits non-zero
  before/at `initialize`; `plugin.Load` logs `plugin "<name>" failed to load` and
  skips it. Other plugins still load. This is the existing isolation path.
- **Tool call crashes the host**: `pluginTool.Execute` already converts a
  transport error into an error result; unchanged.
- **Command call fails**: `CallCommand` returns an error; the slash action
  prints a readable message and does not start a run.
- **Malformed args from the model**: TypeBox validation in the pi tool
  `execute` rejects; mapped to `{isError:true}` content.

## Testing

- `internal/pkgmgr/distribute_test.go`: assert the pi-extension launcher execs
  `node <pihost> <pkgDir>` and drops `.pihost.mjs`; assert a binary-bin package
  keeps the direct-exec launcher; assert both record their created files.
- `internal/plugin`: unit-test `CallCommand` and `Manager.Commands()` against a
  scripted fake plugin (the existing `manager_test.go` pattern with a shell/Go
  fixture speaking the protocol) — no Node needed.
- End-to-end (guarded, skipped when `node` or the SDK is absent): a tiny fixture
  extension `export default (pi) => pi.registerCommand("t", {handler: async(a,c)=>{ pi... }})`
  loaded through the real host + `plugin.Load`, asserting `initialize` returns
  the command and `commands/call` returns the expected prompt.
- `cmd/pigo`: test that a discovered plugin command is registered in the slash
  registry and that invoking it injects the returned prompt.

## Open questions / assumptions

- **SlashCommand shape**: whether to extend `Action` to optionally return a
  prompt-to-run or add a dedicated plugin-command kind. Resolved in the plan;
  either satisfies "print notifications, then run the returned prompt".
- **Host placement**: per-package `.pihost.mjs` (simple, self-contained,
  uninstall-clean) vs one shared `plugins/.pihost/pihost.mjs`. Default:
  per-package copy, since it keeps the uninstall lockfile exact and avoids a
  shared-file lifecycle. Revisit if size becomes a concern.
- **`node_modules` for extensions**: assumed pre-bundled (as noted in
  issue#0158). The host does not run `npm install`. An extension needing
  unbundled deps beyond the pi SDK is out of scope.
