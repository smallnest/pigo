// This file implements the session-local reminder scheduler (issue #565,
// ported from dsh's "Schedule session-local reminders"): three model-invocable
// tools (schedule_create / schedule_list / schedule_delete) over an in-memory
// store bound to one session, plus the delivery seam that turns due reminders
// into follow-up user turns.
//
// Semantics (mirroring dsh, scoped to pigo's session model):
//
//   - Session-local. The store lives in memory for one run assembly; nothing
//     is persisted and a session's end drops every reminder.
//   - Two kinds. One-shot (an absolute `at` instant) and fixed-rate repeating
//     (`every_seconds` >= 300); the two are mutually exclusive.
//   - Strict time parsing. `at` must be a strict RFC 3339 date-time with Z or
//     a numeric offset, or be built from {date, time, time_zone} with
//     time_zone being "UTC" or an IANA Area/Location ("Local" is rejected).
//     DST spring-forward gaps are rejected; fall-back overlaps choose the
//     first instant (Go's time.Date semantics). Stored targets are normalized
//     to UTC.
//   - Delivery. When the run is otherwise idle (the outer loop is about to
//     settle), each due reminder is queued as one normal user turn via the
//     GetFollowUpMessages seam and marked delivered. A repeating reminder's
//     next fire is its last delivery (or creation) plus the interval.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	// Embedded IANA tz database: goreleaser ships windows binaries where the
	// system zone database does not exist, and schedule_create's time_zone
	// parsing needs real zone rules (DST gaps/overlaps) on every platform.
	_ "time/tzdata"

	"github.com/smallnest/pigo/internal/agentcore"
)

// minScheduleIntervalSeconds is the floor for a fixed-rate interval, matching
// dsh's schedule overlay: fine-grained timers belong to cron, not the agent.
const minScheduleIntervalSeconds = 300

// ScheduleReminder is one session-local reminder. Exactly one of the one-shot
// (At) and repeating (Interval) shapes is set.
type ScheduleReminder struct {
	ID        string        `json:"id"`
	Prompt    string        `json:"prompt"`
	OneShot   bool          `json:"oneShot"`
	At        time.Time     `json:"at,omitempty"`              // UTC; one-shot only
	Interval  time.Duration `json:"intervalSeconds,omitempty"` // > 0 repeating only
	CreatedAt time.Time     `json:"createdAt"`                 // UTC
	LastFired time.Time     `json:"lastFired,omitempty"`       // UTC; zero = never
	Delivered bool          `json:"delivered"`                 // one-shot only
}

// Status computes the reminder's delivery status for schedule_list: scheduled
// (in the future), overdue (target passed, not yet delivered), delivered
// (one-shot fired), or repeating.
func (r ScheduleReminder) Status(now time.Time) string {
	switch {
	case r.OneShot && r.Delivered:
		return "delivered"
	case r.OneShot && !r.At.After(now):
		return "overdue"
	case !r.OneShot:
		return "repeating"
	default:
		return "scheduled"
	}
}

// NextFire returns the reminder's next fire instant (UTC), or the zero time
// when nothing will fire again (a delivered one-shot).
func (r ScheduleReminder) NextFire() time.Time {
	if r.OneShot {
		if r.Delivered {
			return time.Time{}
		}
		return r.At
	}
	base := r.LastFired
	if base.IsZero() {
		base = r.CreatedAt
	}
	return base.Add(r.Interval)
}

// Schedule is the in-memory store of session-local reminders. It is safe for
// concurrent use: the executor may run schedule_* tool calls in a parallel
// batch while the loop goroutine consults delivery.
type Schedule struct {
	// Now is the clock consulted for due-ness and creation stamps. It is a
	// field rather than a hidden func so tests can pin it (mirrors
	// TokenSource.Now); nil falls back to the wall clock.
	Now func() time.Time

	mu        sync.Mutex
	nextID    int
	reminders []*ScheduleReminder
}

