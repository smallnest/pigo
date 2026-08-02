package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/openai/openai-go/option"

	"github.com/smallnest/pigo/internal/agentcore"
)

// roundTripFunc adapts a function to http.RoundTripper so a test can stub the
// SDK transport without a live endpoint.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newResponsesTestDriver builds a resp_api driver whose SDK client is pointed at
// the given stub round-tripper, capturing the request path the SDK targets.
func newResponsesTestDriver(baseURL string, rt roundTripFunc) *responsesDriver {
	d := NewOpenAIResponsesProvider("openai", baseURL, nil)
	d.clientOpts = []option.RequestOption{
		option.WithHTTPClient(&http.Client{Transport: rt}),
	}
	return d
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// sseResponse builds a 200 text/event-stream response whose body is the given
// SSE data frames, mirroring how the Responses API streams events. Each frame is
// a JSON object carrying its own "type" discriminator.
func sseResponse(frames ...string) *http.Response {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("data: ")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(b.String())),
	}
}

// completedFrame is a response.completed SSE frame whose embedded Response yields
// the given output text, id, model, and token usage — the authoritative terminal
// payload the driver maps into its final message.
func completedFrame(text, id, model string, inTok, outTok int) string {
	return `{"type":"response.completed","sequence_number":99,"response":{` +
		`"id":"` + id + `","model":"` + model + `",` +
		`"output":[{"type":"message","role":"assistant","status":"completed",` +
		`"content":[{"type":"output_text","text":"` + text + `"}]}],` +
		`"usage":{"input_tokens":` + itoa(inTok) + `,"output_tokens":` + itoa(outTok) +
		`,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`
}

func deltaFrame(delta string) string {
	return `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,` +
		`"content_index":0,"sequence_number":1,"logprobs":[],"delta":"` + delta + `"}`
}

func itoa(n int) string { return strconv.Itoa(n) }

// drain collects the terminal message from a stream, mirroring how the loop
// consumes a provider stream.
func drain(t *testing.T, stream *AssistantMessageEventStream) agentcore.AssistantMessage {
	t.Helper()
	for range stream.Events() {
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result error: %v", err)
	}
	return msg
}

func userMsg(text string) agentcore.UserMessage {
	return agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
	}
}

func TestResponsesDriverPostsToResponsesEndpoint(t *testing.T) {
	var gotPath, gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(
			deltaFrame("hi "),
			deltaFrame("there"),
			completedFrame("hi there", "resp_123", "gpt-4o", 11, 7),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	req := CompletionRequest{
		Model: "gpt-4o",
		Context: LlmContext{
			SystemPrompt: "be terse",
			Messages:     agentcore.MessageList{userMsg("hello")},
		},
		Config: StreamConfig{APIKey: "sk-test"},
	}
	stream, err := d.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)

	if !strings.HasSuffix(gotPath, "/responses") {
		t.Errorf("request path = %q, want to end with /responses", gotPath)
	}
	// The prompt and system instruction must reach the wire body.
	if !strings.Contains(gotBody, "hello") {
		t.Errorf("request body missing prompt: %q", gotBody)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if payload["instructions"] != "be terse" {
		t.Errorf("instructions = %v, want %q", payload["instructions"], "be terse")
	}
	if payload["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o", payload["model"])
	}
	// A streaming call must set stream:true on the wire.
	if payload["stream"] != true {
		t.Errorf("stream = %v, want true", payload["stream"])
	}

	if got := textOf(msg); got != "hi there" {
		t.Errorf("assistant text = %q, want %q", got, "hi there")
	}
	if msg.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want end_turn", msg.StopReason)
	}
	if msg.ResponseID != "resp_123" {
		t.Errorf("response id = %q, want resp_123", msg.ResponseID)
	}
	if msg.Usage == nil || msg.Usage.InputTokens != 11 || msg.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want {11 7}", msg.Usage)
	}
	if msg.API != "openai" || msg.Provider != "openai" {
		t.Errorf("tags = api:%q provider:%q, want openai/openai", msg.API, msg.Provider)
	}
}

