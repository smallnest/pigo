package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// A recorded Gemini streamGenerateContent SSE stream (alt=sse) covering a
// thinking part (thought=true), a text part, and a functionCall part, ending
// with finishReason=STOP and usageMetadata. Structurally faithful to the real
// wire format.
const geminiToolCallSSE = `data: {"responseId":"resp-1","modelVersion":"gemini-2.0-flash","candidates":[{"content":{"parts":[{"thought":true,"text":"Let me think."}],"role":"model"}}]}

data: {"responseId":"resp-1","modelVersion":"gemini-2.0-flash","candidates":[{"content":{"parts":[{"text":"I'll check "}],"role":"model"}}]}

data: {"responseId":"resp-1","modelVersion":"gemini-2.0-flash","candidates":[{"content":{"parts":[{"text":"the weather."}],"role":"model"}}]}

data: {"responseId":"resp-1","modelVersion":"gemini-2.0-flash","candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"city":"SF"}}}],"role":"model"}}]}

data: {"responseId":"resp-1","modelVersion":"gemini-2.0-flash","candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":21,"candidatesTokenCount":14}}

`

func TestGeminiDecoderToolCallStream(t *testing.T) {
	dec := NewGeminiDecoder()
	events, final := feedSSE(t, dec, geminiToolCallSSE)

	if len(events) == 0 || events[len(events)-1].EventKind() != StreamEventDone {
		t.Fatalf("expected a done event last, got %v", eventKinds(events))
	}
	// A functionCall present → tool_use even though Gemini reports STOP.
	if final.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("stop reason = %q, want tool_use", final.StopReason)
	}
	// Usage: prompt→input, candidates→output.
	if final.Usage == nil || final.Usage.InputTokens != 21 || final.Usage.OutputTokens != 14 {
		t.Errorf("usage = %+v, want input=21 output=14", final.Usage)
	}
	if final.ResponseID != "resp-1" || final.ResponseModel != "gemini-2.0-flash" {
		t.Errorf("response id/model = %q/%q", final.ResponseID, final.ResponseModel)
	}

	// Content blocks: thinking, text, function-call.
	if len(final.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d: %+v", len(final.Content), final.Content)
	}
	th, ok := final.Content[0].(agentcore.ThinkingContent)
	if !ok || th.Thinking != "Let me think." {
		t.Errorf("thinking block = %+v", final.Content[0])
	}
	txt, ok := final.Content[1].(agentcore.TextContent)
	if !ok || txt.Text != "I'll check the weather." {
		t.Errorf("text block = %+v", final.Content[1])
	}
	tool, ok := final.Content[2].(agentcore.ToolCallContent)
	if !ok || tool.Name != "get_weather" || tool.ID == "" {
		t.Fatalf("tool block = %+v", final.Content[2])
	}
	var args map[string]string
	if err := json.Unmarshal(tool.Arguments, &args); err != nil {
		t.Fatalf("tool arguments not valid JSON %q: %v", tool.Arguments, err)
	}
	if args["city"] != "SF" {
		t.Errorf("tool arguments = %v, want city=SF", args)
	}
}

func TestGeminiDecoderTextOnlyStop(t *testing.T) {
	body := `data: {"responseId":"r","modelVersion":"m","candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"}}]}

data: {"responseId":"r","modelVersion":"m","candidates":[{"content":{"parts":[{"text":" world"}],"role":"model"}}]}

data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}

`
	dec := NewGeminiDecoder()
	_, final := feedSSE(t, dec, body)
	if final.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want end_turn", final.StopReason)
	}
	if len(final.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(final.Content))
	}
	txt, ok := final.Content[0].(agentcore.TextContent)
	if !ok || txt.Text != "Hello world" {
		t.Errorf("text = %+v", final.Content[0])
	}
}

func TestGeminiDecoderMaxTokensMapsToLength(t *testing.T) {
	body := `data: {"candidates":[{"content":{"parts":[{"text":"truncated"}],"role":"model"}}]}

data: {"candidates":[{"finishReason":"MAX_TOKENS"}]}

`
	dec := NewGeminiDecoder()
	_, final := feedSSE(t, dec, body)
	if final.StopReason != agentcore.StopReasonLength {
		t.Errorf("MAX_TOKENS must map to length, got %q", final.StopReason)
	}
}

// TestGeminiDecoderErrorPayload verifies a Gemini error object becomes a decode
// error (which the transport turns into a terminal error event), never a panic.
func TestGeminiDecoderErrorPayload(t *testing.T) {
	dec := NewGeminiDecoder()
	_, err := dec.Decode([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`))
	if err == nil {
		t.Fatal("error payload must return a decode error")
	}
	if !strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") {
		t.Errorf("error should name the status, got %v", err)
	}
}

// TestGeminiDecoderMalformedPayload verifies invalid JSON is a returned error,
// not a panic.
func TestGeminiDecoderMalformedPayload(t *testing.T) {
	dec := NewGeminiDecoder()
	if _, err := dec.Decode([]byte(`{not json`)); err == nil {
		t.Fatal("malformed payload must return an error")
	}
}

// TestGeminiDecoderFinishFlushesPartial verifies a stream cut short (no
// finishReason) still yields a done event on Finish, defaulting to end_turn.
func TestGeminiDecoderFinishFlushesPartial(t *testing.T) {
	body := `data: {"candidates":[{"content":{"parts":[{"text":"partial"}],"role":"model"}}]}

`
	dec := NewGeminiDecoder()
	events, final := feedSSE(t, dec, body)
	if events[len(events)-1].EventKind() != StreamEventDone {
		t.Fatalf("Finish must emit a terminal done event, got %v", eventKinds(events))
	}
	if final.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("cut-short stream should default to end_turn, got %q", final.StopReason)
	}
	if len(final.Content) != 1 {
		t.Fatalf("expected the partial text block, got %+v", final.Content)
	}
}

// TestGeminiDecoderThroughTransport wires the decoder through the real transport
// pump against a recorded SSE server, exercising the full path.
func TestGeminiDecoderThroughTransport(t *testing.T) {
	srv := sseServer(t, geminiToolCallSSE)
	defer srv.Close()

	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    NewGeminiDecoder(),
	})
	if err != nil {
		t.Fatalf("StreamRequest: %v", err)
	}
	var kinds []string
	for ev := range stream.Events() {
		kinds = append(kinds, ev.EventKind())
	}
	final, resErr := stream.Result(context.Background())
	if resErr != nil {
		t.Fatalf("result: %v", resErr)
	}
	if final.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("stop reason via transport = %q, want tool_use", final.StopReason)
	}
	if len(final.Content) != 3 {
		t.Errorf("expected 3 content blocks via transport, got %d", len(final.Content))
	}
	if kinds[len(kinds)-1] != StreamEventDone {
		t.Errorf("stream must end with done, got %v", kinds)
	}
}