// NewSchedule builds an empty scheduler using the wall clock. Tests inject a
// fixed clock through the Now field.
func NewSchedule() *Schedule {
	return &Schedule{Now: time.Now}
}

// clock returns the current instant from the injected or wall clock.
func (s *Schedule) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Create adds a reminder. One-shot: at non-nil. Repeating: interval >= 300s.
// The prompt must be non-empty. Exactly one shape applies.
func (s *Schedule) Create(prompt string, at *time.Time, interval time.Duration) (*ScheduleReminder, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("prompt must be non-empty")
	}
	if at != nil && interval != 0 {
		return nil, fmt.Errorf("choose either an absolute at target or every_seconds, not both")
	}
	if at == nil && interval == 0 {
		return nil, fmt.Errorf("one of at or every_seconds is required")
	}
	if interval != 0 && interval < minScheduleIntervalSeconds*time.Second {
		return nil, fmt.Errorf("every_seconds must be >= %d", minScheduleIntervalSeconds)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	r := &ScheduleReminder{
		ID:        fmt.Sprintf("sched-%d", s.nextID),
		Prompt:    prompt,
		CreatedAt: s.clock().UTC(),
	}
	if at != nil {
		r.OneShot = true
		r.At = at.UTC()
	} else {
		r.Interval = interval
	}
	s.reminders = append(s.reminders, r)
	return r, nil
}

// Delete removes the reminder with the given id, reporting whether it existed.
func (s *Schedule) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.reminders {
		if r.ID == id {
			s.reminders = append(s.reminders[:i], s.reminders[i+1:]...)
			return true
		}
	}
	return false
}

// List returns a snapshot of the reminders in creation order.
func (s *Schedule) List() []ScheduleReminder {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ScheduleReminder, len(s.reminders))
	for i, r := range s.reminders {
		out[i] = *r
	}
	return out
}

// FollowUpMessages implements the RunConfig.GetFollowUpMessages delivery seam:
// when the run is about to settle, every due reminder is returned as one normal
// user turn (in creation order) and marked delivered. A nil result means
// nothing is due, so the run ends as usual.
func (s *Schedule) FollowUpMessages(_ context.Context, _ *agentcore.AgentContext) []agentcore.AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	var msgs []agentcore.AgentMessage
	for _, r := range s.reminders {
		if !s.dueLocked(r, now) {
			continue
		}
		if r.OneShot {
			r.Delivered = true
		} else {
			r.LastFired = now
		}
		msgs = append(msgs, agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(r.Prompt)},
		})
	}
	return msgs
}

// dueLocked reports whether r should be delivered at now. Caller holds mu.
func (s *Schedule) dueLocked(r *ScheduleReminder, now time.Time) bool {
	if r.OneShot {
		return !r.Delivered && !r.At.After(now)
	}
	base := r.LastFired
	if base.IsZero() {
		base = r.CreatedAt
	}
	return !now.Before(base.Add(r.Interval))
}

// textResult builds a success AgentToolResult carrying a single text block.
func textResult(msg string) agentcore.AgentToolResult {
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(msg)}}
}

// ScheduleFromTools returns the Schedule store backing the schedule_create
// tool in tools, or nil when the scheduler is not wired in (e.g. --no-tools).
// It mirrors MemoryStoreFromTools: front-ends extract the store from the
// assembled tool set to wire delivery, instead of threading it separately.
func ScheduleFromTools(tools []agentcore.AgentTool) *Schedule {
	for _, t := range tools {
		if ct, ok := t.(*scheduleCreateTool); ok {
			return ct.sched
		}
	}
	return nil
}

// ScheduleTools returns the three model-invocable schedule tools sharing s.
func ScheduleTools(s *Schedule) []agentcore.AgentTool {
	return []agentcore.AgentTool{
		&scheduleCreateTool{sched: s},
		&scheduleListTool{sched: s},
		&scheduleDeleteTool{sched: s},
	}
}

type scheduleCreateTool struct{ sched *Schedule }

