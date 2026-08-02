// This file implements the OpenAI Responses API driver (US-003, #539), the
// backing for --protocol openai/resp_api. Unlike openAICompatDriver (which
// hand-rolls the Chat Completions wire format), this driver speaks the
// Responses API (POST {base_url}/responses) via the official
// github.com/openai/openai-go SDK.
//
// This milestone covers streaming text: a plain prompt in, assistant text out,
// consumed from the Responses SSE stream and mapped into pigo's AssistantMessage
// the same way OpenAIDecoder does (API/Provider tags, Usage,
// ResponseID/ResponseModel, StopReason=end_turn). Tools (#541) and
// images/reasoning (#542) layer on later.
//
// Failure model (FR-13): only the earliest "cannot build the stream" case
// (missing API key) is a returned error. Every runtime failure — including a
// non-2xx from the endpoint — rides the returned stream as a terminal
// StreamErrorEvent, matching the chat driver's observable behavior.
//
// Base URL + auth: the SDK client is pointed at the resolved base_url and given
// the resolved key via option.WithBaseURL / option.WithAPIKey rather than
// reading the environment, so ResolveBaseURL precedence is preserved. A custom
// *http.Client may be injected (option.WithHTTPClient) for tests.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"

	"github.com/smallnest/pigo/internal/agentcore"
)

// responsesDriver is the Provider backing --protocol openai/resp_api. It holds
// the provider identity, the resolved endpoint, the model catalog, and whether
// an API key is required, and builds an openai-go client per request.
type responsesDriver struct {
	name    string
	baseURL string
	models  []Model
	// requiresAuth reports whether an API key must be present. Public OpenAI /
	// Azure require it; a local gateway may not.
	requiresAuth bool
	// clientOpts are extra SDK options; tests inject option.WithHTTPClient here
	// to stub the transport.
	clientOpts []option.RequestOption
}

// NewOpenAIResponsesProvider builds a Responses API provider targeting baseURL.
// baseURL must be the fully resolved endpoint (e.g. https://api.openai.com/v1);
// the SDK appends the /responses path.
func NewOpenAIResponsesProvider(name, baseURL string, models []Model) *responsesDriver {
	return &responsesDriver{
		name:         name,
		baseURL:      baseURL,
		models:       models,
		requiresAuth: true,
	}
}

func (d *responsesDriver) Name() string    { return d.name }
func (d *responsesDriver) Models() []Model { return d.models }

// StreamCompletion issues a streaming Responses API call and surfaces the result
// on an AssistantMessageEventStream: a start event, incremental text events as
// deltas arrive, and a terminal done event carrying the aggregated message.
func (d *responsesDriver) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	if d.requiresAuth && strings.TrimSpace(req.Config.APIKey) == "" {
		// Early "cannot build the stream": reference the provider, never a value.
		return nil, fmt.Errorf("%s: missing API key", d.name)
	}

	opts := make([]option.RequestOption, 0, len(d.clientOpts)+2)
	if d.baseURL != "" {
		opts = append(opts, option.WithBaseURL(d.baseURL))
	}
	if key := strings.TrimSpace(req.Config.APIKey); key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	opts = append(opts, d.clientOpts...)
	client := openai.NewClient(opts...)

	params := buildResponsesParams(req)

	stream := NewAssistantMessageEventStream(0)
	go d.pump(ctx, stream, &client, params)
	return stream, nil
}

