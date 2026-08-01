// Memory/checkpoint/compaction config: the nested TOML tables [memory],
// [checkpoint], and compaction.max_context (spec-persistent-memory-infinite-
// context §3/§4/§5.2). This file is pure config plumbing — parse, defaults, and
// resolve helpers only. The actual memory Store, checkpoint persistence, and
// compaction trigger live in later layers (internal/memory, internal/runtime,
// internal/compaction) and consume the resolved values overlaid in cmd/pigo.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Built-in defaults for the [memory] table. Exposed so overlay and tests share
// one source of truth (mirrors the flat-config defaults documented in main.go).
const (
	DefaultMemoryEnabled           = true
	DefaultMemoryReconcileOnSearch = true
	DefaultMemorySearchScoreFloor  = 0.15
	DefaultMemoryCCIndex           = false
)

// DefaultCheckpointThresholds is the built-in compaction trigger ladder as
// percentage strings; ResolveThresholds parses them into fractions in (0,1].
func DefaultCheckpointThresholds() []string {
	return []string{"40%", "60%", "80%"}
}

// MemoryConfig is the [memory] TOML table. Bool fields whose default is true
// (Enabled, ReconcileOnSearch) and the float ScoreFloor use pointers so an
// absent key is distinguishable from an explicit false/0 — nil means "apply the
// default", which is what makes memory.enabled=false representable and
// default-safe. CCIndex defaults to false, so a plain bool suffices.
type MemoryConfig struct {
	Enabled           *bool    `toml:"enabled"`
	ReconcileOnSearch *bool    `toml:"reconcile_on_search"`
	SearchScoreFloor  *float64 `toml:"search_score_floor"`
	CCIndex           bool     `toml:"cc_index"`
}

// ResolvedMemory is MemoryConfig with defaults applied and the score floor
// clamped to [0,1]. It is the shape downstream memory wiring consumes.
type ResolvedMemory struct {
	Enabled           bool
	ReconcileOnSearch bool
	SearchScoreFloor  float64
	CCIndex           bool
}

// Resolve applies the [memory] defaults: absent keys fall back to
// true/true/0.15/false; an explicit search_score_floor outside [0,1] is clamped
// into range.
func (m MemoryConfig) Resolve() ResolvedMemory {
	r := ResolvedMemory{
		Enabled:           DefaultMemoryEnabled,
		ReconcileOnSearch: DefaultMemoryReconcileOnSearch,
		SearchScoreFloor:  DefaultMemorySearchScoreFloor,
		CCIndex:           m.CCIndex,
	}
	if m.Enabled != nil {
		r.Enabled = *m.Enabled
	}
	if m.ReconcileOnSearch != nil {
		r.ReconcileOnSearch = *m.ReconcileOnSearch
	}
	if m.SearchScoreFloor != nil {
		f := *m.SearchScoreFloor
		switch {
		case f < 0:
			f = 0
		case f > 1:
			f = 1
		}
		r.SearchScoreFloor = f
	}
	return r
}

// IntOrString accepts either a TOML integer or string for the optional
// [checkpoint].reserved key (e.g. reserved = 4096 or reserved = "10%"). Set
// reports whether the key was present; IsInt selects the populated field.
type IntOrString struct {
	Set   bool
	IsInt bool
	Int   int
	Str   string
}

// UnmarshalTOML implements toml.Unmarshaler so a bare int or a quoted string
// both decode without failing the whole file.
func (v *IntOrString) UnmarshalTOML(data any) error {
	v.Set = true
	switch t := data.(type) {
	case int64:
		v.IsInt, v.Int = true, int(t)
	case int:
		v.IsInt, v.Int = true, t
	case float64:
		v.IsInt, v.Int = true, int(t)
	case string:
		v.Str = t
	default:
		return fmt.Errorf("reserved: unsupported type %T (want int or string)", data)
	}
	return nil
}

// CheckpointConfig is the [checkpoint] TOML table. push_caps is a nested table
// of per-section token caps (e.g. [checkpoint.push_caps] with memory = 800,
// recall = 1200), modeled as a map.
type CheckpointConfig struct {
	Thresholds []string       `toml:"thresholds"`
	Reserved   IntOrString    `toml:"reserved"`
	PushCaps   map[string]int `toml:"push_caps"`
}

// ResolveThresholds parses the configured threshold percentage strings into
// fractions in (0,1], skipping out-of-range/unparseable entries. An empty list
// — or one where every entry is invalid — falls back to the built-in defaults.
func (c CheckpointConfig) ResolveThresholds() []float64 {
	src := c.Thresholds
	if len(src) == 0 {
		src = DefaultCheckpointThresholds()
	}
	out := parseThresholds(src)
	if len(out) == 0 {
		out = parseThresholds(DefaultCheckpointThresholds())
	}
	return out
}