func (t *scheduleCreateTool) Name() string { return "schedule_create" }

func (t *scheduleCreateTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

func (t *scheduleCreateTool) Description() string {
	return "Create a session-local reminder that will be delivered back to this conversation as a user turn when the agent is idle. Two shapes: an absolute `at` instant (strict RFC 3339 with Z or numeric offset, or date+time+time_zone with an IANA zone), or a fixed-rate `every_seconds` interval (>= 300). Reminders live only for this session and are not persisted."
}

func (t *scheduleCreateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "prompt": {"type": "string", "description": "The reminder text delivered as a follow-up user turn."},
    "at": {"type": "string", "description": "Strict RFC 3339 date-time with Z or numeric offset, e.g. 2026-09-12T15:04:05Z."},
    "date": {"type": "string", "description": "YYYY-MM-DD; use together with time and time_zone."},
    "time": {"type": "string", "description": "HH:MM:SS 24-hour; use together with date and time_zone."},
    "time_zone": {"type": "string", "description": "UTC or an IANA Area/Location such as Asia/Shanghai; 'Local' is not accepted."},
    "every_seconds": {"type": "integer", "description": "Fixed-rate interval in seconds, minimum 300."}
  },
  "required": ["prompt"]
}`)
}

// scheduleCreateArgs is the decoded argument shape for schedule_create.
type scheduleCreateArgs struct {
	Prompt       string `json:"prompt"`
	At           string `json:"at"`
	Date         string `json:"date"`
	Time         string `json:"time"`
	TimeZone     string `json:"time_zone"`
	EverySeconds int    `json:"every_seconds"`
}

func (t *scheduleCreateTool) Execute(_ context.Context, _ string, args json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a scheduleCreateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("invalid arguments: %w", err)
	}
	at, interval, err := parseScheduleTarget(a.At, a.Date, a.Time, a.TimeZone, a.EverySeconds)
	if err != nil {
		return agentcore.AgentToolResult{}, err
	}
	r, err := t.sched.Create(a.Prompt, at, interval)
	if err != nil {
		return agentcore.AgentToolResult{}, err
	}
	var body string
	if r.OneShot {
		body = fmt.Sprintf("scheduled %s (session-local): fires at %s", r.ID, r.At.Format(time.RFC3339))
	} else {
		body = fmt.Sprintf("scheduled %s (session-local): fires every %s starting %s", r.ID, r.Interval, r.NextFire().Format(time.RFC3339))
	}
	return textResult(body), nil
}

type scheduleListTool struct{ sched *Schedule }

func (t *scheduleListTool) Name() string { return "schedule_list" }

func (t *scheduleListTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

func (t *scheduleListTool) Description() string {
	return "List this session's reminders with their status (scheduled, overdue, repeating, delivered) and next fire time. Reminders are session-local: they are never persisted and vanish when the session ends."
}

func (t *scheduleListTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *scheduleListTool) Execute(_ context.Context, _ string, _ json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	reminders := t.sched.List()
	if len(reminders) == 0 {
		return textResult("no session-local reminders"), nil
	}
	now := t.sched.clock()
	var b strings.Builder
	for _, r := range reminders {
		fmt.Fprintf(&b, "%s  %s  %s  next %s  prompt: %s\n",
			r.ID, r.Status(now), r.describeCadence(), r.NextFire().Format(time.RFC3339), r.Prompt)
	}
	return textResult(b.String()), nil
}

type scheduleDeleteTool struct{ sched *Schedule }

func (t *scheduleDeleteTool) Name() string { return "schedule_delete" }

func (t *scheduleDeleteTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

func (t *scheduleDeleteTool) Description() string {
	return "Delete a session-local reminder by id (as reported by schedule_create or schedule_list)."
}

func (t *scheduleDeleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {"id": {"type": "string", "description": "Reminder id, e.g. sched-1."}},
  "required": ["id"]
}`)
}