// The driver must emit incremental text partials as deltas arrive, and each
// partial must carry the text accumulated so far (not just the latest delta), so
// the terminal message equals the concatenation the caller already rendered.
func TestResponsesDriverStreamsIncrementalDeltas(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			deltaFrame("Hello"),
			deltaFrame(", "),
			deltaFrame("world"),
			completedFrame("Hello, world", "resp_9", "gpt-4o", 3, 4),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}

	var textPartials []string
	for ev := range stream.Events() {
		if te, ok := ev.(StreamTextEvent); ok {
			textPartials = append(textPartials, textOf(te.Partial))
		}
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result error: %v", err)
	}

	want := []string{"Hello", "Hello, ", "Hello, world"}
	if len(textPartials) != len(want) {
		t.Fatalf("got %d text partials %q, want %d %q", len(textPartials), textPartials, len(want), want)
	}
	for i := range want {
		if textPartials[i] != want[i] {
			t.Errorf("partial[%d] = %q, want %q", i, textPartials[i], want[i])
		}
	}
	// Final aggregation must match the completed payload, i.e. the last partial.
	if got := textOf(msg); got != "Hello, world" {
		t.Errorf("final text = %q, want %q", got, "Hello, world")
	}
}

// A cancelled context must terminate the stream with an error rather than
// yielding a normal end_turn message. The transport cancels mid-flight (after
// the stream has started) and reports the cancellation, mirroring how an
// in-progress SSE read aborts when the caller cancels.
func TestResponsesDriverContextCancelStopsStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(ctx, CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion should not early-error on cancel: %v", err)
	}

	var sawError bool
	for ev := range stream.Events() {
		if _, ok := ev.(StreamErrorEvent); ok {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected a terminal StreamErrorEvent after context cancel")
	}
	msg, _ := stream.Result(context.Background())
	if msg.StopReason != agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want error", msg.StopReason)
	}
}

// A non-2xx from the endpoint must ride the stream as a terminal error event,
// not be returned from StreamCompletion (dual failure model, FR-13).
func TestResponsesDriverUpstreamErrorRidesStream(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	req := CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hello")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	}
	stream, err := d.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamCompletion should not early-error on upstream failure: %v", err)
	}

	var sawError bool
	for ev := range stream.Events() {
		if _, ok := ev.(StreamErrorEvent); ok {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected a terminal StreamErrorEvent for a 401 response")
	}
	msg, _ := stream.Result(context.Background())
	if msg.StopReason != agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want error", msg.StopReason)
	}
}

// An in-band error event (type "error") mid-stream must ride the stream as a
// terminal error, carrying the event's message. This is a distinct path from a
// transport-level non-2xx (which surfaces via the stream's Err()).
func TestResponsesDriverInStreamErrorEvent(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			deltaFrame("partial"),
			`{"type":"error","code":"server_error","message":"boom","param":"","sequence_number":2}`,
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion should not early-error: %v", err)
	}

	var errEvent *StreamErrorEvent
	for ev := range stream.Events() {
		if se, ok := ev.(StreamErrorEvent); ok {
			e := se
			errEvent = &e
		}
	}
	if errEvent == nil {
		t.Fatal("expected a terminal StreamErrorEvent for an in-band error event")
	}
	if !strings.Contains(errEvent.Message.ErrorMessage, "boom") {
		t.Errorf("error message = %q, want to contain %q", errEvent.Message.ErrorMessage, "boom")
	}
	msg, _ := stream.Result(context.Background())
	if msg.StopReason != agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want error", msg.StopReason)
	}
}

// A missing API key is the one early "cannot build the stream" error.
func TestResponsesDriverMissingKeyIsEarlyError(t *testing.T) {
	d := NewOpenAIResponsesProvider("openai", "https://api.openai.test/v1", nil)
	_, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:  "gpt-4o",
		Config: StreamConfig{APIKey: "  "},
	})
	if err == nil {
		t.Fatal("expected early error for missing API key")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("error = %q, want to mention missing API key", err.Error())
	}
}