// pump consumes the Responses SSE stream and translates events into pigo stream
// events. It always closes the stream. Every runtime failure (transport error,
// context cancellation, or an upstream error/failed event) becomes a terminal
// StreamErrorEvent (dual failure model), not a returned error.
//
// Incremental text.delta events emit a StreamTextEvent carrying the accumulated
// text so far, so the TUI renders tokens as they arrive. The terminal message is
// built from the authoritative response.completed payload via mapResponse, so
// the final aggregation matches the non-streamed result exactly. If no completed
// event arrives (a truncated stream that still ended cleanly), the accumulated
// delta text is used as a fallback.
func (d *responsesDriver) pump(ctx context.Context, stream *AssistantMessageEventStream, client *openai.Client, params responses.ResponseNewParams) {
	defer stream.Close()

	if err := stream.Emit(ctx, StreamStartEvent{Partial: d.newPartial()}); err != nil {
		return
	}

	sse := client.Responses.NewStreaming(ctx, params)
	defer sse.Close()

	var text strings.Builder
	var toolCalls []agentcore.ToolCallContent
	var completed *responses.Response
	for sse.Next() {
		if ctx.Err() != nil {
			d.emitError(stream, ctx.Err())
			return
		}
		switch variant := sse.Current().AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			text.WriteString(variant.Delta)
			partial := d.newPartial()
			partial.Content = append(partial.Content, agentcore.NewTextContent(text.String()))
			partial.Content = appendToolCalls(partial.Content, toolCalls)
			if err := stream.Emit(ctx, StreamTextEvent{Partial: partial}); err != nil {
				return
			}
		case responses.ResponseOutputItemDoneEvent:
			// A finalized function_call item carries the model's tool request
			// (name + arguments + call_id). Accumulate it and surface a tool-call
			// partial so the TUI can show the pending call before the run ends.
			if fc := variant.Item.AsFunctionCall(); fc.Type == "function_call" {
				toolCalls = append(toolCalls, toolCallContent(fc))
				partial := d.newPartial()
				if t := text.String(); t != "" {
					partial.Content = append(partial.Content, agentcore.NewTextContent(t))
				}
				partial.Content = appendToolCalls(partial.Content, toolCalls)
				if err := stream.Emit(ctx, StreamToolCallEvent{Partial: partial}); err != nil {
					return
				}
			}
		case responses.ResponseCompletedEvent:
			r := variant.Response
			completed = &r
		case responses.ResponseFailedEvent:
			d.emitError(stream, fmt.Errorf("response failed"))
			return
		case responses.ResponseErrorEvent:
			d.emitError(stream, fmt.Errorf("%s", variant.Message))
			return
		}
	}
	if err := sse.Err(); err != nil {
		d.emitError(stream, err)
		return
	}

	var msg agentcore.AssistantMessage
	if completed != nil {
		msg = d.mapResponse(completed)
	} else {
		msg = d.newPartial()
		msg.StopReason = agentcore.StopReasonEndTurn
		if t := text.String(); t != "" {
			msg.Content = append(msg.Content, agentcore.NewTextContent(t))
		}
		msg.Content = appendToolCalls(msg.Content, toolCalls)
		if len(toolCalls) > 0 {
			msg.StopReason = agentcore.StopReasonToolUse
		}
	}
	stream.Emit(ctx, StreamDoneEvent{Message: msg})
}

// emitError emits a terminal StreamErrorEvent tagged for this provider. Uses a
// background context so the emit isn't dropped when ctx is already cancelled.
func (d *responsesDriver) emitError(stream *AssistantMessageEventStream, err error) {
	stream.Emit(context.Background(), StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:    agentcore.RoleAssistant,
			API:          "openai",
			Provider:     d.name,
			StopReason:   agentcore.StopReasonError,
			ErrorMessage: err.Error(),
		},
		Err: fmt.Errorf("%s: %w", d.name, err),
	})
}

// newPartial builds an empty assistant message tagged for this provider, the
// seed for start/text partials (mirrors OpenAIDecoder.partial()'s identity).
func (d *responsesDriver) newPartial() agentcore.AssistantMessage {
	return agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		API:       "openai",
		Provider:  d.name,
	}
}

// mapResponse materializes a completed Responses API result into pigo's
// AssistantMessage: text content, tool calls, usage (when present),
// diagnostics, and a stop reason (tool_use when the model requested a tool,
// otherwise end_turn).
func (d *responsesDriver) mapResponse(resp *responses.Response) agentcore.AssistantMessage {
	msg := d.newPartial()
	msg.StopReason = agentcore.StopReasonEndTurn
	msg.ResponseID = resp.ID
	msg.ResponseModel = string(resp.Model)
	if text := resp.OutputText(); text != "" {
		msg.Content = append(msg.Content, agentcore.NewTextContent(text))
	}
	var sawToolCall bool
	for _, item := range resp.Output {
		if fc := item.AsFunctionCall(); fc.Type == "function_call" {
			msg.Content = append(msg.Content, toolCallContent(fc))
			sawToolCall = true
		}
	}
	if sawToolCall {
		msg.StopReason = agentcore.StopReasonToolUse
	}
	if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
		msg.Usage = &agentcore.Usage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
		}
	}
	return msg
}

