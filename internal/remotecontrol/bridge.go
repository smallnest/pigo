package remotecontrol

import (
	"context"
	"io"
	"sync"
)

// Sink is the subset of the server the bridge needs. *Server satisfies it. It
// lets the bridge stream output, request confirmations, and check connectivity
// without importing the HTTP layer directly (and makes the seam unit-testable).
type Sink interface {
	SendOutput(text string)
	SendConfirm(confirmID, tool, summary string)
	HasClient() bool
}

// Decision is the outcome of a confirmation prompt.
type Decision struct {
	Approve bool
	Always  bool
}

// remoteInputBuffer bounds how many un-consumed remote prompts we hold before
// dropping the oldest. Backpressure/coalescing is refined in the hardening node
// (#445); here we simply avoid blocking the WebSocket read goroutine.
const remoteInputBuffer = 32

// Bridge is the seam that merges the local terminal with a remote browser
// controlling the same REPL session. It:
//
//   - tees session output to the remote client (OutputWriter),
//   - surfaces remote-submitted prompts as an input channel (RemoteInput),
//   - routes confirmation prompts to the remote and awaits a decision (Confirm),
//     resolvable either remotely (OnDecide) or locally (ResolveConfirm).
//
// A nil *Bridge (deps.remote == nil) means remote control is off; callers must
// guard with Enabled or simply not construct one, so existing behavior is
// byte-identical.
//
// Bridge implements the server Handler interface (OnInput, OnDecide).
type Bridge struct {
	sink Sink

	inputs chan string

	mu       sync.Mutex
	pending  map[string]chan Decision
	nextID   uint64
}

// NewBridge builds a bridge over sink.
func NewBridge(sink Sink) *Bridge {
	return &Bridge{
		sink:    sink,
		inputs:  make(chan string, remoteInputBuffer),
		pending: make(map[string]chan Decision),
	}
}

// Enabled reports whether remote control is active with a connected client.
func (b *Bridge) Enabled() bool {
	return b != nil && b.sink != nil && b.sink.HasClient()
}

// OutputWriter returns an io.Writer that streams everything written to it to the
// remote client as output frames. It is intended to be combined with the local
// terminal writer via io.MultiWriter, so local rendering is unchanged and the
// remote receives a copy. Writes never fail and never block on the network
// (delivery is best-effort via the sink).
func (b *Bridge) OutputWriter() io.Writer {
	return outputWriter{b}
}

type outputWriter struct{ b *Bridge }

func (w outputWriter) Write(p []byte) (int, error) {
	if w.b != nil && w.b.sink != nil {
		w.b.sink.SendOutput(string(p))
	}
	return len(p), nil
}

// RemoteInput exposes prompts submitted by the remote client. The REPL input
// loop selects on this channel alongside local stdin.
func (b *Bridge) RemoteInput() <-chan string { return b.inputs }

// OnInput implements Handler: a remote prompt. Non-blocking so the WebSocket
// read goroutine is never stalled; if the buffer is full the oldest queued
// prompt is dropped to make room (refined in #445).
func (b *Bridge) OnInput(text string) {
	for {
		select {
		case b.inputs <- text:
			return
		default:
			// Drop the oldest to make room, then retry.
			select {
			case <-b.inputs:
			default:
			}
		}
	}
}

// Confirm requests approval for a risky tool call from the remote client and
// blocks until the client decides, the context is cancelled (e.g. answered
// locally instead), or a deadline in ctx fires. The returned bool is true only
// when the decision came from the remote client; on ctx cancellation it is
// false and the caller should fall back to the local answer.
func (b *Bridge) Confirm(ctx context.Context, tool, summary string) (Decision, bool) {
	id := b.register()
	defer b.unregister(id)

	b.sink.SendConfirm(id, tool, summary)

	b.mu.Lock()
	ch := b.pending[id]
	b.mu.Unlock()

	select {
	case d := <-ch:
		return d, true
	case <-ctx.Done():
		return Decision{}, false
	}
}

// OnDecide implements Handler: the remote client's answer to a confirmation.
func (b *Bridge) OnDecide(confirmID string, approve, always bool) {
	b.ResolveConfirm(confirmID, approve, always)
}

// ResolveConfirm delivers a decision to a pending Confirm. It returns true if a
// waiting Confirm was resolved. Both the remote path (OnDecide) and the local
// terminal path may call it; the first delivery wins and later ones are no-ops.
func (b *Bridge) ResolveConfirm(confirmID string, approve, always bool) bool {
	b.mu.Lock()
	ch, ok := b.pending[confirmID]
	b.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- Decision{Approve: approve, Always: always}:
		return true
	default:
		return false // already resolved
	}
}

func (b *Bridge) register() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := "c" + itoa(b.nextID)
	b.pending[id] = make(chan Decision, 1)
	return id
}

func (b *Bridge) unregister(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// itoa avoids importing strconv for a single small conversion.
func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
