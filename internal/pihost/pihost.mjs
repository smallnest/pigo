#!/usr/bin/env node
// pihost.mjs — pigo's embedded pi-extension host.
//
// pigo ships this single ESM file (embedded via go:embed, see #264) and writes
// it to disk next to an installed pi extension. pigo launches it as an ordinary
// plugin subprocess and speaks line-delimited JSON-RPC 2.0 over its stdio,
// exactly as it speaks to any other plugin (see internal/plugin, internal/jsonrpc).
//
// The host loads a pi extension using pi's real runtime
// (@earendil-works/pi-coding-agent) and re-exposes its registered tools and
// commands over pigo's plugin protocol:
//
//   initialize                    -> Manifest {name, version, tools[], commands[]}
//   tools/call {name, arguments}  -> {content, isError}
//   commands/call {name, arguments}
//                                 -> {prompt, notifications:[{message,type}]}
//   event {type, data} (notify)   -> best-effort runner.emit
//   shutdown (notify)             -> graceful exit
//
// Design invariant: pi actions that pigo does not drive (session/model mutation,
// interactive UI, provider registration, widgets, ...) are inert no-ops that
// NEVER throw. Loading and basic tool/command use must not crash the host.
//
// Argv contract:  node pihost.mjs <pkgDir> [extra args...]
// Launch cwd:     the session cwd (pigo launches the host there).
//
// See docs/superpowers/specs/2026-07-24-pi-extension-host-design.md (§1-§3).

import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import * as path from "node:path";
import { pathToFileURL } from "node:url";

const PKG = "@earendil-works/pi-coding-agent";

// ---------------------------------------------------------------------------
// Diagnostics
// ---------------------------------------------------------------------------

/** Write one diagnostic line to stderr (pigo pipes plugin stderr through). */
function diag(msg) {
  try {
    process.stderr.write(`pihost: ${msg}\n`);
  } catch {
    // stderr unavailable — nothing more we can do.
  }
}

/** Exit non-zero after a clear diagnostic, before any initialize is answered. */
function fatal(msg) {
  diag(msg);
  process.exit(1);
}

// ---------------------------------------------------------------------------
// SDK resolution (§3)
//
// Import the pi runtime from the public entry without assuming the host file is
// inside a node_modules tree:
//   1. import(PKG) directly (works via NODE_PATH or a local node_modules).
//   2. Fall back to the global npm root (`npm root -g`), then import the
//      absolute path to the package entry.
// On failure: clear stderr diagnostic + non-zero exit, BEFORE answering
// initialize, so plugin.Load logs and skips this plugin (fault isolation).
// ---------------------------------------------------------------------------

/** Run `npm root -g` and return the trimmed path, or "" on any failure. */
function npmRootGlobal() {
  return new Promise((resolve) => {
    let out = "";
    let done = false;
    const finish = (v) => {
      if (!done) {
        done = true;
        resolve(v);
      }
    };
    try {
      const child = spawn("npm", ["root", "-g"], { stdio: ["ignore", "pipe", "ignore"] });
      child.stdout.on("data", (d) => {
        out += d.toString();
      });
      child.on("error", () => finish(""));
      child.on("close", (code) => finish(code === 0 ? out.trim() : ""));
      // Bound the probe so a hung npm cannot stall startup.
      setTimeout(() => {
        try {
          child.kill();
        } catch {
          // ignore
        }
        finish("");
      }, 5000);
    } catch {
      finish("");
    }
  });
}

/** Resolve and import the pi SDK. Returns the module namespace. */
async function loadSdk() {
  // 1. Direct import (NODE_PATH / local node_modules).
  try {
    return await import(PKG);
  } catch (errDirect) {
    // 2. Global npm root.
    const candidates = [];
    const envRoots = (process.env.NODE_PATH || "")
      .split(path.delimiter)
      .filter(Boolean);
    const globalRoot = await npmRootGlobal();
    if (globalRoot) envRoots.unshift(globalRoot);
    for (const root of envRoots) {
      candidates.push(path.join(root, PKG, "dist", "index.js"));
      candidates.push(path.join(root, PKG, "index.js"));
    }
    for (const entry of candidates) {
      try {
        return await import(pathToFileURL(entry).href);
      } catch {
        // try next candidate
      }
    }
    const detail = errDirect instanceof Error ? errDirect.message : String(errDirect);
    fatal(
      `cannot resolve ${PKG}. Tried direct import and global npm root ` +
        `(${globalRoot || "unavailable"}). Is the pi SDK installed? (${detail})`,
    );
  }
}

