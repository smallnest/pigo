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
			if err := stream.Emit(ctx, StreamTextEvent{Partial: partial}); err != nil {
				return
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
// AssistantMessage: text content, usage (when present), diagnostics, and a
// natural end_turn stop (this milestone has no tool/length stops yet).
func (d *responsesDriver) mapResponse(resp *responses.Response) agentcore.AssistantMessage {
	msg := d.newPartial()
	msg.StopReason = agentcore.StopReasonEndTurn
	msg.ResponseID = resp.ID
	msg.ResponseModel = string(resp.Model)
	if text := resp.OutputText(); text != "" {
		msg.Content = append(msg.Content, agentcore.NewTextContent(text))
	}
	if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
		msg.Usage = &agentcore.Usage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
		}
	}
	return msg
}

// buildResponsesParams maps a CompletionRequest onto Responses API params. For
// this milestone the system prompt becomes Instructions and each message's text
// becomes an input item with the matching role; non-text content is deferred to
// later milestones (tools #541, images #542).
func buildResponsesParams(req CompletionRequest) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
	}
	if sp := strings.TrimSpace(req.Context.SystemPrompt); sp != "" {
		params.Instructions = openai.String(sp)
	}

	items := make(responses.ResponseInputParam, 0, len(req.Context.Messages))
	for _, m := range req.Context.Messages {
		text := messageText(m)
		if text == "" {
			continue
		}
		items = append(items, responses.ResponseInputItemParamOfMessage(text, responsesRole(m.Role())))
	}
	params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: items}
	return params
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
