package remotecontrol

import (
	"io/fs"
	"strings"
	"testing"
)

// TestSPAAssetsEmbedded verifies the browser SPA is compiled into the binary
// and exposes the files the server serves.
func TestSPAAssetsEmbedded(t *testing.T) {
	sub, err := fs.Sub(spaFiles, "web")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	for _, name := range []string{"index.html", "app.js"} {
		b, err := fs.ReadFile(sub, name)
		if err != nil {
			t.Fatalf("embedded %s missing: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("embedded %s is empty", name)
		}
	}
}

func TestSPAIndexReferencesApp(t *testing.T) {
	b, err := fs.ReadFile(spaFiles, "web/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	html := string(b)
	for _, want := range []string{`id="output"`, `id="composer"`, `id="confirm"`, "app.js", "viewport"} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html missing %q", want)
		}
	}
}

func TestSPAScriptHandlesFrames(t *testing.T) {
	b, err := fs.ReadFile(spaFiles, "web/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	js := string(b)
	// The client must understand every server->client frame type and emit the
	// two client->server types.
	for _, want := range []string{`"output"`, `"confirm"`, `"status"`, `type: "input"`, `type: "decide"`, "/ws"} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing handling for %q", want)
		}
	}
}
