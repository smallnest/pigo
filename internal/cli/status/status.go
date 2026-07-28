// This file implements the /status slash command (US-002, #292) that prints a
// colored multi-section status report with runtime config, context usage, and more.
//
// It reaches the session's collaborators and mutable state through the cli.Host
// contract (like /goal and /btw) rather than importing the concrete replDeps
// aggregate, keeping the dependency single-direction (repl→status).
package status

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/trust"
)

// RunStatus prints a colored multi-section status report to out using data
// read from host through the cli.Host accessors. It shows runtime config,
// context usage, project/environment, credentials, and telemetry.
func RunStatus(out io.Writer, host cli.Host) {
	color := ui.Enabled()

	fmt.Fprintln(out)
	printRuntimeConfig(out, color, host)
	fmt.Fprintln(out)
	printContextStatus(out, color, host)
	fmt.Fprintln(out)
	printEnvStatus(out, color, host)
	fmt.Fprintln(out)
	printCredentialsStatus(out, color, host)
	fmt.Fprintln(out)
	printTelemetryStatus(out, color, host)
}

// printRuntimeConfig prints the runtime model configuration section.
func printRuntimeConfig(out io.Writer, color bool, host cli.Host) {
	live := host.Live()
	header := host.Header()
	model := live.Model
	providerName := live.ProviderName
	baseURL := live.BaseURL
	protocol := live.Protocol
	thinkingLevel := string(live.ThinkingLevel)
	contextWindow := live.ContextWindow

	if model == "" {
		model = header.Model
	}
	if providerName == "" {
		providerName = header.Provider
	}
	if baseURL == "" {
		baseURL = "(default)"
	}
	if protocol == "" {
		protocol = "(default)"
	}
	if thinkingLevel == "" {
		thinkingLevel = "(default)"
	}

	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "runtime config:"))
	fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "model:"), model)
	fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "provider:"), providerName)
	fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "base URL:"), baseURL)
	fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "protocol:"), protocol)
	fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "thinking:"), thinkingLevel)
	if contextWindow > 0 {
		fmt.Fprintf(out, "  %s %d tokens\n", ui.Colorize(color, ui.Dim, "context window:"), contextWindow)
	} else {
		fmt.Fprintf(out, "  %s unknown\n", ui.Colorize(color, ui.Dim, "context window:"))
	}
}

// printContextStatus prints the current context usage and compaction section.
func printContextStatus(out io.Writer, color bool, host cli.Host) {
	msgs := host.AgentCtx().Messages
	tokens := compaction.EstimateContextTokens(msgs).Tokens
	contextWindow := host.Live().ContextWindow

	compactions := 0
	for _, m := range msgs {
		if _, ok := m.(agentcore.CompactionMessage); ok {
			compactions++
		}
	}

	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "context:"))
	fmt.Fprintf(out, "  %s %d / %d tokens\n", ui.Colorize(color, ui.Dim, "current:"), tokens, contextWindow)

	// Calculate utilization percentage if possible
	if contextWindow > 0 {
		utilization := int(float64(tokens) / float64(contextWindow) * 100)
		utilColor := ""
		if utilization >= 90 {
			utilColor = ui.Red
		} else if utilization >= 70 {
			utilColor = ui.Yellow
		} else {
			utilColor = ui.Green
		}
		fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "utilization:"), ui.Colorize(color, utilColor, fmt.Sprintf("%d%%", utilization)))
	} else {
		fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "utilization:"), ui.Colorize(color, ui.Yellow, "unknown"))
	}

	fmt.Fprintf(out, "  %s %d\n", ui.Colorize(color, ui.Dim, "compactions:"), compactions)

	// Calculate remaining tokens before auto-compaction
	if contextWindow > 0 {
		reserve := compaction.DefaultCompactionSettings.ReserveTokens
		threshold := contextWindow - reserve
		remaining := threshold - tokens
		if remaining < 0 {
			fmt.Fprintf(out, "  %s %s (threshold: %d, reserve: %d)\n",
				ui.Colorize(color, ui.Dim, "before compact:"),
				ui.Colorize(color, ui.Red, fmt.Sprintf("%d over threshold", -remaining)),
				threshold,
				reserve,
			)
		} else {
			fmt.Fprintf(out, "  %s %s (threshold: %d, reserve: %d)\n",
				ui.Colorize(color, ui.Dim, "before compact:"),
				ui.Colorize(color, ui.Green, fmt.Sprintf("%d tokens remaining", remaining)),
				threshold,
				reserve,
			)
		}
	} else {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "before compact:"),
			ui.Colorize(color, ui.Yellow, "auto-compaction disabled (unknown window)"),
		)
	}
}

// printEnvStatus prints the project & environment section: cwd, trust status,
// and counts of loaded skills and plugins (with names). User command templates
// are listed separately when present.
func printEnvStatus(out io.Writer, color bool, host cli.Host) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "project & environment:"))
	fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "cwd:"), host.Cwd())
	fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "trust:"), trustStatus(host.Trust(), host.Cwd()))

	var skills, plugins, userCmds []string
	if slash := host.Slash(); slash != nil {
		for _, c := range slash.List() {
			switch c.Source {
			case runtime.SourceSkill:
				skills = append(skills, c.Name)
			case runtime.SourcePlugin:
				plugins = append(plugins, c.Name)
			case runtime.SourceUser:
				userCmds = append(userCmds, c.Name)
			}
		}
	}
	fmt.Fprintf(out, "  %s %d%s\n", ui.Colorize(color, ui.Dim, "skills:"), len(skills), namesSuffix(skills))
	fmt.Fprintf(out, "  %s %d%s\n", ui.Colorize(color, ui.Dim, "plugins:"), len(plugins), namesSuffix(plugins))
	if len(userCmds) > 0 {
		fmt.Fprintf(out, "  %s %d%s\n", ui.Colorize(color, ui.Dim, "user commands:"), len(userCmds), namesSuffix(userCmds))
	}
}

