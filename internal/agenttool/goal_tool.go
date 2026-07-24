// This file implements the goal state and the two goal-control tools that power
// the /goal command (对标 pi-goal / Claude Code's goal mode): given a high-level
// objective, the agent runs autonomously — re-prompted turn after turn — until
// it either declares the goal done (goal_complete), hits a true impasse
// (goal_blocked), or a safety guard / token budget stops it.
//
// The tools live here (rather than in the REPL) because they are ordinary
// AgentTools the model invokes, and because the runtime's GoalReminderProvider
// needs to read the same state — mirroring how TodoTool/TodoStore pairs with
// TodoReminderProvider. GoalState is the shared, concurrency-safe handle both
// the tools (which may run in a batch) and the REPL/reminder (which reads it
// each turn) touch.
package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// GoalStatus is the lifecycle state of the active goal.
type GoalStatus string

const (
	// GoalIdle means no goal is set (the zero value).
	GoalIdle GoalStatus = ""
	// GoalActive means the agent is autonomously working toward the goal.
	GoalActive GoalStatus = "active"
	// GoalPaused means autonomous continuation stopped (a safety guard fired, or
	// the user paused it); it can be resumed.
	GoalPaused GoalStatus = "paused"
	// GoalBlocked means the agent hit a true impasse (goal_blocked was called).
	GoalBlocked GoalStatus = "blocked"
	// GoalComplete means the agent declared the goal done (goal_complete).
	GoalComplete GoalStatus = "complete"
	// GoalBudgetLimited means the token budget was exhausted before completion.
	GoalBudgetLimited GoalStatus = "budget_limited"
)

// GoalState holds the current goal for a REPL session. It is safe for concurrent
// use so the goal tools (which may run in a batch) and the REPL/reminder reader
// can touch it without racing. A single state is shared for a session's
// lifetime; /goal clear resets it to the idle zero value.
type GoalState struct {
	mu sync.RWMutex

	id          string
	objective   string
	summary     string // set by goal_complete
	blockReason string // set by goal_blocked
	status      GoalStatus

	iterations int // autonomous continuations issued so far
	noProgress int // consecutive settles with no tool activity

	tokenBudget int // 0 = unlimited
	tokensUsed  int

	startedAt time.Time
}

// NewGoalState returns an empty (idle) goal state.
func NewGoalState() *GoalState { return &GoalState{} }

// GoalSnapshot is an immutable copy of the goal state for display/decisions.
type GoalSnapshot struct {
	ID          string
	Objective   string
	Summary     string
	BlockReason string
	Status      GoalStatus
	Iterations  int
	NoProgress  int
	TokenBudget int
	TokensUsed  int
	StartedAt   time.Time
}

// Start (re)initializes the state for a new objective, moving it to active. It
// resets all counters so a fresh goal never inherits a prior goal's tallies.
func (s *GoalState) Start(id, objective string, tokenBudget int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
	s.objective = objective
	s.summary = ""
	s.blockReason = ""
	s.status = GoalActive
	s.iterations = 0
	s.noProgress = 0
	s.tokenBudget = tokenBudget
	s.tokensUsed = 0
	s.startedAt = time.Now()
}

// Snapshot returns a copy of the current state, safe to read without a lock.
func (s *GoalState) Snapshot() GoalSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return GoalSnapshot{
		ID:          s.id,
		Objective:   s.objective,
		Summary:     s.summary,
		BlockReason: s.blockReason,
		Status:      s.status,
		Iterations:  s.iterations,
		NoProgress:  s.noProgress,
		TokenBudget: s.tokenBudget,
		TokensUsed:  s.tokensUsed,
		StartedAt:   s.startedAt,
	}
}

// Clear resets the state to idle (no goal). It zeroes the fields individually
// rather than replacing the whole struct so the embedded mutex (currently held)
// is preserved — overwriting it while locked would corrupt the lock.
func (s *GoalState) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = ""
	s.objective = ""
	s.summary = ""
	s.blockReason = ""
	s.status = GoalIdle
	s.iterations = 0
	s.noProgress = 0
	s.tokenBudget = 0
	s.tokensUsed = 0
	s.startedAt = time.Time{}
}

// SetStatus transitions the goal to a new status (used by the REPL when a safety
// guard fires or the user pauses/resumes).
func (s *GoalState) SetStatus(status GoalStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// Resume reactivates a paused or budget-limited goal and clears the transient
// safety-guard counters (iterations, no-progress) that stopped it, so the run
// gets a fresh allowance rather than immediately re-tripping the same guard. The
// token budget is intentionally reset too (tokensUsed → 0): resuming past an
// exhausted budget is an explicit user decision to grant another window. The
// objective and id are preserved. It is a no-op-safe wrapper — callers gate on
// the current status before invoking it.
func (s *GoalState) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = GoalActive
	s.iterations = 0
	s.noProgress = 0
	s.tokensUsed = 0
}

// ID returns the current goal id (empty when idle).
func (s *GoalState) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// RecordIteration increments the autonomous-continuation counter and folds the
// given output-token delta into the running total. hadToolActivity resets the
// no-progress counter when true, else increments it — so a run of tool-free
// turns can trip the no-progress guard.
func (s *GoalState) RecordIteration(outputTokens int, hadToolActivity bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.iterations++
	s.tokensUsed += outputTokens
	if hadToolActivity {
		s.noProgress = 0
	} else {
		s.noProgress++
	}
}

// MarkComplete records the completion summary and moves the goal to complete.
func (s *GoalState) MarkComplete(summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summary = summary
	s.status = GoalComplete
}

// MarkBlocked records the block reason and moves the goal to blocked.
func (s *GoalState) MarkBlocked(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockReason = reason
	s.status = GoalBlocked
}

