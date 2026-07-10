// This file wires the interactive TUI (US-022) into the pigo command. When the
// command is invoked without a prompt and stdout is a terminal, pigo starts the
// bubbletea interactive loop instead of the headless print path.
package main

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agent"
	"github.com/smallnest/pigo/internal/tui"
)

// runInteractive starts the bubbletea TUI. Each submitted prompt launches an
// agent run whose events are streamed into the UI; queued steering text is
// injected between turns via the loop's GetSteeringMessages hook.
func runInteractive(model, providerName string, provider agent.Provider, tools []agent.AgentTool, sysPrompt string) error {
	creds := agent.NewCredentialStore(nil)
	reg := toolRegistry(tools)

	run := func(ctx context.Context, prompt string, steering func() []string) (*agent.LoopEventStream, context.CancelFunc) {
		runCtx, cancel := context.WithCancel(ctx)
		agentCtx := &agent.AgentContext{
			SystemPrompt: sysPrompt,
			Messages: agent.MessageList{
				agent.UserMessage{RoleField: agent.RoleUser, Content: agent.ContentList{agent.NewTextContent(prompt)}},
			},
			Tools: tools,
		}
		cfg := agent.RunConfig{
			LoopConfig: agent.LoopConfig{
				Model:     model,
				Provider:  providerName,
				Stream:    agent.StreamFnFromProvider(provider),
				GetAPIKey: creds.GetAPIKey,
			},
			Batch: agent.BatchConfig{
				ToolExecutorConfig: agent.ToolExecutorConfig{Registry: reg},
			},
			// Bridge the TUI's steering queue into the loop's per-turn hook.
			GetSteeringMessages: func(context.Context) []agent.AgentMessage {
				texts := steering()
				if len(texts) == 0 {
					return nil
				}
				msgs := make([]agent.AgentMessage, 0, len(texts))
				for _, t := range texts {
					msgs = append(msgs, agent.UserMessage{RoleField: agent.RoleUser, Content: agent.ContentList{agent.NewTextContent(t)}})
				}
				return msgs
			},
		}
		return agent.StartRun(runCtx, agentCtx, cfg), cancel
	}

	m := tui.NewModel(run)
	p := tea.NewProgram(m)
	m.SetProgram(p)
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// stdoutIsTerminal reports whether stdout is an interactive terminal (not a
// pipe/file), used to decide print vs interactive mode.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