// toolCallContent maps a Responses function_call item into a pigo
// ToolCallContent, keyed by the model's call_id so the tool result can be
// backfilled against it on the next turn. Arguments ride verbatim as raw JSON.
func toolCallContent(fc responses.ResponseFunctionToolCall) agentcore.ToolCallContent {
	return agentcore.NewToolCallContent(fc.CallID, fc.Name, json.RawMessage(fc.Arguments))
}

// appendToolCalls appends each accumulated tool call to a content list. Kept
// separate so the streaming partial and the terminal message build identical
// content from the same source.
func appendToolCalls(content agentcore.ContentList, calls []agentcore.ToolCallContent) agentcore.ContentList {
	for _, c := range calls {
		content = append(content, c)
	}
	return content
}

// buildResponsesParams maps a CompletionRequest onto Responses API params. The
// system prompt becomes Instructions; pigo tools become Responses function
// tools; and each message is replayed as the matching input item(s): assistant
// tool calls as function_call items, tool results as function_call_output
// items, and text as a role-tagged message. Non-text message content (images)
// is deferred to #542.
func buildResponsesParams(req CompletionRequest) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
	}
	if sp := strings.TrimSpace(req.Context.SystemPrompt); sp != "" {
		params.Instructions = openai.String(sp)
	}
	if tools := buildResponsesTools(req.Context.Tools); len(tools) > 0 {
		params.Tools = tools
	}

	items := make(responses.ResponseInputParam, 0, len(req.Context.Messages))
	for _, m := range req.Context.Messages {
		items = appendInputItems(items, m)
	}
	params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: items}
	return params
}

// buildResponsesTools converts pigo tools into Responses function tools. Each
// tool's JSON Schema becomes the function parameters; a schema that is empty or
// not a JSON object falls back to an empty object schema so the wire stays
// valid. Strict mode is off: pigo schemas are not authored against the Responses
// strict-function contract (which requires additionalProperties:false etc.).
func buildResponsesTools(tools []agentcore.AgentTool) []responses.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		params := map[string]any{}
		if raw := t.Schema(); len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				params = map[string]any{}
			}
		}
		tool := responses.ToolParamOfFunction(t.Name(), params, false)
		if desc := t.Description(); desc != "" {
			tool.OfFunction.Description = openai.String(desc)
		}
		out = append(out, tool)
	}
	return out
}

// appendInputItems replays one pigo message as its Responses input item(s).
func appendInputItems(items responses.ResponseInputParam, m agentcore.Message) responses.ResponseInputParam {
	switch msg := m.(type) {
	case agentcore.ToolResultMessage:
		// A tool result is backfilled against the model's call_id so the model
		// can pair it with the request it issued the previous turn.
		items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(
			msg.ToolCallID, contentText(msg.Content)))
	case agentcore.AssistantMessage:
		if text := contentText(msg.Content); text != "" {
			items = append(items, responses.ResponseInputItemParamOfMessage(
				text, responses.EasyInputMessageRoleAssistant))
		}
		for _, call := range msg.ToolCalls() {
			items = append(items, responses.ResponseInputItemParamOfFunctionCall(
				string(call.Arguments), call.ID, call.Name))
		}
	default:
		if text := messageText(m); text != "" {
			items = append(items, responses.ResponseInputItemParamOfMessage(
				text, responsesRole(m.Role())))
		}
	}
	return items
}

// responsesRole maps a pigo message role to the Responses API input role. Tool
// results are surfaced as user turns for this text milestone.
func responsesRole(role string) responses.EasyInputMessageRole {
	switch role {
	case agentcore.RoleAssistant:
		return responses.EasyInputMessageRoleAssistant
	default:
		return responses.EasyInputMessageRoleUser
	}
}

// messageText concatenates the text blocks of a message, ignoring non-text
// content (handled in later milestones).
func messageText(m agentcore.Message) string {
	var b strings.Builder
	switch msg := m.(type) {
	case agentcore.UserMessage:
		collectText(&b, msg.Content)
	case agentcore.AssistantMessage:
		collectText(&b, msg.Content)
	case agentcore.ToolResultMessage:
		collectText(&b, msg.Content)
	}
	return b.String()
}

func collectText(b *strings.Builder, content agentcore.ContentList) {
	for _, c := range content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
}

// contentText concatenates the text blocks of a content list.
func contentText(content agentcore.ContentList) string {
	var b strings.Builder
	collectText(&b, content)
	return b.String()
}
