// This file implements the shared transport driver (US-007 support): a
// provider-agnostic layer that turns an HTTP request into a stream of
// StreamEvents. Each provider degenerates to a stateful Decoder; the transport
// owns HTTP + SSE line parsing + retry + dual watchdogs + the dual failure
// model.
//
// The design mirrors pi's providerio: the transport never returns a runtime
// failure as a Go error once streaming has begun — it rides the stream as a
// terminal StreamErrorEvent. Only the earliest "cannot build the stream" case
// (bad request construction) is a returned error.
package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// StreamEvent is the transport-level alias for AssistantMessageEvent. Decoders
// produce these; the transport forwards them onto the stream. (Decision #25:
// reuse AssistantMessageEvent rather than a parallel event type.)
type StreamEvent = AssistantMessageEvent

// Decoder is the per-provider stateful SSE payload decoder. The transport calls
// Decode for every complete SSE data payload (one event's worth of bytes) and
// Finish once the stream ends so the decoder can flush any buffered terminal
// event.
type Decoder interface {
	// Decode turns one SSE data payload into zero or more StreamEvents. A
	// returned error is treated as a runtime stream failure (terminal error
	// event), never a panic.
	Decode(payload []byte) ([]StreamEvent, error)
	// Finish flushes any trailing state, returning a final batch of events.
	Finish() ([]StreamEvent, error)
}

// defaultIdleTimeout is the watchdog idle window; PIGO_STREAM_IDLE_TIMEOUT
// (a Go duration string, e.g. "3m") overrides it.
const defaultIdleTimeout = 5 * time.Minute

