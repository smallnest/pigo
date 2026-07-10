// This file implements the Anthropic Messages API streaming decoder (US-008).
// It is a stateful Decoder (see transport.go) that translates Anthropic SSE
// event payloads into the provider-agnostic AssistantMessageEvent set,
// accumulating a partial AssistantMessage as deltas arrive.
//
// Anthropic streams a fixed event sequence:
//
//	message_start        → seeds id/model and initial usage (input tokens)
//	content_block_start  → opens a text / thinking / tool_use block at an index
//	content_block_delta  → text_delta / thinking_delta / signature_delta /
//	                       input_json_delta append to the open block
//	content_block_stop   → closes the block (tool_use JSON is parsed here)
//	message_delta        → carries the final stop_reason and output-token usage
//	message_stop         → terminal; the accumulated message is emitted as done
//	error                → a runtime error payload → terminal error event
//
// Per the dual failure model (FR-13) the decoder never panics: malformed
// payloads and Anthropic `error` events are surfaced as a returned error (which
// the transport turns into a terminal StreamErrorEvent) rather than crashing.
package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// anthropicBlock accumulates one content block's streaming state, keyed by its
// Anthropic content-block index. text/thinking append their deltas; tool_use
// accumulates a partial JSON string parsed lazily when the block is realized.
type anthropicBlock struct {
	kind        string // "text" | "thinking" | "tool_use" | "redacted_thinking"
	text        strings.Builder
	thinking    strings.Builder
	thinkingSig string
	textSig     string
	toolID      string
	toolName    string
	toolJSON    strings.Builder
	redacted    bool
}

// AnthropicDecoder is the stateful SSE decoder for the Anthropic Messages API.
// It implements the transport Decoder interface. It is not safe for concurrent
// use — the transport drives it from a single goroutine.
type AnthropicDecoder struct {
	blocks map[int]*anthropicBlock
	order  []int // content-block indices in first-seen order

	responseID    string
	responseModel string
	inputTokens   int
	outputTokens  int
	stopReason    string // mapped pigo stop reason (empty until message_delta)
	done          bool   // message_stop / done already emitted
}

// NewAnthropicDecoder builds a fresh decoder for one streamed response.
func NewAnthropicDecoder() *AnthropicDecoder {
	return &AnthropicDecoder{blocks: make(map[int]*anthropicBlock)}
}

