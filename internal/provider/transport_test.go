package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// jsonDecoder is a trivial Decoder: each payload is a JSON object with a "text"
// field yielding a StreamTextEvent, or {"done":true} yielding a StreamDoneEvent.
type jsonDecoder struct {
	finished bool
}

func (d *jsonDecoder) Decode(payload []byte) ([]StreamEvent, error) {
	var m struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
		Bad  bool   `json:"bad"`
	}
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, err
	}
	if m.Bad {
		return nil, fmt.Errorf("decoder rejected payload")
	}
	if m.Done {
		return []StreamEvent{StreamDoneEvent{Message: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn}}}, nil
	}
	return []StreamEvent{StreamTextEvent{Partial: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}}}, nil
}

func (d *jsonDecoder) Finish() ([]StreamEvent, error) {
	d.finished = true
	return nil, nil
}

// sseServer returns an httptest server that writes the given SSE body.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("server write: %v", err)
		}
	}))
}

func newReqFn(url string) func(context.Context) (*http.Request, error) {
	return func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	}
}

// TestTransportSSEParsing verifies data accumulation, blank-line flush, [DONE]
// discard, and ":" keep-alive handling.
func TestTransportSSEParsing(t *testing.T) {
	body := ": keep-alive comment\n" +
		"data: {\"text\":\"hi\"}\n" +
		"\n" +
		"data: [DONE]\n" +
		"\n" +
		"data: {\"done\":true}\n" +
		"\n"
	srv := sseServer(t, body)
	defer srv.Close()

	dec := &jsonDecoder{}
	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    dec,
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
		t.Fatalf("result error: %v", resErr)
	}
	// text (from first data) then done; [DONE] payload must be discarded.
	if len(kinds) != 2 || kinds[0] != StreamEventText || kinds[1] != StreamEventDone {
		t.Errorf("event kinds = %v, want [text done]", kinds)
	}
	if final.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("final stop reason = %q, want end_turn", final.StopReason)
	}
	if !dec.finished {
		t.Errorf("decoder Finish() was not called on clean EOF")
	}
}

// TestTransportDecodeErrorRidesStream confirms a decode failure becomes a
// terminal error event, not a returned error.
func TestTransportDecodeErrorRidesStream(t *testing.T) {
	body := "data: {\"bad\":true}\n\n"
	srv := sseServer(t, body)
	defer srv.Close()

	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    &jsonDecoder{},
	})
	if err != nil {
		t.Fatalf("decode failure must NOT be a returned error: %v", err)
	}
	final, _ := stream.Result(context.Background())
	if final.StopReason != agentcore.StopReasonError {
		t.Errorf("expected terminal error message, got stopReason=%q", final.StopReason)
	}
}

// TestTransportEarlyBuildFailure verifies a request-build failure is a returned
// error (the only early-error case).
func TestTransportEarlyBuildFailure(t *testing.T) {
	_, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: func(ctx context.Context) (*http.Request, error) {
			return nil, fmt.Errorf("cannot build")
		},
		Decoder: &jsonDecoder{},
	})
	if err == nil {
		t.Fatal("request-build failure must return an error")
	}
}

// TestTransportMissingConfig checks required-field validation.
func TestTransportMissingConfig(t *testing.T) {
	if _, err := StreamRequest(context.Background(), TransportConfig{Decoder: &jsonDecoder{}}); err == nil {
		t.Error("missing NewRequest must error")
	}
	if _, err := StreamRequest(context.Background(), TransportConfig{NewRequest: newReqFn("http://x")}); err == nil {
		t.Error("missing Decoder must error")
	}
}

// TestTransportRetryOn503 verifies the connect retry path honors a retryable
// status and eventually succeeds without replaying a consumed stream.
func TestTransportRetryOn503(t *testing.T) {
	t.Setenv("PIGO_STREAM_IDLE_TIMEOUT", "5s")
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"done\":true}\n\n"))
	}))
	defer srv.Close()

	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    &jsonDecoder{},
	})
	if err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	final, _ := stream.Result(context.Background())
	if final.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("final stop reason = %q, want end_turn", final.StopReason)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("expected 2 attempts (1 failed + 1 ok), got %d", got)
	}
}

// TestTransportRetryExhausted verifies a persistently retryable status returns
// an early error after exhausting retries.
func TestTransportRetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest:        newReqFn(srv.URL),
		Decoder:           &jsonDecoder{},
		MaxConnectRetries: 1,
	})
	if err == nil {
		t.Fatal("exhausted retries must return an error")
	}
}

// TestTransportIdleWatchdog verifies the idle watchdog fires a terminal error
// when the server stalls without sending data.
func TestTransportIdleWatchdog(t *testing.T) {
	t.Setenv("PIGO_STREAM_IDLE_TIMEOUT", "100ms")
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-hold // never send any data
	}))
	defer srv.Close()
	defer close(hold)

	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    &jsonDecoder{},
	})
	if err != nil {
		t.Fatalf("StreamRequest: %v", err)
	}

	done := make(chan agentcore.AssistantMessage, 1)
	go func() {
		final, _ := stream.Result(context.Background())
		done <- final
	}()
	select {
	case final := <-done:
		if final.StopReason != agentcore.StopReasonError {
			t.Errorf("idle watchdog must produce terminal error, got %q", final.StopReason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle watchdog did not fire")
	}
}

// TestRetryAfterParsing covers seconds and absent header parsing.
func TestRetryAfterParsing(t *testing.T) {
	h := http.Header{}
	if d := retryAfter(h); d != 0 {
		t.Errorf("absent Retry-After = %v, want 0", d)
	}
	h.Set("Retry-After", "3")
	if d := retryAfter(h); d != 3*time.Second {
		t.Errorf("Retry-After 3 = %v, want 3s", d)
	}
}
