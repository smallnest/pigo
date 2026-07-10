// This file implements the headless / stdio run modes (US-020, FR-18): a
// non-interactive driver that runs the agent loop over a single prompt for
// scripting and CI. Two output modes are supported, mirroring pi's print-mode
// and rpc/stream-json protocols:
//
//   - PrintMode: run the loop to completion and write only the final assistant
//     text to the output (the "-p / --print" mode).
//   - StreamJSONMode: serialize every AgentEvent as a line-delimited JSON object
//     as it is emitted (the "--output-format stream-json" mode), so a parent
//     process can consume the run incrementally.
//
// The run's success/failure is reported as a returned error so the CLI can map
// it to a process exit code: a run whose final assistant message carries
// stopReason error/aborted, or whose stream result errors, is a failure.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// HeadlessMode selects how a headless run reports its progress and result.
type HeadlessMode int

const (
	// PrintMode runs the loop to completion and writes only the final assistant
	// text to the output writer.
	PrintMode HeadlessMode = iota
	// StreamJSONMode writes each AgentEvent as a line-delimited JSON object as it
	// is emitted.
	StreamJSONMode
)

// HeadlessConfig configures a headless run.
type HeadlessConfig struct {
	// Run is the loop configuration (provider stream, tools, hooks).
	Run RunConfig
	// Mode selects print vs stream-json output. Defaults to PrintMode.
	Mode HeadlessMode
	// Out receives the run output (final text or JSON lines). Required.
	Out io.Writer
}

// ErrRunFailed is the sentinel returned by RunHeadless when the agent run ended
// in a failure state (stopReason error/aborted). The CLI maps a non-nil error
// to a non-zero exit code.
type ErrRunFailed struct {
	// Reason is the stopReason (or message) that marked the run as failed.
	Reason string
}

func (e *ErrRunFailed) Error() string {
	if e.Reason == "" {
		return "agent run failed"
	}
	return "agent run failed: " + e.Reason
}

// RunHeadless runs the agent loop for the already-assembled agentCtx and drives
// output per cfg.Mode. It blocks until the run ends and returns nil on success
// or an error describing the failure (for exit-code mapping). It never returns
// before the stream is fully drained, so no goroutine is leaked.
func RunHeadless(ctx context.Context, agentCtx *AgentContext, cfg HeadlessConfig) error {
	if cfg.Out == nil {
		return fmt.Errorf("headless: nil output writer")
	}
	stream := agentLoop(ctx, agentCtx, cfg.Run)

	var lastAssistant *AssistantMessage
	// writeErr holds the first stream-json write failure. We keep draining
	// after it so the loop's producer goroutine never blocks on a synchronous
	// (unbuffered) Emit — honoring the no-leak contract even on a broken pipe.
	var writeErr error
	for ev := range stream.Events() {
		if cfg.Mode == StreamJSONMode && writeErr == nil {
			if err := writeEventJSON(cfg.Out, ev); err != nil {
				writeErr = err
			}
		}
		if te, ok := ev.(TurnEndEvent); ok {
			m := te.Message
			lastAssistant = &m
		}
	}
	if writeErr != nil {
		return writeErr
	}

	msgs, resErr := stream.Result(ctx)
	if resErr != nil {
		return resErr
	}
	// Prefer the final assistant message from the result messages, falling back
	// to the last turn_end message observed on the stream.
	if final := lastAssistantOf(msgs); final != nil {
		lastAssistant = final
	}

	if cfg.Mode == PrintMode {
		text := ""
		if lastAssistant != nil {
			text = contentToText(lastAssistant.Content)
		}
		if _, err := io.WriteString(cfg.Out, text); err != nil {
			return err
		}
		if text != "" && !strings.HasSuffix(text, "\n") {
			if _, err := io.WriteString(cfg.Out, "\n"); err != nil {
				return err
			}
		}
	}

	if lastAssistant != nil {
		switch lastAssistant.StopReason {
		case StopReasonError:
			reason := lastAssistant.ErrorMessage
			if reason == "" {
				reason = "error"
			}
			return &ErrRunFailed{Reason: reason}
		case StopReasonAborted:
			return &ErrRunFailed{Reason: "aborted"}
		}
	}
	return nil
}

// lastAssistantOf returns a pointer to the last AssistantMessage in msgs, or nil.
func lastAssistantOf(msgs []AgentMessage) *AssistantMessage {
	for i := len(msgs) - 1; i >= 0; i-- {
		if a, ok := msgs[i].(AssistantMessage); ok {
			return &a
		}
	}
	return nil
}

// writeEventJSON serializes one AgentEvent as a single line of JSON, terminated
// by a newline, onto w. The envelope always carries a "type" discriminant so a
// consumer can dispatch without positional knowledge.
func writeEventJSON(w io.Writer, ev AgentEvent) error {
	env := eventEnvelope(ev)
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("headless: marshal event: %w", err)
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// eventEnvelope maps an AgentEvent onto a JSON-serializable object with a
// "type" discriminant plus the event's observable payload. Only fields that are
// safe and useful over the wire are included (assistant text, tool ids/names,
// stop reasons) — never secrets.
func eventEnvelope(ev AgentEvent) map[string]any {
	env := map[string]any{"type": ev.EventType()}
	switch e := ev.(type) {
	case AgentEndEvent:
		env["messageCount"] = len(e.Messages)
	case TurnEndEvent:
		env["stopReason"] = e.Message.StopReason
		if text := contentToText(e.Message.Content); text != "" {
			env["text"] = text
		}
		if calls := e.Message.ToolCalls(); len(calls) > 0 {
			names := make([]string, len(calls))
			for i, c := range calls {
				names[i] = c.Name
			}
			env["toolCalls"] = names
		}
	case MessageUpdateEvent:
		if a, ok := e.Message.(AssistantMessage); ok {
			if text := contentToText(a.Content); text != "" {
				env["text"] = text
			}
		}
	case ToolExecutionStartEvent:
		env["toolCallId"] = e.ToolCallID
		env["toolName"] = e.ToolName
	case ToolExecutionEndEvent:
		env["toolCallId"] = e.ToolCallID
		env["toolName"] = e.ToolName
		env["isError"] = e.IsError
	}
	return env
}