// idleTimeout resolves the configured idle watchdog window.
func idleTimeout() time.Duration {
	if v := os.Getenv("PIGO_STREAM_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultIdleTimeout
}

// TransportConfig configures a single StreamRequest run.
type TransportConfig struct {
	// Client is the HTTP client; defaults to http.DefaultClient when nil.
	Client *http.Client
	// NewRequest builds a fresh *http.Request for each connection attempt. It is
	// called once per connect (initial + reconnects) so retries never replay a
	// consumed body — the caller owns idempotent request construction.
	NewRequest func(ctx context.Context) (*http.Request, error)
	// Decoder converts SSE payloads to StreamEvents (required).
	Decoder Decoder
	// MaxConnectRetries bounds connect-only retries (default 2).
	MaxConnectRetries int
}

// StreamRequest runs cfg as a transport stream. Per the dual failure model it
// returns an error only when the very first request cannot be built or the
// initial connection can never be established; every runtime failure once
// streaming begins rides the returned stream as a terminal StreamErrorEvent.
func StreamRequest(ctx context.Context, cfg TransportConfig) (*AssistantMessageEventStream, error) {
	if cfg.NewRequest == nil {
		return nil, errors.New("transport: NewRequest is required")
	}
	if cfg.Decoder == nil {
		return nil, errors.New("transport: Decoder is required")
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	maxRetries := cfg.MaxConnectRetries
	if maxRetries == 0 {
		maxRetries = 2
	}

	// Connect once up front so a "cannot even build the stream" failure surfaces
	// as a returned error (the only early-error case per FR-13).
	resp, err := connect(ctx, client, cfg.NewRequest, maxRetries)
	if err != nil {
		return nil, err
	}

	stream := NewAssistantMessageEventStream(0)
	go pump(ctx, stream, resp, cfg.Decoder)
	return stream, nil
}

// connect performs the initial connection with retry. It only retries when the
// server explicitly signals a retryable condition (429/503/529); it never
// replays a consumed stream, so retrying at connect time is always safe.
func connect(ctx context.Context, client *http.Client, newReq func(context.Context) (*http.Request, error), maxRetries int) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := newReq(ctx)
		if err != nil {
			return nil, fmt.Errorf("transport: build request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = classifyTransportError(err)
			if !isRetryableNetErr(err) || attempt == maxRetries {
				return nil, lastErr
			}
			if !sleepBackoff(ctx, attempt, 0) {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == 529 {
			wait := retryAfter(resp.Header)
			resp.Body.Close()
			lastErr = fmt.Errorf("transport: upstream %d", resp.StatusCode)
			if attempt == maxRetries {
				return nil, lastErr
			}
			if !sleepBackoff(ctx, attempt, wait) {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, fmt.Errorf("transport: upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return resp, nil
	}
	return nil, lastErr
}

// pump drives the SSE read loop with dual watchdogs, decoding payloads and
// forwarding events onto the stream. All runtime failures become a terminal
// error event; pump always closes the stream.
func pump(ctx context.Context, stream *AssistantMessageEventStream, resp *http.Response, dec Decoder) {
	defer stream.Close()
	defer resp.Body.Close()

	idle := idleTimeout()
	// content-stall watchdog is slightly slacker than idle (idle×1.2) so a slow
	// but progressing stream is not killed by the stall guard.
	stall := time.Duration(float64(idle) * 1.2)

	// The watchdog fires by cancelling a derived context; reads race against it.
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// done stops the reader goroutine so it never blocks on a send after pump
	// returns (watchdog / abort paths), avoiding a goroutine leak.
	done := make(chan struct{})
	defer close(done)
	lines := make(chan string)
	readErr := make(chan error, 1)
	go readLines(resp.Body, lines, readErr, done)

	var dataBuf strings.Builder
	idleTimer := time.NewTimer(idle)
	stallTimer := time.NewTimer(stall)
	defer idleTimer.Stop()
	defer stallTimer.Stop()

	emit := func(events []StreamEvent) bool {
		for _, ev := range events {
			if err := stream.Emit(watchCtx, ev); err != nil {
				return false
			}
		}
		return true
	}

	fail := func(msg string, err error) {
		stream.Emit(context.Background(), StreamErrorEvent{
			Message: AssistantMessage{
				RoleField:    RoleAssistant,
				StopReason:   StopReasonError,
				ErrorMessage: msg,
			},
			Err: err,
		})
	}

	flush := func() bool {
		if dataBuf.Len() == 0 {
			return true
		}
		payload := dataBuf.String()
		dataBuf.Reset()
		if payload == "[DONE]" {
			return true
		}
		events, err := dec.Decode([]byte(payload))
		if err != nil {
			fail("decode error: "+err.Error(), err)
			return false
		}
		return emit(events)
	}

	for {
		select {
		case <-ctx.Done():
			fail("stream aborted", ctx.Err())
			return
		case <-idleTimer.C:
			fail("idle timeout: no data received", errStreamIdle)
			return
		case <-stallTimer.C:
			fail("content stall timeout", errStreamStall)
			return
		case err := <-readErr:
			if err != nil && !errors.Is(err, io.EOF) {
				fail("read error: "+classifyTransportError(err).Error(), err)
				return
			}
			// Clean EOF: flush any buffered payload, then finish the decoder.
			if !flush() {
				return
			}
			finalEvents, ferr := dec.Finish()
			if ferr != nil {
				fail("finish error: "+ferr.Error(), ferr)
				return
			}
			emit(finalEvents)
			return
		case line, ok := <-lines:
			if !ok {
				continue
			}
			// Any byte resets the idle watchdog; a flushed event resets stall.
			resetTimer(idleTimer, idle)
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				// Blank line = event boundary: flush accumulated data.
				if !flush() {
					return
				}
				resetTimer(stallTimer, stall)
			case strings.HasPrefix(line, ":"):
				// Comment / keep-alive: ignore payload, watchdog already reset.
			case strings.HasPrefix(line, "data:"):
				dataBuf.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			default:
				// Non-data field (event:, id:, etc.) — ignored for our decoders.
			}
		}
	}
}

// readLines reads the body line by line, pushing each onto lines and the final
// error (io.EOF on clean close) onto readErr. It stops promptly when done is
// closed so pump can return on a watchdog/abort without leaking this goroutine.
func readLines(r io.Reader, lines chan<- string, readErr chan<- error, done <-chan struct{}) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			select {
			case lines <- line:
			case <-done:
				return
			}
		}
		if err != nil {
			select {
			case readErr <- err:
			case <-done:
			}
			return
		}
	}
}

// resetTimer stops and re-arms t to fire after d.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// Sentinel errors for watchdog classification.
var (
	errStreamIdle  = errors.New("stream idle timeout")
	errStreamStall = errors.New("stream content stall")
)

// retryAfter parses a Retry-After header (seconds or HTTP-date), returning 0
// when absent/unparseable.
func retryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// sleepBackoff waits for the retry delay (Retry-After if given, else
// exponential), honoring ctx cancellation. Returns false if ctx was cancelled.
func sleepBackoff(ctx context.Context, attempt int, retryAfter time.Duration) bool {
	d := retryAfter
	if d == 0 {
		d = time.Duration(1<<uint(attempt)) * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// isRetryableNetErr reports whether a client.Do error is a transient network
// condition worth reconnecting for (timeout / temporary).
func isRetryableNetErr(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

// classifyTransportError maps a low-level error to a typed, descriptive error
// using net.Error / errors.Is classification.
func classifyTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("transport: canceled: %w", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("transport: deadline exceeded: %w", err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return fmt.Errorf("transport: network timeout: %w", err)
		}
		return fmt.Errorf("transport: network error: %w", err)
	}
	return fmt.Errorf("transport: %w", err)
}
