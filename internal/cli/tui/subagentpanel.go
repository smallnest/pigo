package tui

import (
	"fmt"
	"strings"
	"time"
)

// This file renders the multi-line sub-agent status panel (SPEC 4.4, US-006): a
// block shown just above the working spinner while one or more sub-agents
// dispatched by the `task` tool are running. Each active sub-agent contributes
// exactly one line of the form:
//
//	⏺ {desc} · {activity} ({elapsed} · ↓{tokens})
//
// The panel is a pure function of the model's ordered active-subagent set; it is
// re-rendered every spinner tick so the elapsed clock stays live without a
// dedicated timer. When there are no active sub-agents it renders nothing (zero
// lines, zero height), leaving the existing single-run layout untouched.

// subagentRow is one live sub-agent's status, keyed by the parent task tool-call
// id. start is recorded when the row is added so elapsed can be computed at
// render time; activity/tokens are refreshed by subagentProgressMsg.
type subagentRow struct {
	id       string
	desc     string
	activity string
	tokens   int
	start    time.Time
}

// subagentPanel is the ordered set of live sub-agents. order preserves insertion
// order (so rows render stably, oldest first) while byID gives O(1) lookup for
// progress updates and removal. The zero value is a valid empty panel.
type subagentPanel struct {
	order []string
	byID  map[string]*subagentRow
}

// add records a newly dispatched sub-agent (a toolStartMsg with name=="task").
// It is idempotent on the id: a duplicate start refreshes the description and
// resets the start clock rather than adding a second row.
func (p *subagentPanel) add(id, desc string, now time.Time) {
	if p.byID == nil {
		p.byID = make(map[string]*subagentRow)
	}
	if row, ok := p.byID[id]; ok {
		row.desc = desc
		row.start = now
		return
	}
	p.byID[id] = &subagentRow{id: id, desc: desc, start: now}
	p.order = append(p.order, id)
}

// update folds a progress event into the row for id, refreshing its activity and
// token estimate. A progress for an unknown id (late/out-of-order, arriving
// before or without a start) adds the row so no update is lost; now seeds its
// start clock in that case.
func (p *subagentPanel) update(id, desc, activity string, tokens int, now time.Time) {
	if p.byID == nil {
		p.byID = make(map[string]*subagentRow)
	}
	row, ok := p.byID[id]
	if !ok {
		row = &subagentRow{id: id, desc: desc, start: now}
		p.byID[id] = row
		p.order = append(p.order, id)
	}
	if activity != "" {
		row.activity = activity
	}
	if desc != "" {
		row.desc = desc
	}
	row.tokens = tokens
}

// remove drops the row for id (the task's toolEndMsg). It is a no-op when id is
// absent, so an end without a matching start — or a duplicate end — is safe.
func (p *subagentPanel) remove(id string) {
	if _, ok := p.byID[id]; !ok {
		return
	}
	delete(p.byID, id)
	for i, v := range p.order {
		if v == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
}

// active reports the number of live sub-agents (rows the panel would render).
func (p *subagentPanel) active() int { return len(p.order) }

// view renders the panel to a string, one line per active sub-agent in insertion
// order, each truncated to width display columns. It returns "" when there are
// no active sub-agents or width is non-positive, so an empty panel contributes
// zero rows and zero height. now is the reference time elapsed is measured from
// (the spinner tick's time) so the clock advances each frame.
func (p subagentPanel) view(theme Theme, width int, now time.Time) string {
	if len(p.order) == 0 || width <= 0 {
		return ""
	}
	lines := make([]string, 0, len(p.order))
	for _, id := range p.order {
		row := p.byID[id]
		if row == nil {
			continue
		}
		lines = append(lines, TruncateToWidth(row.render(theme, now), width))
	}
	return strings.Join(lines, "\n")
}

// render builds one status line for a row: "⏺ {desc} · {activity} ({elapsed} ·
// ↓{tokens})". A blank description is omitted (the line leads with the glyph and
// activity); a zero token estimate drops the "↓" stat. The glyph + head take the
// accent color and the parenthetical stats are dim, mirroring the spinner line.
func (r subagentRow) render(theme Theme, now time.Time) string {
	var head strings.Builder
	head.WriteString("⏺")
	if r.desc != "" {
		fmt.Fprintf(&head, " %s ·", r.desc)
	}
	fmt.Fprintf(&head, " %s", r.activity)

	stats := formatElapsed(now.Sub(r.start))
	if r.tokens > 0 {
		stats += " · ↓" + humanizeInt(r.tokens)
	}
	return theme.Spinner.Render(head.String()) + " " + theme.System.Render("("+stats+")")
}
