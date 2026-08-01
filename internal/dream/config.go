// Package dream implements the /dream memory-consolidation feature's foundation
// layer: the resolved [dream] configuration, on-disk run state, and the
// deterministic due-check that decides whether an auto-trigger is warranted.
// This layer has no LLM dependency; the runner, scheduler, lock, and plan/apply
// logic live in later nodes. See tasks/spec-dream-memory-consolidation.md
// §3.2/§3.3/§5.1/§5.4.
package dream

// Built-in defaults for the [dream] table, exposed so config normalization and
// tests share one source of truth.
const (
	DefaultEnabled        = true
	DefaultIntervalDays   = 7
	DefaultRecentSessions = 20
)

// Config is the resolved [dream] configuration with defaults applied. It is the
// shape the scheduler and runner consume, distinct from the raw
// config.DreamConfig (which uses *bool / zero to distinguish "unset").
type Config struct {
	Enabled        bool
	IntervalDays   int
	RecentSessions int
}

// NewConfig normalizes a raw [dream] table into a Config, applying defaults: a
// nil enabled pointer means true (only an explicit false disables); a
// non-positive interval_days falls back to 7; a non-positive recent_sessions
// falls back to 20. A missing [dream] table is representable as the zero
// arguments (nil, 0, 0) and yields all defaults, so parsing never errors on an
// absent table.
func NewConfig(enabled *bool, intervalDays, recentSessions int) Config {
	c := Config{
		Enabled:        DefaultEnabled,
		IntervalDays:   intervalDays,
		RecentSessions: recentSessions,
	}
	if enabled != nil {
		c.Enabled = *enabled
	}
	if c.IntervalDays <= 0 {
		c.IntervalDays = DefaultIntervalDays
	}
	if c.RecentSessions <= 0 {
		c.RecentSessions = DefaultRecentSessions
	}
	return c
}