// ---------------------------------------------------------------------------
// TypeBox parameters -> JSON Schema
//
// A pi tool's `parameters` is a TypeBox TSchema, which is already a plain
// JSON-Schema-shaped object annotated with TypeBox symbol keys ([Kind], etc.).
// JSON.stringify drops symbol-keyed properties, so a round-trip yields a clean
// JSON Schema object. Degrade to a permissive object schema when absent or
// unserializable so tool registration on the pigo side never fails.
// ---------------------------------------------------------------------------
function toJsonSchema(parameters) {
  if (parameters && typeof parameters === "object") {
    try {
      const cleaned = JSON.parse(JSON.stringify(parameters));
      if (cleaned && typeof cleaned === "object") {
        if (!cleaned.type) cleaned.type = "object";
        return cleaned;
      }
    } catch {
      // fall through to permissive default
    }
  }
  return { type: "object" };
}

// ---------------------------------------------------------------------------
// Content mapping: pi AgentToolResult.content -> plain text
// ---------------------------------------------------------------------------
function contentToText(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  const parts = [];
  for (const c of content) {
    if (typeof c === "string") {
      parts.push(c);
    } else if (c && typeof c === "object") {
      if (typeof c.text === "string") {
        parts.push(c.text);
      } else if (c.type === "image") {
        parts.push("[image]");
      }
    }
  }
  return parts.join("");
}

// ---------------------------------------------------------------------------
// Command-call capture buffers (§2)
//
// While a commands/call handler runs, pi.sendUserMessage and pi.ui.notify are
// captured into the *current* capture. Outside a command call, ui.notify is
// forwarded to stderr and sendUserMessage is dropped (there is no turn to feed).
// ---------------------------------------------------------------------------
let currentCapture = null; // { prompts: string[], notifications: [{message,type}] }

function captureUserMessage(content) {
  if (!currentCapture) return;
  currentCapture.prompts.push(contentToText(content));
}

function captureNotify(message, type) {
  const msg = typeof message === "string" ? message : String(message ?? "");
  const t = typeof type === "string" && type ? type : "info";
  if (currentCapture) {
    currentCapture.notifications.push({ message: msg, type: t });
  } else {
    diag(`notify[${t}]: ${msg}`);
  }
}

// ---------------------------------------------------------------------------
// Action bridging (§2)
//
// pigo drives the agent loop, not pi, so these actions are stubs or capture
// buffers. Read-only context values are sensible constants. Mutation actions
// and interactive UI are inert. NOTHING throws.
// ---------------------------------------------------------------------------

/** UI context: notify captures/forwards; interactive prompts resolve inert. */
function makeUIContext() {
  return {
    select: async () => undefined,
    confirm: async () => false,
    input: async () => undefined,
    notify: (message, type) => captureNotify(message, type),
    onTerminalInput: () => () => {},
    setStatus: () => {},
    setWorkingMessage: () => {},
    setWorkingVisible: () => {},
    setWorkingIndicator: () => {},
    setHiddenThinkingLabel: () => {},
    setWidget: () => {},
    setFooter: () => {},
    setHeader: () => {},
    setTitle: () => {},
    custom: async () => undefined,
    pasteToEditor: () => {},
    setEditorText: () => {},
    getEditorText: () => "",
    editor: async () => undefined,
    addAutocompleteProvider: () => {},
    setEditorComponent: () => {},
    getEditorComponent: () => undefined,
    theme: undefined,
    getAllThemes: () => [],
    getTheme: () => undefined,
    setTheme: () => ({ success: false }),
    getToolsExpanded: () => false,
    setToolsExpanded: () => {},
  };
}

/** ExtensionActions: pi.* action methods. */
function makeActions() {
  return {
    sendMessage: () => {},
    sendUserMessage: (content) => captureUserMessage(content),
    appendEntry: () => {},
    setSessionName: () => {},
    getSessionName: () => undefined,
    setLabel: () => {},
    getActiveTools: () => [],
    getAllTools: () => [],
    setActiveTools: () => {},
    refreshTools: () => {},
    getCommands: () => [],
    setModel: async () => false,
    getThinkingLevel: () => "off",
    setThinkingLevel: () => {},
  };
}

