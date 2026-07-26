// This file implements the /status slash command (US-002, #292) that prints a
// colored multi-section status report with runtime config, context usage, and more.
package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/trust"
)

// runStatus prints a colored multi-section status report to out using data
// from deps. It shows runtime config, context usage, and (later) credentials
// and telemetry.
func runStatus(out io.Writer, deps *replDeps) {
	color := colorEnabled()

	fmt.Fprintln(out)
	printRuntimeConfig(out, color, deps)
	fmt.Fprintln(out)
	printContextStatus(out, color, deps)
	fmt.Fprintln(out)
	printEnvStatus(out, color, deps)
	fmt.Fprintln(out)
	printCredentialsStatus(out, color, deps)
	fmt.Fprintln(out)
	printTelemetryStatus(out, color, deps)
}

// printRuntimeConfig prints the runtime model configuration section.
func printRuntimeConfig(out io.Writer, color bool, deps *replDeps) {
	model := deps.live.model
	providerName := deps.live.providerName
	baseURL := deps.live.baseURL
	protocol := deps.live.protocol
	thinkingLevel := string(deps.live.thinkingLevel)
	contextWindow := deps.live.contextWindow

	if model == "" {
		model = deps.header.Model
	}
	if providerName == "" {
		providerName = deps.header.Provider
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

	fmt.Fprintf(out, "%s\n", colorize(color, ansiBold, "runtime config:"))
	fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "model:"), model)
	fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "provider:"), providerName)
	fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "base URL:"), baseURL)
	fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "protocol:"), protocol)
	fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "thinking:"), thinkingLevel)
	if contextWindow > 0 {
		fmt.Fprintf(out, "  %s %d tokens\n", colorize(color, ansiDim, "context window:"), contextWindow)
	} else {
		fmt.Fprintf(out, "  %s unknown\n", colorize(color, ansiDim, "context window:"))
	}
}

// printContextStatus prints the current context usage and compaction section.
func printContextStatus(out io.Writer, color bool, deps *replDeps) {
	msgs := deps.agentCtx.Messages
	tokens := compaction.EstimateContextTokens(msgs).Tokens
	contextWindow := deps.live.contextWindow

	compactions := 0
	for _, m := range msgs {
		if _, ok := m.(agentcore.CompactionMessage); ok {
			compactions++
		}
	}

	fmt.Fprintf(out, "%s\n", colorize(color, ansiBold, "context:"))
	fmt.Fprintf(out, "  %s %d / %d tokens\n", colorize(color, ansiDim, "current:"), tokens, contextWindow)

	// Calculate utilization percentage if possible
	if contextWindow > 0 {
		utilization := int(float64(tokens) / float64(contextWindow) * 100)
		utilColor := ""
		if utilization >= 90 {
			utilColor = ansiRed
		} else if utilization >= 70 {
			utilColor = ansiYellow
		} else {
			utilColor = ansiGreen
		}
		fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "utilization:"), colorize(color, utilColor, fmt.Sprintf("%d%%", utilization)))
	} else {
		fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "utilization:"), colorize(color, ansiYellow, "unknown"))
	}

	fmt.Fprintf(out, "  %s %d\n", colorize(color, ansiDim, "compactions:"), compactions)

	// Calculate remaining tokens before auto-compaction
	if contextWindow > 0 {
		reserve := compaction.DefaultCompactionSettings.ReserveTokens
		threshold := contextWindow - reserve
		remaining := threshold - tokens
		if remaining < 0 {
			fmt.Fprintf(out, "  %s %s (threshold: %d, reserve: %d)\n",
				colorize(color, ansiDim, "before compact:"),
				colorize(color, ansiRed, fmt.Sprintf("%d over threshold", -remaining)),
				threshold,
				reserve,
			)
		} else {
			fmt.Fprintf(out, "  %s %s (threshold: %d, reserve: %d)\n",
				colorize(color, ansiDim, "before compact:"),
				colorize(color, ansiGreen, fmt.Sprintf("%d tokens remaining", remaining)),
				threshold,
				reserve,
			)
		}
	} else {
		fmt.Fprintf(out, "  %s %s\n",
			colorize(color, ansiDim, "before compact:"),
			colorize(color, ansiYellow, "auto-compaction disabled (unknown window)"),
		)
	}
}

