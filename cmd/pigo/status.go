// This file implements the /status slash command (US-002, #292) that prints a
// colored multi-section status report with runtime config, context usage, and more.
package main

import (
	"fmt"
	"io"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
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