// trustStatus classifies the cwd's trust state for display: disabled when trust
// is off, trusted when IsTrusted is true (session grant or saved Trusted),
// untrusted when a saved Untrusted decision applies, else prompt (undecided).
func trustStatus(mgr *trust.Manager, cwd string) string {
	if mgr == nil {
		return "disabled"
	}
	if mgr.IsTrusted(cwd) {
		return "trusted"
	}
	if res := mgr.NearestTrustDecision(cwd); res.Found && res.Decision == trust.Untrusted {
		return "untrusted"
	}
	return "prompt"
}

// namesSuffix renders " (n1, n2, ...)" for a non-empty name list, capping at 8
// names with "+k more". It returns "" for an empty list.
func namesSuffix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	const max = 8
	if len(names) <= max {
		return " (" + strings.Join(names, ", ") + ")"
	}
	return fmt.Sprintf(" (%s, +%d more)", strings.Join(names[:max], ", "), len(names)-max)
}

// printCredentialsStatus prints the credentials & connectivity section: API key
// presence (masked, never plaintext) and the provider endpoint URL.
func printCredentialsStatus(out io.Writer, color bool, host cli.Host) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "credentials & connectivity:"))
	live := host.Live()
	creds := host.Creds()
	provider := live.ProviderName
	if creds != nil && creds.HasCredential(context.Background(), provider) {
		key := creds.GetAPIKey(context.Background(), provider)
		fmt.Fprintf(out, "  %s %s %s\n",
			ui.Colorize(color, ui.Dim, "api key:"),
			ui.Colorize(color, ui.Green, "set"),
			ui.Colorize(color, ui.Dim, maskKey(key)))
	} else {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "api key:"),
			ui.Colorize(color, ui.Yellow, "not set"))
	}
	endpoint := live.BaseURL
	if endpoint == "" {
		endpoint = "(default)"
	}
	fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "endpoint:"), endpoint)
}

// maskKey returns a masked hint of an API key showing only the last 4 chars
// (e.g. "••••abcd"). A key of 4 chars or fewer is masked entirely. It never
// returns the full key.
func maskKey(key string) string {
	const tail = 4
	r := []rune(key)
	if len(r) <= tail {
		return strings.Repeat("•", len(r))
	}
	return strings.Repeat("•", tail) + string(r[len(r)-tail:])
}

// printTelemetryStatus prints the telemetry section with two sub-blocks -
// cumulative (since session start) and last run - each showing turn count,
// truncation/compaction counts, context utilization, and a per-tool table.
// Both blocks show "no telemetry yet" before any run has completed.
func printTelemetryStatus(out io.Writer, color bool, host cli.Host) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "telemetry:"))
	holder := host.Telemetry()
	if holder == nil || !holder.HasTelemetry() {
		fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "since session start:"), ui.Colorize(color, ui.Dim, "no telemetry yet"))
		fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "last run:"), ui.Colorize(color, ui.Dim, "no telemetry yet"))
		return
	}
	printTelemetryBlock(out, color, "since session start:",
		holder.CumulativeTurns(),
		holder.CumulativeTruncationCount(),
		holder.CumulativeCompactionCount(),
		holder.CumulativeContextUtilization(),
		holder.CumulativeToolDurations(),
	)
	if last := holder.Last(); last != nil {
		printTelemetryBlock(out, color, "last run:",
			last.Turns,
			last.TruncationCount,
			last.CompactionCount,
			last.ContextUtilization,
			last.ToolDurationsMs,
		)
	} else {
		fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "last run:"), ui.Colorize(color, ui.Dim, "no telemetry yet"))
	}
}

// printTelemetryBlock renders one telemetry sub-block (cumulative or last run).
func printTelemetryBlock(out io.Writer, color bool, label string, turns, trunc, compact int, util float64, tools map[string]agentcore.ToolTiming) {
	fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Cyan, label))
	fmt.Fprintf(out, "    %s %d\n", ui.Colorize(color, ui.Dim, "turns:"), turns)
	fmt.Fprintf(out, "    %s %d\n", ui.Colorize(color, ui.Dim, "truncations:"), trunc)
	fmt.Fprintf(out, "    %s %d\n", ui.Colorize(color, ui.Dim, "compactions:"), compact)
	fmt.Fprintf(out, "    %s %s\n", ui.Colorize(color, ui.Dim, "utilization:"), fmt.Sprintf("%.0f%%", util*100))
	if len(tools) == 0 {
		fmt.Fprintf(out, "    %s (none)\n", ui.Colorize(color, ui.Dim, "tools:"))
		return
	}
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(out, "    %s\n", ui.Colorize(color, ui.Dim, "tools:"))
	for _, n := range names {
		t := tools[n]
		fmt.Fprintf(out, "      %-12s %3d calls  %dms\n", n, t.Count, t.TotalMs)
	}
}
