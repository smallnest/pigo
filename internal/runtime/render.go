// This file implements DrainStream (architecture deepening ①): the single place
// that consumes a loop EventStream. The streaming-text delta accounting (track
// how many bytes of the current assistant message have been surfaced, emit only
// the new suffix) and the message_update-vs-turn_end dispatch were previously
// hand-rolled in three places — the REPL, the headless driver, and the
// sub-agent tool. Each consumer now supplies callbacks and shares one drain
// loop, so a bug in the delta arithmetic or the dispatch has exactly one home.
package runtime

import (
	"context"

	"github.com/smallnest/pigo/internal/agentcore"
)

// StreamHandler is the set of callbacks DrainStream invokes as it consumes a
// run's events. Every field is optional (nil = ignore that signal). Callbacks
// run on the draining goroutine, in event order.
type StreamHandler struct {
	// OnText receives each new suffix of the streaming assistant text: the bytes
	// produced since the last OnText call for the current turn. The final suffix
	// is flushed at turn end before OnTurnEnd, so a consumer that only implements
	// OnText still sees the complete text.
	OnText func(delta string)
	// OnTurnEnd fires once per completed turn, after the turn's text is fully
	// flushed, carrying the final assistant message and the tool results produced
	// during the turn. Consumers render tool activity here.
	OnTurnEnd func(msg agentcore.AssistantMessage, results []agentcore.ToolResultMessage)
	// OnEvent, when set, receives every raw event before the typed callbacks —
	// used by the stream-json protocol driver, which serialises the whole event.
	OnEvent func(ev agentcore.AgentEvent)
}

// DrainStream consumes stream to completion, invoking h's callbacks, and returns
// the final assistant message (or nil) plus the run's result error. It always
// drains every event even if a callback has side effects that fail, so the
// loop's producer goroutine never blocks on back-pressure (the no-leak
// contract). The final assistant message is taken from the run result when
// available, falling back to the last turn_end message observed on the stream.
func DrainStream(ctx context.Context, stream *LoopEventStream, h StreamHandler) (*agentcore.AssistantMessage, error) {
	// printed tracks how many bytes of the current streaming assistant message
	// have already been surfaced via OnText, so each update emits only the delta.
	printed := 0
	var lastTurn *agentcore.AssistantMessage

	emitText := func(text string) {
		if len(text) > printed {
			if h.OnText != nil {
				h.OnText(text[printed:])
			}
			printed = len(text)
		}
	}

	for ev := range stream.Events() {
		if h.OnEvent != nil {
			h.OnEvent(ev)
		}
		switch e := ev.(type) {
		case agentcore.MessageUpdateEvent:
			if a, ok := e.Message.(agentcore.AssistantMessage); ok {
				emitText(agentcore.ContentToText(a.Content))
			}
		case agentcore.TurnEndEvent:
			// Flush any tail the streaming updates did not cover (covers providers
			// that only deliver the complete message at turn end), then reset for
			// the next turn and hand the turn to the consumer.
			emitText(agentcore.ContentToText(e.Message.Content))
			printed = 0
			m := e.Message
			lastTurn = &m
			if h.OnTurnEnd != nil {
				h.OnTurnEnd(e.Message, e.ToolResults)
			}
		}
	}

	msgs, resErr := stream.Result(ctx)
	if resErr != nil {
		return lastTurn, resErr
	}
	if final := agentcore.LastAssistantOf(msgs); final != nil {
		return final, nil
	}
	return lastTurn, nil
}