func parseThresholds(ss []string) []float64 {
	var out []float64
	for _, s := range ss {
		if f, ok := ParseThresholdFraction(s); ok {
			out = append(out, f)
		}
	}
	return out
}

// ParseThresholdFraction parses a percentage string like "80%" into a fraction
// in (0,1]. Values outside that range (<=0, >100%) or lacking a % suffix are
// rejected with ok=false so callers fall back to a default.
func ParseThresholdFraction(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, "%") {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
	if err != nil {
		return 0, false
	}
	f := n / 100
	if f <= 0 || f > 1 {
		return 0, false
	}
	return f, true
}

// CompactionConfig is the [compaction] TOML table. Only max_context is wired
// here; it lowers the auto-compaction trigger point and is always clamped by
// the provider window by the consumer.
type CompactionConfig struct {
	MaxContext string `toml:"max_context"`
}

// ResolveMaxContext parses the max_context string form. An empty value yields
// an unset MaxContext (no error).
func (c CompactionConfig) ResolveMaxContext() (MaxContext, error) {
	return ParseMaxContext(c.MaxContext)
}

// MaxContext is a parsed compaction.max_context value: either an absolute token
// count or a fraction of the provider window. The zero value is "unset" and
// Resolve returns 0. Resolve is a pure function; the provider-limit clamp is
// applied by the consumer.
type MaxContext struct {
	set      bool
	fraction float64 // >0 for a "N%" form
	tokens   int     // absolute token count when fraction == 0
}

// IsSet reports whether max_context was configured.
func (m MaxContext) IsSet() bool { return m.set }

// Resolve returns the token budget for the given provider window: window*
// fraction (rounded) for a percentage form, or the absolute token count
// otherwise. An unset value returns 0. This is intentionally unclamped — the
// consumer applies the provider-limit clamp.
func (m MaxContext) Resolve(window int) int {
	if !m.set {
		return 0
	}
	if m.fraction > 0 {
		return int(float64(window)*m.fraction + 0.5)
	}
	return m.tokens
}

// ParseMaxContext parses the accepted max_context forms: a plain token count
// ("300000"), a K/M-suffixed count ("300K", "1M", case-insensitive, fractions
// allowed like "1.5M"), or a percentage of the provider window ("50%"). An
// empty string is unset (no error); other malformed or non-positive values are
// errors.
func ParseMaxContext(s string) (MaxContext, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return MaxContext{}, nil
	}
	if strings.HasSuffix(s, "%") {
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
		if err != nil {
			return MaxContext{}, fmt.Errorf("max_context: invalid percent %q: %w", s, err)
		}
		f := n / 100
		if f <= 0 || f > 1 {
			return MaxContext{}, fmt.Errorf("max_context: percent out of range %q (want (0%%,100%%])", s)
		}
		return MaxContext{set: true, fraction: f}, nil
	}
	mult := 1.0
	body := s
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		mult, body = 1_000, s[:len(s)-1]
	case 'm', 'M':
		mult, body = 1_000_000, s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(body), 64)
	if err != nil {
		return MaxContext{}, fmt.Errorf("max_context: invalid token count %q: %w", s, err)
	}
	if n <= 0 {
		return MaxContext{}, fmt.Errorf("max_context: must be positive %q", s)
	}
	return MaxContext{set: true, tokens: int(n*mult + 0.5)}, nil
}

// MemorySettings bundles the resolved [memory]/[checkpoint]/[compaction] config
// for overlay into runtime options: defaults applied, string forms pre-parsed.
// It is produced by FileConfig.ResolveMemorySettings and is always well-formed
// (an invalid max_context is treated as unset rather than failing the overlay).
type MemorySettings struct {
	Memory               ResolvedMemory
	CheckpointThresholds []float64
	CheckpointReserved   IntOrString
	CheckpointPushCaps   map[string]int
	MaxContext           MaxContext
}

// ResolveMemorySettings resolves the three nested tables into MemorySettings,
// applying defaults. It never fails: an unparseable compaction.max_context is
// dropped to unset so a single bad key cannot break config overlay.
func (c FileConfig) ResolveMemorySettings() MemorySettings {
	mc, _ := c.Compaction.ResolveMaxContext()
	return MemorySettings{
		Memory:               c.Memory.Resolve(),
		CheckpointThresholds: c.Checkpoint.ResolveThresholds(),
		CheckpointReserved:   c.Checkpoint.Reserved,
		CheckpointPushCaps:   c.Checkpoint.PushCaps,
		MaxContext:           mc,
	}
}
