package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/provider"
)

// TestPrintProviderHelp verifies the --help "Supported providers" block is
// derived from the registry: every built-in provider name, its env vars, and
// its protocol must appear, so the docs cannot silently drift from the code.
func TestPrintProviderHelp(t *testing.T) {
	var buf bytes.Buffer
	printProviderHelp(&buf)
	out := buf.String()

	for _, spec := range provider.ProviderSpecs() {
		if !strings.Contains(out, spec.Name+":") {
			t.Errorf("help output missing provider %q", spec.Name)
		}
		for _, env := range spec.EnvVars {
			if !strings.Contains(out, env) {
				t.Errorf("help output missing env var %q for provider %q", env, spec.Name)
			}
		}
		if !strings.Contains(out, "["+spec.Protocol+"]") {
			t.Errorf("help output missing protocol %q for provider %q", spec.Protocol, spec.Name)
		}
	}
}
