// End-to-end load test for the embedded pi-extension host (#266).
//
// This exercises the REAL host (pihost.mjs) against the REAL pi SDK
// (@earendil-works/pi-coding-agent) via internal/plugin.Load, driving the
// actual newline-delimited JSON-RPC 2.0 handshake over a subprocess. A tiny
// fixture pi extension is written to a temp dir; the test loads it through the
// host, asserts the fixture's registered command shows up in the manifest, and
// asserts commands/call returns the prompt the fixture emits via
// pi.sendUserMessage (which the host captures into CommandCallResult.Prompt).
//
// It is guarded: it skips cleanly when `node` is not on PATH or when the pi SDK
// cannot be resolved, so CI without a Node/SDK toolchain still passes.
package pihost_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/pihost"
	"github.com/smallnest/pigo/internal/plugin"
)

// piSDKProbe mirrors pihost.mjs's own SDK resolution (loadSdk): try a direct
// import of the package, then fall back to importing the package entry under
// the global npm root. Reporting availability the same way the host resolves
// it keeps the gate honest — the test skips only when the host itself could
// not load the SDK.
const piSDKProbe = `
const PKG = "@earendil-works/pi-coding-agent";
import("node:child_process").then(async ({ execFileSync }) => {
  try { await import(PKG); process.exit(0); } catch {}
  try {
    const root = execFileSync("npm", ["root", "-g"], { encoding: "utf8" }).trim();
    const { pathToFileURL } = await import("node:url");
    const path = await import("node:path");
    for (const entry of [
      path.join(root, PKG, "dist", "index.js"),
      path.join(root, PKG, "index.js"),
    ]) {
      try { await import(pathToFileURL(entry).href); process.exit(0); } catch {}
    }
  } catch {}
  process.exit(1);
}).catch(() => process.exit(1));
`

// piHostAvailable reports whether the E2E prerequisites are satisfied, and a
// human-readable reason to log when they are not. It requires `node` on PATH
// and a resolvable pi SDK; the SDK probe is bounded so a hung npm cannot stall
// the suite.
func piHostAvailable() (ok bool, reason string) {
	if _, err := exec.LookPath("node"); err != nil {
		return false, "node is not on PATH"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "-e", piSDKProbe)
	if err := cmd.Run(); err != nil {
		return false, "pi SDK (@earendil-works/pi-coding-agent) is not resolvable: " + err.Error()
	}
	return true, ""
}

// writeFixtureExtension writes a minimal pi extension package into dir: a
// package.json declaring index.js as its sole pi extension, and an index.js
// that registers a single "e2e" command whose handler emits a known string via
// pi.sendUserMessage. The host captures that message into the command result's
// Prompt.
func writeFixtureExtension(t *testing.T, dir string) {
	t.Helper()

	pkgJSON := `{
  "name": "pi-e2e-fixture",
  "version": "0.0.0",
  "type": "module",
  "pi": { "extensions": ["index.js"] }
}
`
	indexJS := `export default (pi) => {
  pi.registerCommand("e2e", {
    description: "e2e probe",
    handler: async (args, ctx) => {
      pi.sendUserMessage("E2E_PROMPT_OK");
    },
  });
};
`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(indexJS), 0o644); err != nil {
		t.Fatalf("write index.js: %v", err)
	}
}

func TestPiHostExtensionE2E(t *testing.T) {
	if ok, reason := piHostAvailable(); !ok {
		t.Skip("skipping pi-host E2E: " + reason)
	}

	// Exercise the EMBEDDED host bytes: write pihost.Script to a temp .mjs so
	// the test drives exactly what pigo ships, not a stray on-disk copy.
	tmp := t.TempDir()
	hostPath := filepath.Join(tmp, "pihost.mjs")
	if err := os.WriteFile(hostPath, pihost.Script, 0o644); err != nil {
		t.Fatalf("write embedded pihost.mjs: %v", err)
	}

	// The fixture extension lives in its own package dir so the host's pkgDir
	// filter (which keeps only extensions under the target dir) selects it.
	pkgDir := filepath.Join(tmp, "fixture")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	writeFixtureExtension(t, pkgDir)

	// Load the extension through the real host. Args mirror the host's argv
	// contract: `node pihost.mjs <pkgDir>`. plugin.Load performs the initialize
	// handshake and decodes the manifest.
	p, err := plugin.Load("node", []string{hostPath, pkgDir}, os.Stderr)
	if err != nil {
		t.Fatalf("plugin.Load(pihost): %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	// initialize must surface the fixture's registered command.
	if got := p.Manifest.Name; got != "pi-e2e-fixture" {
		t.Errorf("manifest name = %q, want %q", got, "pi-e2e-fixture")
	}
	var found bool
	for _, c := range p.Manifest.Commands {
		if c.Name == "e2e" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("manifest commands %+v missing %q", p.Manifest.Commands, "e2e")
	}

	// commands/call must run the handler and return the captured prompt.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := p.CallCommand(ctx, "e2e", json.RawMessage(`""`))
	if err != nil {
		t.Fatalf("CallCommand(e2e): %v", err)
	}
	if res.Prompt != "E2E_PROMPT_OK" {
		t.Fatalf("command prompt = %q (notifications %+v), want %q", res.Prompt, res.Notifications, "E2E_PROMPT_OK")
	}
}