// textOf returns the concatenated text content of an assistant message.
func textOf(m agentcore.AssistantMessage) string {
	var b bytes.Buffer
	for _, c := range m.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// fakeTool is a minimal AgentTool for exercising tool-schema serialization; it
// never executes in these transport-level tests.
type fakeTool struct {
	name   string
	desc   string
	schema json.RawMessage
}

func (t fakeTool) Name() string                               { return t.name }
func (t fakeTool) Description() string                        { return t.desc }
func (t fakeTool) Schema() json.RawMessage                    { return t.schema }
func (t fakeTool) ExecutionMode() agentcore.ToolExecutionMode { return agentcore.ToolExecutionParallel }
func (t fakeTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{}, nil
}

// functionCallDoneFrame is a response.output_item.done SSE frame carrying a
// finalized function_call item (the model's tool request).
func functionCallDoneFrame(callID, name, argsJSON string) string {
	frame := map[string]any{
		"type":            "response.output_item.done",
		"output_index":    0,
		"sequence_number": 5,
		"item": map[string]any{
			"type":      "function_call",
			"id":        "fc_1",
			"call_id":   callID,
			"name":      name,
			"arguments": argsJSON,
			"status":    "completed",
		},
	}
	b, _ := json.Marshal(frame)
	return string(b)
}

// completedToolFrame is a response.completed frame whose output is a single
// function_call item (no assistant text) plus token usage.
func completedToolFrame(id, model, callID, name, argsJSON string) string {
	frame := map[string]any{
		"type":            "response.completed",
		"sequence_number": 99,
		"response": map[string]any{
			"id":    id,
			"model": model,
			"output": []any{map[string]any{
				"type":      "function_call",
				"id":        "fc_1",
				"call_id":   callID,
				"name":      name,
				"arguments": argsJSON,
				"status":    "completed",
			}},
			"usage": map[string]any{
				"input_tokens":          5,
				"output_tokens":         2,
				"input_tokens_details":  map[string]any{"cached_tokens": 0},
				"output_tokens_details": map[string]any{"reasoning_tokens": 0},
			},
		},
	}
	b, _ := json.Marshal(frame)
	return string(b)
}

// A pigo tool must reach the wire as a Responses function tool: type "function",
// its name, JSON-Schema parameters, and description.
func TestResponsesDriverSendsToolSchema(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("ok", "resp_1", "gpt-4o", 1, 1)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	tool := fakeTool{
		name:   "read_file",
		desc:   "reads a file",
		schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model: "gpt-4o",
		Context: LlmContext{
			Messages: agentcore.MessageList{userMsg("read a.go")},
			Tools:    []agentcore.AgentTool{tool},
		},
		Config: StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want a single-element array", payload["tools"])
	}
	tool0 := tools[0].(map[string]any)
	if tool0["type"] != "function" {
		t.Errorf("tool type = %v, want function", tool0["type"])
	}
	if tool0["name"] != "read_file" {
		t.Errorf("tool name = %v, want read_file", tool0["name"])
	}
	if tool0["description"] != "reads a file" {
		t.Errorf("tool description = %v, want %q", tool0["description"], "reads a file")
	}
	params, ok := tool0["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("tool parameters missing or not an object: %v", tool0["parameters"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok || props["path"] == nil {
		t.Errorf("tool parameters.properties.path missing: %v", params)
	}
}

// A function_call in the completed response must be parsed into a pigo
// ToolCallContent (id + name + raw arguments) and set StopReason=tool_use; a
// StreamToolCallEvent must also surface the pending call mid-stream.
func TestResponsesDriverParsesToolCall(t *testing.T) {
	args := `{"path":"a.go"}`
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			functionCallDoneFrame("call_abc", "read_file", args),
			completedToolFrame("resp_7", "gpt-4o", "call_abc", "read_file", args),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("read a.go")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}

	var sawToolCallEvent bool
	for ev := range stream.Events() {
		if _, ok := ev.(StreamToolCallEvent); ok {
			sawToolCallEvent = true
		}
	}
	if !sawToolCallEvent {
		t.Error("expected a StreamToolCallEvent mid-stream")
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result error: %v", err)
	}

	calls := msg.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	if calls[0].ID != "call_abc" || calls[0].Name != "read_file" {
		t.Errorf("tool call = id:%q name:%q, want call_abc/read_file", calls[0].ID, calls[0].Name)
	}
	if string(calls[0].Arguments) != args {
		t.Errorf("tool call arguments = %q, want %q", calls[0].Arguments, args)
	}
	if msg.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("stop reason = %q, want tool_use", msg.StopReason)
	}
}