func (t *scheduleDeleteTool) Execute(_ context.Context, _ string, args json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if !t.sched.Delete(a.ID) {
		return agentcore.AgentToolResult{}, fmt.Errorf("unknown reminder id %q", a.ID)
	}
	return textResult(fmt.Sprintf("deleted %s (session-local)", a.ID)), nil
}

// describeCadence renders the one-shot/repeating shape for list output.
func (r ScheduleReminder) describeCadence() string {
	if r.OneShot {
		return "one-shot"
	}
	return "every " + r.Interval.String()
}

// parseScheduleTarget validates and normalizes the create arguments into
// exactly one schedule shape. at-only callers get a *time.Time; repeating
// callers get a duration. The date/time/time_zone triple is parsed with
// strict layouts against a UTC-or-IANA zone, DST spring-forward gaps are
// rejected, and the result is normalized to UTC.
func parseScheduleTarget(at, date, timeStr, zone string, everySeconds int) (*time.Time, time.Duration, error) {
	hasAt := strings.TrimSpace(at) != ""
	hasTriple := date != "" || timeStr != "" || zone != ""
	hasInterval := everySeconds != 0

	switch {
	case hasAt && hasTriple || hasAt && hasInterval || hasTriple && hasInterval:
		return nil, 0, fmt.Errorf("choose exactly one of at, date+time+time_zone, or every_seconds")
	case hasTriple && (date == "" || timeStr == "" || zone == ""):
		return nil, 0, fmt.Errorf("date, time, and time_zone must be provided together")
	case !hasAt && !hasTriple && !hasInterval:
		return nil, 0, fmt.Errorf("one of at, date+time+time_zone, or every_seconds is required")
	}

	if hasInterval {
		if everySeconds < 0 {
			return nil, 0, fmt.Errorf("every_seconds must be positive")
		}
		if everySeconds < minScheduleIntervalSeconds {
			return nil, 0, fmt.Errorf("every_seconds must be >= %d", minScheduleIntervalSeconds)
		}
		return nil, time.Duration(everySeconds) * time.Second, nil
	}

	if hasAt {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return nil, 0, fmt.Errorf("at must be strict RFC 3339 with Z or a numeric offset: %v", err)
		}
		return &t, 0, nil
	}

	t, err := parseZonedWallClock(date, timeStr, zone)
	if err != nil {
		return nil, 0, err
	}
	return &t, 0, nil
}

// parseZonedWallClock builds an instant from a YYYY-MM-DD date and HH:MM:SS
// wall time in UTC or an IANA zone. A wall time inside a DST spring-forward
// gap is rejected (the zone never observes it); a fall-back overlap resolves
// to the first instant, matching Go's time.Date semantics.
func parseZonedWallClock(date, timeStr, zone string) (time.Time, error) {
	if zone == "Local" {
		return time.Time{}, fmt.Errorf("time_zone must be UTC or an IANA Area/Location, not Local")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, fmt.Errorf("time_zone must be UTC or an IANA Area/Location: %v", err)
	}
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return time.Time{}, fmt.Errorf("date must be YYYY-MM-DD: %v", err)
	}
	clock, err := time.Parse("15:04:05", timeStr)
	if err != nil {
		if clock2, err2 := time.Parse("15:04", timeStr); err2 == nil {
			clock = clock2
		} else {
			return time.Time{}, fmt.Errorf("time must be HH:MM:SS: %v", err)
		}
	}
	y, m, d := day.Date()
	hh, mm, ss := clock.Clock()
	want := fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", y, int(m), d, hh, mm, ss)
	built := time.Date(y, m, d, hh, mm, ss, 0, loc)
	// time.Date normalizes nonexistent gap times forward by their zone offset;
	// round-tripping the wall clock detects the shift.
	if built.In(loc).Format("2006-01-02 15:04:05") != want {
		return time.Time{}, fmt.Errorf("%s %s does not exist in %s (DST gap)", date, timeStr, zone)
	}
	return built.UTC(), nil
}