// anthropicEvent is the discriminated envelope shared by every Anthropic SSE
// data payload; fields are populated selectively by event type.
type anthropicEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`

	Message *struct {
		ID    string          `json:"id"`
		Model string          `json:"model"`
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`

	ContentBlock *struct {
		Type string `json:"type"`
		// text
		Text string `json:"text"`
		// thinking
		Thinking string `json:"thinking"`
		// tool_use
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`

	Delta *struct {
		Type string `json:"type"`
		// text_delta
		Text string `json:"text"`
		// thinking_delta
		Thinking string `json:"thinking"`
		// signature_delta
		Signature string `json:"signature"`
		// input_json_delta
		PartialJSON string `json:"partial_json"`
		// message_delta
		StopReason string `json:"stop_reason"`
	} `json:"delta"`

	Usage *anthropicUsage `json:"usage"`

	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Decode turns one Anthropic SSE data payload into zero or more StreamEvents.
func (d *AnthropicDecoder) Decode(payload []byte) ([]StreamEvent, error) {
	var ev anthropicEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("anthropic: parse event: %w", err)
	}

	switch ev.Type {
	case "message_start":
		return d.onMessageStart(ev), nil
	case "content_block_start":
		return d.onBlockStart(ev), nil
	case "content_block_delta":
		return d.onBlockDelta(ev), nil
	case "content_block_stop":
		// Nothing to emit on stop; the block is already reflected in the partial.
		return nil, nil
	case "message_delta":
		return d.onMessageDelta(ev), nil
	case "message_stop":
		return d.finishDone(), nil
	case "ping":
		return nil, nil
	case "error":
		msg := "anthropic stream error"
		if ev.Error != nil {
			if ev.Error.Type != "" {
				msg = "anthropic " + ev.Error.Type
			}
			if ev.Error.Message != "" {
				msg += ": " + ev.Error.Message
			}
		}
		return nil, fmt.Errorf("%s", msg)
	default:
		// Unknown event types are ignored (forward-compatible).
		return nil, nil
	}
}

// Finish flushes a terminal done event if the stream ended without an explicit
// message_stop (e.g. a clean EOF mid-stream), so a partial response is still
// delivered rather than lost.
func (d *AnthropicDecoder) Finish() ([]StreamEvent, error) {
	if d.done {
		return nil, nil
	}
	return d.finishDone(), nil
}

func (d *AnthropicDecoder) onMessageStart(ev anthropicEvent) []StreamEvent {
	if ev.Message != nil {
		d.responseID = ev.Message.ID
		d.responseModel = ev.Message.Model
		if ev.Message.Usage != nil {
			d.inputTokens = ev.Message.Usage.InputTokens
			d.outputTokens = ev.Message.Usage.OutputTokens
		}
	}
	return []StreamEvent{StreamStartEvent{Partial: d.partial()}}
}

func (d *AnthropicDecoder) onBlockStart(ev anthropicEvent) []StreamEvent {
	if ev.ContentBlock == nil {
		return nil
	}
	b := &anthropicBlock{kind: ev.ContentBlock.Type}
	switch ev.ContentBlock.Type {
	case "text":
		b.text.WriteString(ev.ContentBlock.Text)
	case "thinking":
		b.thinking.WriteString(ev.ContentBlock.Thinking)
	case "redacted_thinking":
		b.redacted = true
	case "tool_use":
		b.toolID = ev.ContentBlock.ID
		b.toolName = ev.ContentBlock.Name
	}
	d.putBlock(ev.Index, b)

	switch ev.ContentBlock.Type {
	case "thinking", "redacted_thinking":
		return []StreamEvent{StreamThinkingEvent{Partial: d.partial()}}
	case "tool_use":
		return []StreamEvent{StreamToolCallEvent{Partial: d.partial()}}
	default:
		return []StreamEvent{StreamTextEvent{Partial: d.partial()}}
	}
}

func (d *AnthropicDecoder) onBlockDelta(ev anthropicEvent) []StreamEvent {
	if ev.Delta == nil {
		return nil
	}
	b := d.blocks[ev.Index]
	if b == nil {
		// A delta for an unseen index: open a bare block so we don't drop data.
		b = &anthropicBlock{}
		d.putBlock(ev.Index, b)
	}
	switch ev.Delta.Type {
	case "text_delta":
		b.text.WriteString(ev.Delta.Text)
		return []StreamEvent{StreamTextEvent{Partial: d.partial()}}
	case "thinking_delta":
		b.thinking.WriteString(ev.Delta.Thinking)
		return []StreamEvent{StreamThinkingEvent{Partial: d.partial()}}
	case "signature_delta":
		// Signature belongs to the thinking block it rides on.
		b.thinkingSig += ev.Delta.Signature
		return []StreamEvent{StreamThinkingEvent{Partial: d.partial()}}
	case "input_json_delta":
		b.toolJSON.WriteString(ev.Delta.PartialJSON)
		return []StreamEvent{StreamToolCallEvent{Partial: d.partial()}}
	default:
		return nil
	}
}

func (d *AnthropicDecoder) onMessageDelta(ev anthropicEvent) []StreamEvent {
	if ev.Delta != nil && ev.Delta.StopReason != "" {
		d.stopReason = mapAnthropicStopReason(ev.Delta.StopReason)
	}
	if ev.Usage != nil {
		// message_delta reports cumulative output tokens (and sometimes input).
		if ev.Usage.OutputTokens != 0 {
			d.outputTokens = ev.Usage.OutputTokens
		}
		if ev.Usage.InputTokens != 0 {
			d.inputTokens = ev.Usage.InputTokens
		}
	}
	// No standalone event kind for usage/stop-reason accumulation; the values
	// surface in the terminal done message.
	return nil
}

// finishDone builds the terminal assistant message and marks the decoder done.
func (d *AnthropicDecoder) finishDone() []StreamEvent {
	if d.done {
		return nil
	}
	d.done = true
	msg := d.partial()
	if msg.StopReason == "" {
		msg.StopReason = StopReasonEndTurn
	}
	return []StreamEvent{StreamDoneEvent{Message: msg}}
}

// putBlock records a block at index, tracking first-seen order.
func (d *AnthropicDecoder) putBlock(index int, b *anthropicBlock) {
	if _, seen := d.blocks[index]; !seen {
		d.order = append(d.order, index)
	}
	d.blocks[index] = b
}

// partial materializes the accumulated state into an AssistantMessage. Content
// blocks are emitted in content-block index order. Tool-use JSON that has not
// yet parsed cleanly is passed through as-is (raw partial), which is valid for
// a still-streaming partial and finalized once the block completes.
func (d *AnthropicDecoder) partial() AssistantMessage {
	msg := AssistantMessage{
		RoleField:     RoleAssistant,
		API:           "anthropic",
		Provider:      "anthropic",
		StopReason:    d.stopReason,
		ResponseID:    d.responseID,
		ResponseModel: d.responseModel,
	}
	if d.inputTokens != 0 || d.outputTokens != 0 {
		msg.Usage = &Usage{InputTokens: d.inputTokens, OutputTokens: d.outputTokens}
	}

	idx := make([]int, len(d.order))
	copy(idx, d.order)
	sort.Ints(idx)

	for _, i := range idx {
		b := d.blocks[i]
		if b == nil {
			continue
		}
		switch b.kind {
		case "thinking", "redacted_thinking":
			tc := NewThinkingContent(b.thinking.String())
			tc.ThinkingSignature = b.thinkingSig
			tc.Redacted = b.redacted
			msg.Content = append(msg.Content, tc)
		case "tool_use":
			args := json.RawMessage(strings.TrimSpace(b.toolJSON.String()))
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			msg.Content = append(msg.Content, NewToolCallContent(b.toolID, b.toolName, args))
		default: // text
			tc := NewTextContent(b.text.String())
			tc.TextSignature = b.textSig
			msg.Content = append(msg.Content, tc)
		}
	}
	return msg
}

// mapAnthropicStopReason maps an Anthropic stop_reason to the pigo StopReason
// set. Unknown reasons default to end_turn (a natural, non-error stop).
func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "max_tokens":
		return StopReasonLength
	case "tool_use":
		return StopReasonToolUse
	case "end_turn", "stop_sequence":
		return StopReasonEndTurn
	default:
		return StopReasonEndTurn
	}
}
