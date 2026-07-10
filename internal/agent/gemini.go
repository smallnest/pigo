// This file implements the Gemini streaming decoder (US-010): a stateful
// Decoder (see transport.go) for the Google Gemini streamGenerateContent SSE
// stream (alt=sse). Like the other provider decoders it is base-URL agnostic —
// selecting the endpoint is a transport concern (NewRequest builds the
// *http.Request).
//
// Gemini streams a sequence of GenerateContentResponse snapshots, each carrying
// incremental candidate parts:
//
//	{"candidates":[{"content":{"parts":[{"text":"Hel"}],"role":"model"}}]}   → text delta
//	{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f",
//	    "args":{"a":1}}}]}}]}                                                → function call
//	{"candidates":[{"content":{"parts":[{"thought":true,
//	    "text":"reasoning"}]}}]}                                             → thinking delta
//	{"candidates":[{"finishReason":"STOP"}]}                                → stop reason
//	{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}      → usage
//
// Unlike OpenAI, Gemini function calls arrive as one complete part (args is a
// full JSON object, not an accumulated string) and carry no call id, so a
// stable synthetic id is assigned. Gemini reports STOP even when a functionCall
// is present, so the presence of any function call maps the stop reason to
// tool_use.
//
// Per the dual failure model (FR-13) the decoder never panics: malformed
// payloads and Gemini `error` payloads surface as a returned error which the
// transport turns into a terminal StreamErrorEvent.
package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// geminiFunctionCall accumulates one Gemini function call. Gemini delivers the
// whole call in a single part (name + complete args object), so nothing is
// streamed incrementally; the synthetic id keeps tool-result correlation stable.
type geminiFunctionCall struct {
	id   string
	name string
	args json.RawMessage
}

// GeminiDecoder is the stateful SSE decoder for the Gemini
// streamGenerateContent API. It implements the transport Decoder interface and
// is not safe for concurrent use — the transport drives it from one goroutine.
type GeminiDecoder struct {
	text     strings.Builder
	thinking strings.Builder
	calls    []geminiFunctionCall

	responseID    string
	responseModel string
	inputTokens   int
	outputTokens  int
	stopReason    string // mapped pigo stop reason (empty until finishReason)
	hasToolCall   bool
	done          bool
}

// NewGeminiDecoder builds a fresh decoder for one streamed response.
func NewGeminiDecoder() *GeminiDecoder {
	return &GeminiDecoder{}
}

// geminiResponse is the streamed GenerateContentResponse envelope.
type geminiResponse struct {
	ResponseID   string `json:"responseId"`
	ModelVersion string `json:"modelVersion"`
	Candidates   []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
			Role  string       `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	// Gemini surfaces failures as a top-level error object.
	Error *struct {
		Code    int    `json:"code"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

// geminiPart is one content part: text, thinking text (thought=true), or a
// functionCall. Exactly one shape is populated per part.
type geminiPart struct {
	Text         string `json:"text"`
	Thought      bool   `json:"thought"`
	FunctionCall *struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
}

// Decode turns one Gemini SSE data payload into zero or more StreamEvents.
func (d *GeminiDecoder) Decode(payload []byte) ([]StreamEvent, error) {
	var resp geminiResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("gemini: parse response: %w", err)
	}
	if resp.Error != nil {
		msg := "gemini stream error"
		if resp.Error.Status != "" {
			msg = "gemini " + resp.Error.Status
		}
		if resp.Error.Message != "" {
			msg += ": " + resp.Error.Message
		}
		return nil, fmt.Errorf("%s", msg)
	}

	if resp.ResponseID != "" {
		d.responseID = resp.ResponseID
	}
	if resp.ModelVersion != "" {
		d.responseModel = resp.ModelVersion
	}
	if resp.UsageMetadata != nil {
		d.inputTokens = resp.UsageMetadata.PromptTokenCount
		d.outputTokens = resp.UsageMetadata.CandidatesTokenCount
	}

	var events []StreamEvent
	for _, cand := range resp.Candidates {
		for _, part := range cand.Content.Parts {
			switch {
			case part.FunctionCall != nil:
				d.calls = append(d.calls, geminiFunctionCall{
					id:   "call_" + strconv.Itoa(len(d.calls)),
					name: part.FunctionCall.Name,
					args: part.FunctionCall.Args,
				})
				d.hasToolCall = true
				events = append(events, StreamToolCallEvent{Partial: d.partial()})
			case part.Thought:
				d.thinking.WriteString(part.Text)
				events = append(events, StreamThinkingEvent{Partial: d.partial()})
			case part.Text != "":
				d.text.WriteString(part.Text)
				events = append(events, StreamTextEvent{Partial: d.partial()})
			}
		}
		if cand.FinishReason != "" {
			d.stopReason = mapGeminiFinishReason(cand.FinishReason, d.hasToolCall)
		}
	}
	return events, nil
}

// Finish flushes a terminal done event if the stream ended without an explicit
// terminator, so a partial response is still delivered rather than lost.
func (d *GeminiDecoder) Finish() ([]StreamEvent, error) {
	if d.done {
		return nil, nil
	}
	return d.finishDone(), nil
}

// finishDone builds the terminal assistant message and marks the decoder done.
func (d *GeminiDecoder) finishDone() []StreamEvent {
	if d.done {
		return nil
	}
	d.done = true
	msg := d.partial()
	if msg.StopReason == "" {
		if d.hasToolCall {
			msg.StopReason = StopReasonToolUse
		} else {
			msg.StopReason = StopReasonEndTurn
		}
	}
	return []StreamEvent{StreamDoneEvent{Message: msg}}
}

// partial materializes the accumulated state into an AssistantMessage: thinking
// block first (if any), then text, then function-call blocks in arrival order.
func (d *GeminiDecoder) partial() AssistantMessage {
	msg := AssistantMessage{
		RoleField:     RoleAssistant,
		API:           "gemini",
		Provider:      "google",
		StopReason:    d.stopReason,
		ResponseID:    d.responseID,
		ResponseModel: d.responseModel,
	}
	if d.inputTokens != 0 || d.outputTokens != 0 {
		msg.Usage = &Usage{InputTokens: d.inputTokens, OutputTokens: d.outputTokens}
	}
	if d.thinking.Len() > 0 {
		msg.Content = append(msg.Content, NewThinkingContent(d.thinking.String()))
	}
	if d.text.Len() > 0 {
		msg.Content = append(msg.Content, NewTextContent(d.text.String()))
	}
	for _, c := range d.calls {
		args := json.RawMessage(strings.TrimSpace(string(c.args)))
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		msg.Content = append(msg.Content, NewToolCallContent(c.id, c.name, args))
	}
	return msg
}

// mapGeminiFinishReason maps a Gemini finishReason to the pigo StopReason set.
// Gemini reports STOP even alongside a functionCall, so a present tool call
// takes precedence and maps to tool_use. Unknown reasons default to end_turn.
func mapGeminiFinishReason(reason string, hasToolCall bool) string {
	if hasToolCall {
		return StopReasonToolUse
	}
	switch reason {
	case "MAX_TOKENS":
		return StopReasonLength
	case "STOP":
		return StopReasonEndTurn
	default:
		return StopReasonEndTurn
	}
}