// On a follow-up turn, a prior assistant tool call must be replayed as a
// function_call input item and its result as a function_call_output item, both
// keyed by the same call_id, so the model can pair request and result.
func TestResponsesDriverBackfillsToolResult(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("done", "resp_2", "gpt-4o", 8, 3)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	assistant := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewToolCallContent("call_abc", "read_file", json.RawMessage(`{"path":"a.go"}`)),
		},
	}
	result := agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: "call_abc",
		ToolName:   "read_file",
		Content:    agentcore.ContentList{agentcore.NewTextContent("package main")},
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model: "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{
			userMsg("read a.go"), assistant, result,
		}},
		Config: StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	var payload struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	var sawCall, sawOutput bool
	for _, item := range payload.Input {
		switch item["type"] {
		case "function_call":
			sawCall = true
			if item["call_id"] != "call_abc" || item["name"] != "read_file" {
				t.Errorf("function_call item = %v, want call_abc/read_file", item)
			}
			if item["arguments"] != `{"path":"a.go"}` {
				t.Errorf("function_call arguments = %v, want the raw args JSON string", item["arguments"])
			}
		case "function_call_output":
			sawOutput = true
			if item["call_id"] != "call_abc" {
				t.Errorf("function_call_output call_id = %v, want call_abc", item["call_id"])
			}
			if item["output"] != "package main" {
				t.Errorf("function_call_output output = %v, want %q", item["output"], "package main")
			}
		}
	}
	if !sawCall {
		t.Error("wire input missing the replayed function_call item")
	}
	if !sawOutput {
		t.Error("wire input missing the function_call_output item")
	}
}

// completedReasoningFrame is a response.completed frame whose output carries a
// reasoning item (summary text) followed by the assistant message text.
func completedReasoningFrame(id, model, summary, text string) string {
	frame := map[string]any{
		"type":            "response.completed",
		"sequence_number": 99,
		"response": map[string]any{
			"id":    id,
			"model": model,
			"output": []any{
				map[string]any{
					"type": "reasoning",
					"id":   "rs_1",
					"summary": []any{map[string]any{
						"type": "summary_text",
						"text": summary,
					}},
				},
				map[string]any{
					"type":   "message",
					"role":   "assistant",
					"status": "completed",
					"content": []any{map[string]any{
						"type": "output_text",
						"text": text,
					}},
				},
			},
			"usage": map[string]any{
				"input_tokens":          3,
				"output_tokens":         4,
				"input_tokens_details":  map[string]any{"cached_tokens": 0},
				"output_tokens_details": map[string]any{"reasoning_tokens": 2},
			},
		},
	}
	b, _ := json.Marshal(frame)
	return string(b)
}

// A user message carrying an image must reach the wire as a message whose
// content is a part list: an input_text part plus an input_image part whose
// image_url is the base64 data URI.
func TestResponsesDriverSendsImageInput(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("ok", "resp_1", "gpt-4o", 1, 1)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	imgMsg := agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content: agentcore.ContentList{
			agentcore.NewTextContent("what is this?"),
			agentcore.NewImageContent("aGVsbG8=", "image/png"),
		},
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{imgMsg}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	var payload struct {
		Input []struct {
			Type    string           `json:"type"`
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if len(payload.Input) != 1 {
		t.Fatalf("got %d input items, want 1", len(payload.Input))
	}
	parts := payload.Input[0].Content
	var sawText, sawImage bool
	for _, p := range parts {
		switch p["type"] {
		case "input_text":
			sawText = true
			if p["text"] != "what is this?" {
				t.Errorf("input_text = %v, want %q", p["text"], "what is this?")
			}
		case "input_image":
			sawImage = true
			if p["image_url"] != "data:image/png;base64,aGVsbG8=" {
				t.Errorf("input_image image_url = %v, want the data URI", p["image_url"])
			}
		}
	}
	if !sawText {
		t.Error("wire input missing the input_text part")
	}
	if !sawImage {
		t.Error("wire input missing the input_image part")
	}
}

// A request with a thinking level must set the reasoning.effort (and an auto
// summary) on the wire, and a reasoning item in the completed response must be
// parsed into a leading ThinkingContent block.
func TestResponsesDriverReasoning(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedReasoningFrame("resp_9", "gpt-4o", "let me think", "the answer")), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("solve it")}},
		Config:  StreamConfig{APIKey: "sk-test", ThinkingLevel: agentcore.ThinkingMedium},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)

	var payload struct {
		Reasoning struct {
			Effort  string `json:"effort"`
			Summary string `json:"summary"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if payload.Reasoning.Effort != "medium" {
		t.Errorf("reasoning.effort = %q, want medium", payload.Reasoning.Effort)
	}
	if payload.Reasoning.Summary != "auto" {
		t.Errorf("reasoning.summary = %q, want auto", payload.Reasoning.Summary)
	}

	var thinking string
	for _, c := range msg.Content {
		if tc, ok := c.(agentcore.ThinkingContent); ok {
			thinking = tc.Thinking
		}
	}
	if thinking != "let me think" {
		t.Errorf("thinking content = %q, want %q", thinking, "let me think")
	}
}
