// This file implements the OpenAI-compatible streaming decoder (US-009): a
// stateful Decoder (see transport.go) for the OpenAI Chat Completions SSE
// stream, which is also the wire format spoken by most third-party gateways
// (OpenRouter, Groq, together, local servers, …). Selecting the base URL is a
// transport concern (NewRequest builds the *http.Request), so this decoder is
// base-URL agnostic and reused across every OpenAI-compatible provider.
//
// OpenAI streams a sequence of chat.completion.chunk objects:
//
//	{"choices":[{"delta":{"role":"assistant"}}]}                 → first chunk
//	{"choices":[{"delta":{"content":"Hel"}}]}                    → text delta
//	{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1",
//	    "function":{"name":"f","arguments":"{\"a\":"}}}]}]}       → tool-call delta
//	{"choices":[{"finish_reason":"tool_calls"}]}                 → stop reason
//	{"usage":{"prompt_tokens":10,"completion_tokens":5}}         → final usage
//	[DONE]                                                        → transport-level
//
// Per the dual failure model (FR-13) the decoder never panics: malformed
// payloads surface as a returned error which the transport turns into a
// terminal StreamErrorEvent.
package provider

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// openaiToolCall accumulates one streamed tool call, keyed by its delta index.
// id/name arrive once (usually in the first fragment); arguments accumulate.
type openaiToolCall struct {
	id   string
	name string
	args strings.Builder
}

// OpenAIDecoder is the stateful SSE decoder for the OpenAI Chat Completions API
// and compatible gateways. It implements the transport Decoder interface and is
// not safe for concurrent use — the transport drives it from one goroutine.
type OpenAIDecoder struct {
	text      strings.Builder
	thinking  strings.Builder // reasoning_content / reasoning stream (if any)
	toolCalls map[int]*openaiToolCall
	toolOrder []int // tool-call indices in first-seen order

	responseID    string
	responseModel string
	inputTokens   int
	outputTokens  int
	stopReason    string // mapped pigo stop reason (empty until finish_reason)
	done          bool
}

// NewOpenAIDecoder builds a fresh decoder for one streamed response.
func NewOpenAIDecoder() *OpenAIDecoder {
	return &OpenAIDecoder{toolCalls: make(map[int]*openaiToolCall)}
}

// openaiChunk is the streamed chat.completion.chunk envelope.
type openaiChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// ReasoningContent carries the model's reasoning/thinking stream on the
			// OpenAI wire (DeepSeek-R1, Kimi, and other reasoning models put their
			// chain-of-thought here). Some gateways name it "reasoning" instead, so
			// both are accepted; without this field the thinking stream is silently
			// dropped from the response and from history.
			ReasoningContent string            `json:"reasoning_content"`
			Reasoning        string            `json:"reasoning"`
			ToolCalls        []openaiToolDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	// Some gateways surface an error object inline on the stream.
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type openaiToolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Decode turns one OpenAI SSE data payload into zero or more StreamEvents.
func (d *OpenAIDecoder) Decode(payload []byte) ([]StreamEvent, error) {
	var chunk openaiChunk
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return nil, fmt.Errorf("openai: parse chunk: %w", err)
	}
	if chunk.Error != nil {
		msg := "openai stream error"
		if chunk.Error.Type != "" {
			msg = "openai " + chunk.Error.Type
		}
		if chunk.Error.Message != "" {
			msg += ": " + chunk.Error.Message
		}
		return nil, fmt.Errorf("%s", msg)
	}

	if chunk.ID != "" {
		d.responseID = chunk.ID
	}
	if chunk.Model != "" {
		d.responseModel = chunk.Model
	}
	if chunk.Usage != nil {
		d.inputTokens = chunk.Usage.PromptTokens
		d.outputTokens = chunk.Usage.CompletionTokens
	}

	var events []StreamEvent
	for _, choice := range chunk.Choices {
		// Reasoning stream (DeepSeek-R1 / Kimi / …): reasoning_content is the
		// common field; a few gateways use "reasoning". Accumulate whichever is set.
		if r := choice.Delta.ReasoningContent; r != "" {
			d.thinking.WriteString(r)
			events = append(events, StreamThinkingEvent{Partial: d.partial()})
		} else if r := choice.Delta.Reasoning; r != "" {
			d.thinking.WriteString(r)
			events = append(events, StreamThinkingEvent{Partial: d.partial()})
		}
		if choice.Delta.Content != "" {
			d.text.WriteString(choice.Delta.Content)
			events = append(events, StreamTextEvent{Partial: d.partial()})
		}
		for _, tc := range choice.Delta.ToolCalls {
			d.applyToolDelta(tc)
			events = append(events, StreamToolCallEvent{Partial: d.partial()})
		}
		if choice.FinishReason != "" {
			d.stopReason = mapOpenAIFinishReason(choice.FinishReason)
		}
	}
	return events, nil
}