/** ExtensionContextActions: ctx.* in event/tool handlers. Read-only + inert. */
function makeContextActions() {
  return {
    getModel: () => undefined,
    isIdle: () => true,
    isProjectTrusted: () => true,
    getSignal: () => undefined,
    abort: () => {},
    hasPendingMessages: () => false,
    shutdown: () => {},
    getContextUsage: () => undefined,
    compact: () => {},
    getSystemPrompt: () => "",
    getSystemPromptOptions: () => ({ cwd: process.cwd() }),
  };
}

// bindCommandContext(undefined) installs safe no-op stubs for the mutation
// handlers (newSession/fork/navigateTree/switchSession/reload/waitForIdle),
// which is exactly the inert behavior we want — see runner.js bindCommandContext.

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 framing (matches internal/jsonrpc)
//
// Read newline-delimited JSON from stdin; write one JSON object + "\n" per
// response to stdout. A request has an id; a notification omits it.
// ---------------------------------------------------------------------------
function writeMessage(obj) {
  try {
    process.stdout.write(JSON.stringify(obj) + "\n");
  } catch (err) {
    diag(`failed to write response: ${err instanceof Error ? err.message : String(err)}`);
  }
}

function writeResult(id, result) {
  writeMessage({ jsonrpc: "2.0", id, result });
}

function writeError(id, code, message) {
  writeMessage({ jsonrpc: "2.0", id, error: { code, message } });
}

// ---------------------------------------------------------------------------
// Host
// ---------------------------------------------------------------------------
async function main() {
  const pkgDir = process.argv[2];
  if (!pkgDir) {
    fatal("usage: node pihost.mjs <pkgDir> [extra args...]");
  }
  const absPkgDir = path.resolve(pkgDir);
  const cwd = process.cwd();

  // --- Resolve the SDK (exits non-zero before initialize on failure). ---
  const sdk = await loadSdk();
  const {
    discoverAndLoadExtensions,
    ExtensionRunner,
    createEventBus,
  } = sdk;
  if (
    typeof discoverAndLoadExtensions !== "function" ||
    typeof ExtensionRunner !== "function"
  ) {
    fatal(
      `${PKG} loaded but its public API is missing expected exports ` +
        `(discoverAndLoadExtensions/ExtensionRunner). SDK version mismatch?`,
    );
  }

  // --- Load the extension(s) from the package dir via the public loader. ---
  // discoverAndLoadExtensions reads the package's pi.extensions and loads the
  // declared entrypoints. Pass an event bus so the runtime is fully wired.
  const eventBus = typeof createEventBus === "function" ? createEventBus() : undefined;
  let loadResult;
  try {
    loadResult = await discoverAndLoadExtensions([absPkgDir], cwd, undefined, eventBus);
  } catch (err) {
    fatal(
      `failed to load extension from ${absPkgDir}: ` +
        `${err instanceof Error ? err.message : String(err)}`,
    );
  }
  if (loadResult.errors && loadResult.errors.length > 0) {
    for (const e of loadResult.errors) {
      diag(`extension load error (${e.path}): ${e.error}`);
    }
  }

  // discoverAndLoadExtensions ALSO scans standard locations (cwd/.pi/extensions
  // and the global agent dir), which would merge unrelated extensions into this
  // package's manifest. This host re-exposes exactly one installed package, so
  // keep only the extensions that live under the target package directory.
  const pkgPrefix = absPkgDir + path.sep;
  const extensions = (loadResult.extensions || []).filter((ext) => {
    const rp = ext && typeof ext.resolvedPath === "string" ? path.resolve(ext.resolvedPath) : "";
    return rp === absPkgDir || rp.startsWith(pkgPrefix);
  });
  if (extensions.length === 0) {
    fatal(`no pi extensions loaded from ${absPkgDir}`);
  }

  // --- Build the runner and bind pigo-appropriate action bridges. ---
  // sessionManager/modelRegistry are only touched by mutation paths pigo never
  // drives; the runner tolerates minimal stand-ins for tool/command use.
  let runner;
  try {
    runner = new ExtensionRunner(
      extensions,
      loadResult.runtime,
      cwd,
      /* sessionManager */ undefined,
      /* modelRegistry  */ { registerProvider: () => {}, unregisterProvider: () => {} },
    );
    runner.bindCore(makeActions(), makeContextActions(), {
      registerProvider: () => {},
      unregisterProvider: () => {},
    });
    runner.bindCommandContext(undefined);
    runner.setUIContext(makeUIContext(), "print");
    // Swallow extension-side errors instead of letting them surface as crashes.
    if (typeof runner.onError === "function") {
      runner.onError((e) => diag(`extension error [${e.event}] ${e.extensionPath}: ${e.error}`));
    }
  } catch (err) {
    fatal(
      `failed to initialize extension runner: ` +
        `${err instanceof Error ? err.message : String(err)}`,
    );
  }

  // --- Derive the manifest name from the package.json (fallback: dir name). ---
  let pkgName = path.basename(absPkgDir);
  let pkgVersion = "";
  try {
    const { readFileSync } = await import("node:fs");
    const pkg = JSON.parse(readFileSync(path.join(absPkgDir, "package.json"), "utf-8"));
    if (pkg && typeof pkg.name === "string" && pkg.name) pkgName = pkg.name;
    if (pkg && typeof pkg.version === "string") pkgVersion = pkg.version;
  } catch {
    // no package.json / unreadable — keep the directory-name fallback.
  }

  buildManifestAndServe(runner, pkgName, pkgVersion);
}