// terminate is the shared *bool=true value returned by both goal tools to end
// the run immediately (the loop terminates when every result in a batch has
// Terminate=true; a goal tool is expected to be the sole call in its turn).
func terminatePtr() *bool { b := true; return &b }

// contradictorySummary reports whether a goal_complete summary plainly claims the
// goal is NOT done — a guard against the model closing a goal it just admitted
// is unfinished. The check is a conservative substring match on well-known
// negative phrasings (English + 中文), matching pi-goal's "plainly contradictory
// summary" rejection.
func contradictorySummary(summary string) bool {
	lower := strings.ToLower(summary)
	for _, bad := range []string{
		"not complete",
		"not done",
		"incomplete",
		"tests still fail",
		"tests fail",
		"still failing",
		"could not",
		"couldn't",
		"unable to",
		"未完成",
		"没有完成",
		"未能",
		"无法完成",
		"仍然失败",
		"测试失败",
	} {
		if strings.Contains(lower, bad) || strings.Contains(summary, bad) {
			return true
		}
	}
	return false
}

// GoalCompleteTool lets the model declare the active goal finished. It records
// the summary, moves the state to complete, and terminates the run.
type GoalCompleteTool struct {
	// State is the session goal state. Must be non-nil.
	State *GoalState
}

type goalCompleteArgs struct {
	Summary string `json:"summary"`
}

// Name implements AgentTool.
func (t *GoalCompleteTool) Name() string { return "goal_complete" }

// Description implements AgentTool.
func (t *GoalCompleteTool) Description() string {
	return "Declare the current goal COMPLETE. Call this ONLY after verifying, " +
		"requirement by requirement, that the objective is fully met — treat the " +
		"working tree, tests, and actual runtime behavior as authoritative, not " +
		"the prior conversation. Provide a concise summary of what was accomplished. " +
		"Do NOT call this if any requirement is unmet, tests fail, or work remains; " +
		"use goal_blocked for a true impasse instead. Calling this ends the run."
}

// Schema implements AgentTool.
func (t *GoalCompleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": {"type": "string", "description": "Concise summary of what was accomplished to satisfy the goal."}
  },
  "required": ["summary"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. It mutates shared goal state → sequential.
func (t *GoalCompleteTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute implements AgentTool. It validates the summary (non-empty and not
// plainly contradictory), records completion, and terminates the run. Invalid
// input degrades to an error result (not a Go error) so the model can retry.
func (t *GoalCompleteTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[goalCompleteArgs](args, "goal_complete")
	if bad != nil {
		return *bad, nil
	}
	if t.State == nil {
		return errorResult("goal_complete: no active goal"), nil
	}
	summary := strings.TrimSpace(a.Summary)
	if summary == "" {
		return errorResult("goal_complete: summary must not be empty"), nil
	}
	if contradictorySummary(summary) {
		return errorResult("goal_complete: summary indicates the goal is NOT complete; " +
			"keep working, or call goal_blocked with evidence if truly stuck"), nil
	}
	snap := t.State.Snapshot()
	if snap.Status == GoalIdle {
		return errorResult("goal_complete: no active goal"), nil
	}
	t.State.MarkComplete(summary)
	return agentcore.AgentToolResult{
		Content:   agentcore.ContentList{agentcore.NewTextContent("Goal marked complete: " + summary)},
		Terminate: terminatePtr(),
	}, nil
}

// GoalBlockedTool lets the model report a true impasse it cannot work around. It
// records the reason, moves the state to blocked, and terminates the run.
type GoalBlockedTool struct {
	// State is the session goal state. Must be non-nil.
	State *GoalState
}

type goalBlockedArgs struct {
	Reason   string `json:"reason"`
	Evidence string `json:"evidence"`
}

// Name implements AgentTool.
func (t *GoalBlockedTool) Name() string { return "goal_blocked" }

// Description implements AgentTool.
func (t *GoalBlockedTool) Description() string {
	return "Report that the current goal is BLOCKED by a true impasse you cannot " +
		"resolve (e.g. missing credentials, an external dependency you cannot " +
		"install, contradictory requirements). Provide a concrete reason and the " +
		"evidence that establishes the blocker. Use this only as a last resort — " +
		"prefer trying a different approach first. Calling this ends the run."
}

// Schema implements AgentTool.
func (t *GoalBlockedTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "reason":   {"type": "string", "description": "Concise statement of what blocks the goal."},
    "evidence": {"type": "string", "description": "Concrete evidence establishing the blocker (error output, missing file, etc.)."}
  },
  "required": ["reason", "evidence"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. It mutates shared goal state → sequential.
func (t *GoalBlockedTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute implements AgentTool. It validates the reason/evidence, records the
// block, and terminates the run.
func (t *GoalBlockedTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[goalBlockedArgs](args, "goal_blocked")
	if bad != nil {
		return *bad, nil
	}
	if t.State == nil {
		return errorResult("goal_blocked: no active goal"), nil
	}
	reason := strings.TrimSpace(a.Reason)
	if reason == "" {
		return errorResult("goal_blocked: reason must not be empty"), nil
	}
	if strings.TrimSpace(a.Evidence) == "" {
		return errorResult("goal_blocked: evidence must not be empty"), nil
	}
	snap := t.State.Snapshot()
	if snap.Status == GoalIdle {
		return errorResult("goal_blocked: no active goal"), nil
	}
	t.State.MarkBlocked(reason)
	return agentcore.AgentToolResult{
		Content:   agentcore.ContentList{agentcore.NewTextContent("Goal marked blocked: " + reason)},
		Terminate: terminatePtr(),
	}, nil
}