// Finish flushes a terminal done event if the stream ended without an explicit
// terminator, so a partial response is still delivered rather than lost.
func (d *OpenAIDecoder) Finish() ([]StreamEvent, error) {
	if d.done {
		return nil, nil
	}
	return d.finishDone(), nil
}

// applyToolDelta merges one tool-call fragment into the accumulated state.
func (d *OpenAIDecoder) applyToolDelta(tc openaiToolDelta) {
	call := d.toolCalls[tc.Index]
	if call == nil {
		call = &openaiToolCall{}
		d.toolCalls[tc.Index] = call
		d.toolOrder = append(d.toolOrder, tc.Index)
	}
	if tc.ID != "" {
		call.id = tc.ID
	}
	if tc.Function.Name != "" {
		call.name = tc.Function.Name
	}
	call.args.WriteString(tc.Function.Arguments)
}

// finishDone builds the terminal assistant message and marks the decoder done.
func (d *OpenAIDecoder) finishDone() []StreamEvent {
	if d.done {
		return nil
	}
	d.done = true
	msg := d.partial()
	if msg.StopReason == "" {
		msg.StopReason = agentcore.StopReasonEndTurn
	}
	return []StreamEvent{StreamDoneEvent{Message: msg}}
}

// partial materializes the accumulated state into an AssistantMessage: the text
// block first (if any), then tool-call blocks in first-seen index order.
func (d *OpenAIDecoder) partial() agentcore.AssistantMessage {
	msg := agentcore.AssistantMessage{
		RoleField:     agentcore.RoleAssistant,
		API:           "openai",
		Provider:      "openai",
		StopReason:    d.stopReason,
		ResponseID:    d.responseID,
		ResponseModel: d.responseModel,
	}
	if d.inputTokens != 0 || d.outputTokens != 0 {
		msg.Usage = &agentcore.Usage{InputTokens: d.inputTokens, OutputTokens: d.outputTokens}
	}
	if d.thinking.Len() > 0 {
		msg.Content = append(msg.Content, agentcore.NewThinkingContent(d.thinking.String()))
	}
	if d.text.Len() > 0 {
		msg.Content = append(msg.Content, agentcore.NewTextContent(d.text.String()))
	}

	idx := make([]int, len(d.toolOrder))
	copy(idx, d.toolOrder)
	sort.Ints(idx)
	for _, i := range idx {
		call := d.toolCalls[i]
		if call == nil {
			continue
		}
		args := json.RawMessage(strings.TrimSpace(call.args.String()))
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		msg.Content = append(msg.Content, agentcore.NewToolCallContent(call.id, call.name, args))
	}
	return msg
}

// mapOpenAIFinishReason maps an OpenAI finish_reason to the pigo StopReason set.
// Unknown reasons default to end_turn (a natural, non-error stop).
func mapOpenAIFinishReason(reason string) string {
	switch reason {
	case "length":
		return agentcore.StopReasonLength
	case "tool_calls", "function_call":
		return agentcore.StopReasonToolUse
	case "stop":
		return agentcore.StopReasonEndTurn
	default:
		return agentcore.StopReasonEndTurn
	}
}
