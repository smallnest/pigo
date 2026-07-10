package agent

import (
	"context"
	"errors"
	"sync"
)

// EventStream is the Go equivalent of pi's EventStream<T,R>: a producer pushes
// events onto a channel while a consumer ranges over them, and a terminal event
// yields a final result R. It replaces pi's async generator (event-stream.ts).
//
// Design (research §2.2):
//   - Iteration: Events() returns <-chan T for `for ev := range s.Events()`.
//   - Result: Result(ctx) blocks until the producer sets a result (or the
//     stream fails/cancels). The result is NOT sent on the event channel, so a
//     consumer that stops reading events can still obtain it.
//   - Cancellation: the producer selects on ctx.Done() when sending, so a
//     consumer that stops reading never leaks the producer goroutine.
//
// pi's isComplete/extractResult callbacks are retained as optional fields so a
// producer can let the stream detect the terminal event itself; a producer may
// instead call SetResult explicitly (more Go-idiomatic). Either path resolves
// Result exactly once.
type EventStream[T any, R any] struct {
	ch chan T

	// IsComplete reports whether an event is the terminal one. Optional: if
	// set, Emit auto-captures the result via ExtractResult when it returns true.
	IsComplete func(event T) bool
	// ExtractResult derives the final result from the terminal event. Required
	// when IsComplete is set.
	ExtractResult func(event T) R

	resultOnce sync.Once
	result     R
	resultErr  error
	resultCh   chan struct{} // closed once result (or resultErr) is set
}

// ErrStreamIncomplete is returned by Result when the event channel closed
// without any result being set (the producer ended abnormally without a
// terminal event).
var ErrStreamIncomplete = errors.New("agent: event stream ended without a result")

// NewEventStream constructs an EventStream with the given channel buffer size.
// A buffer of 0 gives fully synchronous back-pressure (each Emit blocks until a
// consumer receives), matching pi's sequential `await emit(...)`.
func NewEventStream[T any, R any](buffer int) *EventStream[T, R] {
	if buffer < 0 {
		buffer = 0
	}
	return &EventStream[T, R]{
		ch:       make(chan T, buffer),
		resultCh: make(chan struct{}),
	}
}

// Events returns the receive-only event channel. Ranging over it terminates
// when the producer calls Close.
func (s *EventStream[T, R]) Events() <-chan T { return s.ch }

// Emit sends an event to consumers, honoring cancellation. If ctx is cancelled
// before the event is received, Emit returns ctx.Err() and the event is
// dropped. When IsComplete is configured and reports true for the event, the
// result is captured (once) before the send.
func (s *EventStream[T, R]) Emit(ctx context.Context, event T) error {
	if s.IsComplete != nil && s.IsComplete(event) && s.ExtractResult != nil {
		s.SetResult(s.ExtractResult(event))
	}
	select {
	case s.ch <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetResult records the final result. Only the first call wins; later calls
// (including SetError) are no-ops. Safe to call before Close.
func (s *EventStream[T, R]) SetResult(result R) {
	s.resultOnce.Do(func() {
		s.result = result
		close(s.resultCh)
	})
}

// SetError records a terminal error as the stream's outcome. Only the first
// call among SetResult/SetError wins.
func (s *EventStream[T, R]) SetError(err error) {
	s.resultOnce.Do(func() {
		s.resultErr = err
		close(s.resultCh)
	})
}

// Close closes the event channel, ending consumer iteration. If no result was
// set, Result will report ErrStreamIncomplete. Call exactly once from the
// producer after the last Emit.
func (s *EventStream[T, R]) Close() {
	// Ensure a waiting Result never blocks forever if the producer forgot to
	// set a result.
	s.resultOnce.Do(func() {
		s.resultErr = ErrStreamIncomplete
		close(s.resultCh)
	})
	close(s.ch)
}

// Result blocks until the producer sets a result/error, ctx is cancelled, or
// the stream closes without a result. It is safe to call concurrently and
// returns the same outcome on every call.
func (s *EventStream[T, R]) Result(ctx context.Context) (R, error) {
	select {
	case <-s.resultCh:
		return s.result, s.resultErr
	case <-ctx.Done():
		var zero R
		return zero, ctx.Err()
	}
}
