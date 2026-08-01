package session

// Context inheritance plumbing for the "infinite context" feature (#483, built
// on the header fields added by #480). A session created to *continue* another
// session's collapsed context records two things on its header:
//
//   - ContextFrom: the source session id whose checkpoint holds the collapsed
//     prefix (see runtime.LoadCheckpoint / CheckpointPath).
//   - ContextWatermark: the message index up to which that prefix was collapsed;
//     on resume/fork the runtime (#481) can load the source checkpoint and skip
//     re-summarizing messages [0, ContextWatermark).
//
// This is deliberately side-effect-free header plumbing: it never touches the
// memory dir or the runtime. It sits alongside ParentSession (which records
// lineage) — inheritance additionally records *where the collapsed context came
// from* so a forked/continued session need not re-summarize early context.

// SetContextInheritance records on header that this session continues fromID's
// collapsed context up to watermark. A non-positive watermark or an empty fromID
// clears inheritance (both fields reset to their zero/omitempty state), so the
// header round-trips as a session with no inherited checkpoint. This mirrors how
// ParentSession is a plain header field set at creation time.
func SetContextInheritance(header *SessionHeader, fromID string, watermark int) {
	if header == nil {
		return
	}
	if fromID == "" || watermark <= 0 {
		header.ContextFrom = ""
		header.ContextWatermark = 0
		return
	}
	header.ContextFrom = fromID
	header.ContextWatermark = watermark
}

// ContextInheritance reports the inherited-context source recorded on header:
// the source session id, the watermark, and whether inheritance is set. It is
// the read counterpart to SetContextInheritance — a convenience accessor the
// runtime (#481) uses on resume/fork to decide whether to load the source
// checkpoint. ok is true only when a non-empty source and a positive watermark
// are both present, so a header with only one of the two (a malformed or
// partially written record) reports ok=false rather than a half-configured
// inheritance.
func (h SessionHeader) ContextInheritance() (fromID string, watermark int, ok bool) {
	if h.ContextFrom == "" || h.ContextWatermark <= 0 {
		return "", 0, false
	}
	return h.ContextFrom, h.ContextWatermark, true
}

// HasContextInheritance reports whether header declares inherited collapsed
// context. It is shorthand for the ok return of ContextInheritance.
func (h SessionHeader) HasContextInheritance() bool {
	_, _, ok := h.ContextInheritance()
	return ok
}
