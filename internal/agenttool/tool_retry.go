// This file adds resilience to the single tool-execution seam (see
// tool_executor.go): when a tool's Execute returns a non-nil Go error, the
// error is classified as transient (worth a bounded retry) or terminal (give
// up immediately). This is deliberately separate from and does NOT touch the
// transport-layer connect-time retry in internal/provider/transport.go, which
// keeps its own, stricter semantics (only 429/503/529, respect Retry-After,
// never replay a consumed stream).
package agenttool

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

// maxToolRetries is the default cap on RETRIES (not attempts) for a transient
// tool error: 2 retries => at most 3 attempts total. Override per-executor via
// ToolExecutorConfig.MaxToolRetries. This is always finite; the retry loop can
// never spin forever.
const maxToolRetries = 2

// toolRetryBaseDelay is the unit of the small linear backoff between attempts
// (attempt N waits (N+1)*base). Kept intentionally short so retries add
// resilience without stalling the agent loop.
const toolRetryBaseDelay = 20 * time.Millisecond

// toolPanic wraps a recovered panic value so the retry loop can (a) tell a
// panic apart from an ordinary error to shape the right message and (b) treat
// it as terminal (never retryable).
type toolPanic struct{ value any }

func (p toolPanic) Error() string { return "panic" }

// isRetryableToolError reports whether a non-nil error returned by a tool's
// Execute is a TRANSIENT failure worth retrying. Transient means a temporary
// IO/network/timeout condition that may succeed on a fresh attempt:
//
//   - syscall.ETIMEDOUT / ECONNRESET / EAGAIN
//   - a net.Error whose Timeout() or Temporary() is true
//   - context.DeadlineExceeded (a per-attempt/inner deadline; the caller
//     separately refuses to retry when the OUTER ctx is already done)
//   - os.ErrDeadlineExceeded (i/o deadline)
//   - error text containing "connection refused" / "temporarily unavailable" /
//     "i/o timeout" / "connection reset"
//
// Everything else is TERMINAL and must not be retried: argument/validation
// errors, file-not-found, and — importantly — context.Canceled, which always
// means "stop", never "try again". A recovered panic (toolPanic) is terminal
// too.
func isRetryableToolError(err error) bool {
	if err == nil {
		return false
	}
	// Cancellation is always terminal, even if some inner cause looks transient.
	if errors.Is(err, context.Canceled) {
		return false
	}
	// A recovered panic is a programming error, never transient.
	var tp toolPanic
	if errors.As(err, &tp) {
		return false
	}

	// Deadline / timeout sentinels.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	// Transient syscall errnos.
	if errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EAGAIN) {
		return true
	}
	// net.Error temporary/timeout conditions.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() || netErr.Temporary() {
			return true
		}
	}
	// Best-effort string fallbacks for errors that lost their typed cause.
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"connection refused",
		"temporarily unavailable",
		"i/o timeout",
		"connection reset",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// toolRetryCap resolves the effective retry cap from the config field,
// mirroring the sentinel convention used by MaxResultBytes: 0 => default
// (maxToolRetries), <0 => disabled (0 retries, i.e. a single attempt).
func toolRetryCap(cfgMax int) int {
	if cfgMax == 0 {
		return maxToolRetries
	}
	if cfgMax < 0 {
		return 0
	}
	return cfgMax
}

// waitToolRetryBackoff sleeps a small, attempt-scaled delay before the next
// attempt, but returns early (false) if ctx is cancelled/expired during the
// wait so a dead context never costs the full backoff.
func waitToolRetryBackoff(ctx context.Context, attempt int) bool {
	d := time.Duration(attempt+1) * toolRetryBaseDelay
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