/** Collect the manifest, then serve JSON-RPC over stdio. */
function buildManifestAndServe(runner, pkgName, pkgVersion) {
  // Tools: {name, description, schema(JSON Schema from TypeBox parameters)}.
  const registeredTools = safeCall(() => runner.getAllRegisteredTools(), []);
  const tools = [];
  const toolDefsByName = new Map();
  for (const rt of registeredTools) {
    const def = rt && rt.definition ? rt.definition : rt;
    if (!def || typeof def.name !== "string") continue;
    toolDefsByName.set(def.name, def);
    tools.push({
      name: def.name,
      description: typeof def.description === "string" ? def.description : "",
      schema: toJsonSchema(def.parameters),
    });
  }

  // Commands: {name, description, prompt:""}. The prompt is produced at call
  // time (from captured sendUserMessage), not declared here.
  const registeredCommands = safeCall(() => runner.getRegisteredCommands(), []);
  const commands = [];
  const commandsByName = new Map();
  for (const rc of registeredCommands) {
    // resolveRegisteredCommands returns objects with an invocationName; prefer
    // it (it disambiguates duplicate names) and fall back to name.
    const invName = rc && (rc.invocationName || rc.name);
    if (!invName || typeof rc.handler !== "function") continue;
    if (commandsByName.has(invName)) continue; // first registration wins
    commandsByName.set(invName, rc);
    commands.push({
      name: invName,
      description: typeof rc.description === "string" ? rc.description : "",
      prompt: "",
    });
  }

  const manifest = { name: pkgName, version: pkgVersion, tools, commands };

  serve(runner, manifest, toolDefsByName, commandsByName);
}

/** Call fn, returning fallback (and logging) on any throw. */
function safeCall(fn, fallback) {
  try {
    return fn();
  } catch (err) {
    diag(`recovered from error: ${err instanceof Error ? err.message : String(err)}`);
    return fallback;
  }
}

/** Serve JSON-RPC 2.0 requests over stdin/stdout until EOF or shutdown. */
function serve(runner, manifest, toolDefsByName, commandsByName) {
  const rl = createInterface({ input: process.stdin, crlfDelay: Infinity });

  rl.on("line", (line) => {
    const trimmed = line.trim();
    if (!trimmed) return;
    let msg;
    try {
      msg = JSON.parse(trimmed);
    } catch {
      // Unparseable line: cannot correlate an id, so ignore it (never throw).
      diag("ignoring unparseable input line");
      return;
    }
    // Handle asynchronously; a rejected handler must never crash the host.
    handleMessage(msg, runner, manifest, toolDefsByName, commandsByName).catch((err) => {
      const id = msg && msg.id !== undefined ? msg.id : null;
      diag(`handler error: ${err instanceof Error ? err.message : String(err)}`);
      if (id !== null && id !== undefined) {
        writeError(id, -32603, "internal error");
      }
    });
  });

  rl.on("close", () => {
    // stdin EOF: pigo closed the pipe. Exit cleanly.
    process.exit(0);
  });
}

