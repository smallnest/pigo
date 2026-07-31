package agentcore

import "context"

// progressEmitterKey is the unexported context key under which a run-level
// progress EmitFunc is stored.
type progressEmitterKey struct{}

// WithProgressEmitter returns a child context carrying emit as the run-level
// progress emitter. The task tool injects the parent loop's EmitFunc here so a
// dispatched sub-agent can surface SubAgentProgressEvent up the parent stream.
func WithProgressEmitter(ctx context.Context, emit EmitFunc) context.Context {
	return context.WithValue(ctx, progressEmitterKey{}, emit)
}

// ProgressEmitterFromContext returns the run-level progress emitter carried by
// ctx, or nil if none was set (in which case callers should skip progress
// reporting rather than panic).
func ProgressEmitterFromContext(ctx context.Context) EmitFunc {
	emit, _ := ctx.Value(progressEmitterKey{}).(EmitFunc)
	return emit
}