// printEnvStatus prints the project & environment section: cwd, trust status,
// and counts of loaded skills and plugins (with names). User command templates
// are listed separately when present.
func printEnvStatus(out io.Writer, color bool, deps *replDeps) {
	fmt.Fprintf(out, "%s\n", colorize(color, ansiBold, "project & environment:"))
	fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "cwd:"), deps.cwd)
	fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "trust:"), trustStatus(deps.trust, deps.cwd))

	var skills, plugins, userCmds []string
	if deps.slash != nil {
		for _, c := range deps.slash.List() {
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
	fmt.Fprintf(out, "  %s %d%s\n", colorize(color, ansiDim, "skills:"), len(skills), namesSuffix(skills))
	fmt.Fprintf(out, "  %s %d%s\n", colorize(color, ansiDim, "plugins:"), len(plugins), namesSuffix(plugins))
	if len(userCmds) > 0 {
		fmt.Fprintf(out, "  %s %d%s\n", colorize(color, ansiDim, "user commands:"), len(userCmds), namesSuffix(userCmds))
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
func printCredentialsStatus(out io.Writer, color bool, deps *replDeps) {
	fmt.Fprintf(out, "%s\n", colorize(color, ansiBold, "credentials & connectivity:"))
	provider := deps.live.providerName
	if deps.creds != nil && deps.creds.HasCredential(context.Background(), provider) {
		key := deps.creds.GetAPIKey(context.Background(), provider)
		fmt.Fprintf(out, "  %s %s %s\n",
			colorize(color, ansiDim, "api key:"),
			colorize(color, ansiGreen, "set"),
			colorize(color, ansiDim, maskKey(key)))
	} else {
		fmt.Fprintf(out, "  %s %s\n",
			colorize(color, ansiDim, "api key:"),
			colorize(color, ansiYellow, "not set"))
	}
	endpoint := deps.live.baseURL
	if endpoint == "" {
		endpoint = "(default)"
	}
	fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "endpoint:"), endpoint)
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
func printTelemetryStatus(out io.Writer, color bool, deps *replDeps) {
	fmt.Fprintf(out, "%s\n", colorize(color, ansiBold, "telemetry:"))
	holder := deps.telemetry
	if holder == nil || !holder.HasTelemetry() {
		fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "since session start:"), colorize(color, ansiDim, "no telemetry yet"))
		fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "last run:"), colorize(color, ansiDim, "no telemetry yet"))
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
		fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiDim, "last run:"), colorize(color, ansiDim, "no telemetry yet"))
	}
}

// printTelemetryBlock renders one telemetry sub-block (cumulative or last run).
func printTelemetryBlock(out io.Writer, color bool, label string, turns, trunc, compact int, util float64, tools map[string]agentcore.ToolTiming) {
	fmt.Fprintf(out, "  %s\n", colorize(color, ansiCyan, label))
	fmt.Fprintf(out, "    %s %d\n", colorize(color, ansiDim, "turns:"), turns)
	fmt.Fprintf(out, "    %s %d\n", colorize(color, ansiDim, "truncations:"), trunc)
	fmt.Fprintf(out, "    %s %d\n", colorize(color, ansiDim, "compactions:"), compact)
	fmt.Fprintf(out, "    %s %s\n", colorize(color, ansiDim, "utilization:"), fmt.Sprintf("%.0f%%", util*100))
	if len(tools) == 0 {
		fmt.Fprintf(out, "    %s (none)\n", colorize(color, ansiDim, "tools:"))
		return
	}
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(out, "    %s\n", colorize(color, ansiDim, "tools:"))
	for _, n := range names {
		t := tools[n]
		fmt.Fprintf(out, "      %-12s %3d calls  %dms\n", n, t.Count, t.TotalMs)
	}
}
