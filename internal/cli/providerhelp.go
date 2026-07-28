// This file holds ProviderHelp, the "--help" provider block moved from cmd/pigo
// (US-004, #361). It enumerates the built-in provider registry so the values
// accepted by --provider (and their env vars / default base URLs / protocols)
// stay in sync with the code rather than being hand-maintained.
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/smallnest/pigo/internal/provider"
)

// PrintProviderHelp writes the "Supported providers" block appended to `--help`
// output. It enumerates the built-in provider registry so the list of values
// accepted by --provider (and their env vars / default base URLs / protocols)
// stays in sync with the code rather than being hand-maintained.
func PrintProviderHelp(w io.Writer) {
	fmt.Fprintf(w, "\nSupported --provider names (name: ENV_VARS -> default base URL [protocol]):\n")
	for _, spec := range provider.ProviderSpecs() {
		base := spec.DefaultBaseURL
		if strings.TrimSpace(base) == "" {
			base = "(composed from env)"
		}
		fmt.Fprintf(w, "  %s: %s -> %s [%s]\n",
			spec.Name, strings.Join(spec.EnvVars, ", "), base, spec.Protocol)
	}
	fmt.Fprintf(w, "\nBase URL override precedence: --base-url > <provider>-specific *_BASE_URL env > generic <PROVIDER>_BASE_URL env > registry default.\n")
	fmt.Fprintf(w, "API key env fallback: any provider also accepts the generic <PROVIDER>_API_KEY convention.\n")
}