/** Dispatch one decoded JSON-RPC message. */
async function handleMessage(msg, runner, manifest, toolDefsByName, commandsByName) {
  const { id, method, params } = msg || {};
  const isNotification = id === undefined || id === null;

  switch (method) {
    case "initialize": {
      if (!isNotification) writeResult(id, manifest);
      return;
    }

    case "tools/call": {
      const result = await callTool(runner, toolDefsByName, params || {});
      if (!isNotification) writeResult(id, result);
      return;
    }

    case "commands/call": {
      const result = await callCommand(runner, commandsByName, params || {});
      if (!isNotification) writeResult(id, result);
      return;
    }

    case "event": {
      // Best-effort lifecycle event delivery; one-way, never throws.
      await deliverEvent(runner, params || {});
      return;
    }

    case "shutdown": {
      // Graceful shutdown. Resolve then exit.
      safeCall(() => runner.shutdown && runner.shutdown(), undefined);
      process.exit(0);
      return;
    }

    default: {
      // Unsupported method. Reply with an error for requests; ignore for
      // notifications. Never throws.
      if (!isNotification) {
        writeError(id, -32601, `method not found: ${String(method)}`);
      } else {
        diag(`ignoring unsupported notification: ${String(method)}`);
      }
      return;
    }
  }
}

/**
 * Run a pi tool's execute() and map AgentToolResult -> {content, isError}.
 * A thrown error, an isError result, or a missing tool -> {isError:true}.
 */
async function callTool(runner, toolDefsByName, params) {
  const name = params && params.name;
  const args = params && params.arguments !== undefined ? params.arguments : {};
  const def = name ? toolDefsByName.get(name) : undefined;
  if (!def || typeof def.execute !== "function") {
    return { content: `unknown tool: ${String(name)}`, isError: true };
  }

  const ctx = safeCall(() => runner.createContext(), undefined);
  const toolCallId = `pihost-${Date.now()}`;
  try {
    const result = await def.execute(toolCallId, args, undefined, undefined, ctx);
    const content = contentToText(result && result.content);
    // A pi tool signals failure by throwing; some also set details.isError.
    const isError = Boolean(
      result && (result.isError === true || (result.details && result.details.isError === true)),
    );
    return { content, isError };
  } catch (err) {
    return {
      content: `${name}: ${err instanceof Error ? err.message : String(err)}`,
      isError: true,
    };
  }
}

/**
 * Run a pi command's handler with a capturing context and return
 * {prompt, notifications}. prompt = concatenation of captured user messages
 * (empty if none). A thrown handler -> a notification + whatever was captured.
 */
async function callCommand(runner, commandsByName, params) {
  const name = params && params.name;
  const rc = name ? commandsByName.get(name) : undefined;
  if (!rc || typeof rc.handler !== "function") {
    return {
      prompt: "",
      notifications: [{ message: `unknown command: ${String(name)}`, type: "error" }],
    };
  }

  // Arguments: the plugin protocol passes free-form args as raw JSON under
  // "arguments". pi command handlers expect a string (the text after the slash
  // command). Coerce: use a string directly, else JSON-encode non-empty values.
  let argStr = "";
  const rawArgs = params ? params.arguments : undefined;
  if (typeof rawArgs === "string") {
    argStr = rawArgs;
  } else if (rawArgs !== undefined && rawArgs !== null) {
    try {
      argStr = JSON.stringify(rawArgs);
    } catch {
      argStr = "";
    }
  }

  const capture = { prompts: [], notifications: [] };
  const previous = currentCapture;
  currentCapture = capture;
  const ctx = safeCall(() => runner.createCommandContext(), undefined);
  try {
    await rc.handler(argStr, ctx);
  } catch (err) {
    capture.notifications.push({
      message: `${name}: ${err instanceof Error ? err.message : String(err)}`,
      type: "error",
    });
  } finally {
    currentCapture = previous;
  }

  return {
    prompt: capture.prompts.join(""),
    notifications: capture.notifications,
  };
}

/** Best-effort delivery of a lifecycle event to the runner. Never throws. */
async function deliverEvent(runner, params) {
  const type = params && params.type;
  if (!type || typeof runner.emit !== "function") return;
  let data;
  if (params.data !== undefined) {
    // data arrives as raw JSON (already parsed by JSON.parse of the line).
    data = params.data;
  }
  const event = data && typeof data === "object" ? { type, ...data } : { type };
  try {
    await runner.emit(event);
  } catch (err) {
    diag(`event ${type} delivery failed: ${err instanceof Error ? err.message : String(err)}`);
  }
}

main().catch((err) => {
  fatal(`fatal: ${err instanceof Error ? err.stack || err.message : String(err)}`);
});
