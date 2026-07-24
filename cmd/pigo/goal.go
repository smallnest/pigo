// This file implements the /goal command (对标 pi-goal / Claude Code's goal
// mode): given a high-level objective, pigo runs the agent autonomously —
// re-prompting it turn after turn from the loop's follow-up seam — until the
// model declares the goal done (goal_complete), reports a true impasse
// (goal_blocked), or a safety guard (max turns / no-progress) or the token
// budget stops it.
//
// /goal is intercepted in the REPL loop (see repl.go) rather than routed through
// a slash Action closure because it must run agent streams and mutate the shared
// context and goal state — none of which a pure string→string Action can do,
// exactly like /compact and /fork.
//
// Scope: core autonomous continuation plus an optional token budget. Goal state
// lives only in the session's in-memory GoalState (deps.goal); it is not
// persisted across process restarts.
package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// goalMaxAutomaticTurns caps how many autonomous continuations a single /goal
// run will issue before pausing, a runaway guard (对标 pi-goal's automaticTurns).
const goalMaxAutomaticTurns = 25

// goalMaxNoProgress pauses the run after this many consecutive tool-free
// continuations, so a model looping without acting cannot spin forever (对标
// pi-goal's noProgressTurns).
const goalMaxNoProgress = 3

// runGoal parses and dispatches a /goal invocation. setCancel publishes the
// active run's cancel func so the REPL's SIGINT handler can interrupt an
// autonomous run (same plumbing as a normal turn).
func runGoal(setCancel func(context.CancelFunc), out io.Writer, deps *replDeps, line string) {
	args := strings.TrimSpace(strings.TrimPrefix(line, "/goal"))

	switch {
	case args == "":
		printGoalStatus(out, deps.goal)
		return
	case args == "clear":
		deps.goal.Clear()
		fmt.Fprintln(out, "goal cleared")
		return
	case args == "pause":
		snap := deps.goal.Snapshot()
		if snap.Status != agenttool.GoalActive && snap.Status != agenttool.GoalPaused {
			fmt.Fprintln(out, "no active goal to pause")
			return
		}
		deps.goal.SetStatus(agenttool.GoalPaused)
		fmt.Fprintln(out, "goal paused — run /goal resume to continue")
		return
	case args == "resume":
		snap := deps.goal.Snapshot()
		if snap.Status != agenttool.GoalPaused && snap.Status != agenttool.GoalBudgetLimited {
			fmt.Fprintln(out, "no paused goal to resume")
			return
		}
		deps.goal.Resume()
		fmt.Fprintf(out, "resuming goal: %s\n", oneLine(snap.Objective))
		runGoalLoop(setCancel, out, deps)
		return
	}

	// Otherwise args is a new objective, optionally prefixed with --tokens N.
	objective, budget, err := parseGoalObjective(args)
	if err != nil {
		fmt.Fprintf(out, "pigo: %v\n", err)
		return
	}
	if strings.TrimSpace(objective) == "" {
		fmt.Fprintln(out, "usage: /goal [--tokens N] <objective>")
		return
	}
	deps.goal.Start(newGoalID(), objective, budget)
	if budget > 0 {
		fmt.Fprintf(out, "goal set (token budget %d): %s\n", budget, oneLine(objective))
	} else {
		fmt.Fprintf(out, "goal set: %s\n", oneLine(objective))
	}
	runGoalLoop(setCancel, out, deps)
}

// newGoalID returns a short unique id for a goal (used to key goal_complete's
// exact-id contract in a future extension; here it just labels the goal).
func newGoalID() string { return "goal-" + strconv.FormatInt(time.Now().UnixNano(), 36) }

// parseGoalObjective splits an optional leading "--tokens N" flag from the
// objective text. N accepts a bare integer or a k/m suffix (100k, 1m). The flag
// must lead; anything after it (or the whole string when absent) is the
// objective.
func parseGoalObjective(args string) (objective string, budget int, err error) {
	rest := args
	if strings.HasPrefix(rest, "--tokens") {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "--tokens"))
		// The value is the next whitespace-delimited token.
		var valTok string
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			valTok = rest[:i]
			rest = strings.TrimSpace(rest[i+1:])
		} else {
			valTok = rest
			rest = ""
		}
		budget, err = parseTokenBudget(valTok)
		if err != nil {
			return "", 0, err
		}
	}
	return rest, budget, nil
}

// parseTokenBudget parses a token count with an optional k/m (×1000/×1000000)
// suffix, case-insensitive. It rejects a non-positive or malformed value.
func parseTokenBudget(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("--tokens requires a value (e.g. --tokens 100k)")
	}
	mult := 1
	switch {
	case strings.HasSuffix(s, "k"):
		mult = 1000
		s = strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult = 1000000
		s = strings.TrimSuffix(s, "m")
	}
	n, convErr := strconv.Atoi(s)
	if convErr != nil || n <= 0 {
		return 0, fmt.Errorf("invalid --tokens value %q (want a positive number, optionally with k/m)", s)
	}
	return n * mult, nil
}

// goalContinuationPrompt is the follow-up injected each autonomous turn to keep
// the model working toward the goal. The objective itself is re-stated every
// turn by the GoalReminderProvider, so this only nudges continuation.
const goalContinuationPrompt = "Continue working toward the goal. When every requirement is " +
	"verifiably met, call goal_complete with a summary. If you hit a true impasse you cannot work " +
	"around, call goal_blocked with concrete evidence. Otherwise keep going — do not stop or ask " +
	"the user whether to continue."

// goalFollowUpDecision decides, from a goal snapshot, whether the autonomous
// loop should issue another continuation turn. It is pure so the branch logic
// (complete/blocked terminal states, token budget, turn cap, no-progress guard)
// can be unit-tested without running a real agent stream. The returned status is
// the terminal status to record when cont is false (GoalIdle means "leave the
// status as-is" — used for the already-terminal complete/blocked cases).
func goalFollowUpDecision(snap agenttool.GoalSnapshot) (cont bool, terminal agenttool.GoalStatus) {
	switch snap.Status {
	case agenttool.GoalComplete, agenttool.GoalBlocked:
		// A goal tool already ended the run; nothing to continue.
		return false, agenttool.GoalIdle
	}
	if snap.TokenBudget > 0 && snap.TokensUsed >= snap.TokenBudget {
		return false, agenttool.GoalBudgetLimited
	}
	if snap.Iterations >= goalMaxAutomaticTurns {
		return false, agenttool.GoalPaused
	}
	if snap.NoProgress >= goalMaxNoProgress {
		return false, agenttool.GoalPaused
	}
	return true, agenttool.GoalIdle
}

// runGoalLoop drives the autonomous goal run. It assembles a run just like
// streamRun but with the goal tools (goal_complete/goal_blocked) added, the goal
// reminder wired alongside the todo reminder, and a GetFollowUpMessages hook that
// re-prompts the model each time the inner loop settles — until a goal tool ends
// the run or goalFollowUpDecision trips a guard. On return it prints the outcome
// and persists the turn. The run reuses the REPL's SIGINT cancel plumbing via
// setCancel.
func runGoalLoop(setCancel func(context.CancelFunc), out io.Writer, deps *replDeps) {
	goalReg := goalToolRegistry(deps.reg, deps.goal)
	reminders := goalReminders(deps.reg, deps.goal)

	runCtx, cancel := context.WithCancel(context.Background())
	setCancel(cancel)
	defer func() {
		cancel()
		setCancel(nil)
	}()

	// lastSeen tracks how many messages we have already accounted for, so each
	// settle folds in only the assistant turns produced since the previous one:
	// their output tokens (budget) and whether any tool ran (no-progress guard).
	lastSeen := len(deps.agentCtx.Messages)

	cfg := runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:         deps.live.model,
			Provider:      deps.live.providerName,
			ThinkingLevel: deps.live.thinkingLevel,
			Stream:        provider.StreamFnFromProvider(deps.live.provider),
			GetAPIKey:     deps.creds.GetAPIKey,
			ContextWindow: deps.live.contextWindow,
			Compaction:    compaction.DefaultCompactionSettings,
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{
				Registry:       goalReg,
				BeforeToolCall: trustBeforeToolCall(deps.trust, deps.cwd, deps.in, out, deps.confirmMu),
			},
		},
		Reminders: reminders,
		GetFollowUpMessages: func(ctx context.Context, agentCtx *agentcore.AgentContext) []agentcore.AgentMessage {
			// Account for the turns produced since the last settle. Auto-compaction
			// can shrink agentCtx.Messages in place (summary + tail) between settles,
			// dropping its length below lastSeen; clamp so the slice never goes out
			// of bounds. The compacted turn's tokens are then under-counted, which is
			// acceptable for a soft budget guard (a crash is not).
			if lastSeen > len(agentCtx.Messages) {
				lastSeen = len(agentCtx.Messages)
			}
			outputTokens, hadTool := goalTurnActivity(agentCtx.Messages[lastSeen:])
			lastSeen = len(agentCtx.Messages)
			deps.goal.RecordIteration(outputTokens, hadTool)

			cont, terminal := goalFollowUpDecision(deps.goal.Snapshot())
			if !cont {
				if terminal != agenttool.GoalIdle {
					deps.goal.SetStatus(terminal)
				}
				return nil
			}
			return []agentcore.AgentMessage{agentcore.UserMessage{
				RoleField: agentcore.RoleUser,
				Content:   agentcore.ContentList{agentcore.NewTextContent(goalContinuationPrompt)},
			}}
		},
	}

	// The first turn is driven by the goal reminder alone (the objective is
	// injected as background context); no explicit user prompt is appended so the
	// objective is not duplicated in the durable history.
	stream := runtime.StartRun(runCtx, deps.agentCtx, cfg)
	drainGoalStream(runCtx, out, deps, stream)

	printGoalOutcome(out, deps.goal.Snapshot())
	persistTurn(out, deps)
}

// goalToolRegistry returns a registry holding every tool from base plus the two
// goal-control tools, so the autonomous run can invoke goal_complete/goal_blocked
// while keeping all the normal tools available. The base registry is left
// unchanged (the goal tools are only present for the goal run).
func goalToolRegistry(base *agenttool.ToolRegistry, state *agenttool.GoalState) *agenttool.ToolRegistry {
	reg := agenttool.NewToolRegistry()
	if base != nil {
		for _, t := range base.List() {
			_ = reg.Register(t)
		}
	}
	_ = reg.Register(&agenttool.GoalCompleteTool{State: state})
	_ = reg.Register(&agenttool.GoalBlockedTool{State: state})
	return reg
}

// goalReminders builds the per-turn reminder registry for a goal run: the goal
// reminder (re-stating the objective every turn) plus the todo reminder when a
// todo tool is present, so an autonomous run keeps both its objective and its
// task list in view.
func goalReminders(base *agenttool.ToolRegistry, state *agenttool.GoalState) *runtime.ReminderRegistry {
	reg := runtime.NewReminderRegistry(&runtime.GoalReminderProvider{State: state})
	if base != nil {
		if t, ok := base.Get("todo"); ok {
			if tt, ok := t.(*agenttool.TodoTool); ok && tt.Store != nil {
				reg.Register(&runtime.TodoReminderProvider{Store: tt.Store})
			}
		}
	}
	return reg
}

// goalTurnActivity sums the output tokens across the assistant messages in tail
// and reports whether any tool ran in that window (an assistant tool call or a
// tool result). It feeds RecordIteration's token-budget and no-progress inputs.
func goalTurnActivity(tail []agentcore.AgentMessage) (outputTokens int, hadTool bool) {
	for _, m := range tail {
		switch msg := m.(type) {
		case agentcore.AssistantMessage:
			if msg.Usage != nil {
				outputTokens += msg.Usage.OutputTokens
			}
			if len(msg.ToolCalls()) > 0 {
				hadTool = true
			}
		case agentcore.ToolResultMessage:
			hadTool = true
		}
	}
	return outputTokens, hadTool
}

// drainGoalStream prints the streamed assistant text and tool activity of a goal
// run to out, mirroring streamRun's rendering. It blocks until the run ends.
func drainGoalStream(ctx context.Context, out io.Writer, deps *replDeps, stream *runtime.LoopEventStream) {
	var reply strings.Builder
	flushReply := func() {
		if reply.Len() == 0 {
			return
		}
		rendered := renderMarkdown(reply.String())
		fmt.Fprint(out, rendered)
		if !strings.HasSuffix(rendered, "\n") {
			fmt.Fprintln(out)
		}
		reply.Reset()
	}
	_, err := runtime.DrainStream(ctx, stream, runtime.StreamHandler{
		OnEvent: deps.notifierHandle(),
		OnText: func(delta string) {
			reply.WriteString(delta)
		},
		OnTurnEnd: func(msg agentcore.AssistantMessage, results []agentcore.ToolResultMessage) {
			flushReply()
			for _, c := range msg.ToolCalls() {
				fmt.Fprintf(out, "  %s %s\n", colorize(colorEnabled(), ansiGreen, "→ tool:"), toolCallLabel(c))
			}
			for _, tr := range results {
				renderToolResult(out, tr)
			}
		},
	})
	flushReply()
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(out, "^C interrupted — goal paused (run /goal resume to continue)")
			deps.goal.SetStatus(agenttool.GoalPaused)
		} else {
			fmt.Fprintf(out, "error: %v\n", err)
		}
	}
}

// printGoalOutcome prints a one-line result banner after a goal run settles,
// keyed on the terminal status the run reached.
func printGoalOutcome(out io.Writer, snap agenttool.GoalSnapshot) {
	color := colorEnabled()
	switch snap.Status {
	case agenttool.GoalComplete:
		fmt.Fprintf(out, "%s goal complete: %s\n", colorize(color, ansiGreen, "✓"), snap.Summary)
	case agenttool.GoalBlocked:
		fmt.Fprintf(out, "%s goal blocked: %s\n", colorize(color, ansiRed, "⚠"), snap.BlockReason)
	case agenttool.GoalBudgetLimited:
		fmt.Fprintf(out, "%s goal paused — token budget reached (%d / %d). Run /goal resume to continue.\n",
			colorize(color, ansiYellow, "⏸"), snap.TokensUsed, snap.TokenBudget)
	case agenttool.GoalPaused:
		fmt.Fprintf(out, "%s goal paused after %d turns. Run /goal resume to continue, or /goal clear to drop it.\n",
			colorize(color, ansiYellow, "⏸"), snap.Iterations)
	}
}

// printGoalStatus prints a summary of the current goal state for a bare /goal.
func printGoalStatus(out io.Writer, goal *agenttool.GoalState) {
	snap := goal.Snapshot()
	if snap.Status == agenttool.GoalIdle {
		fmt.Fprintln(out, "no goal set — run /goal <objective> to start one")
		return
	}
	fmt.Fprintf(out, "goal:       %s\n", snap.Objective)
	fmt.Fprintf(out, "status:     %s\n", snap.Status)
	fmt.Fprintf(out, "iterations: %d\n", snap.Iterations)
	if snap.TokenBudget > 0 {
		fmt.Fprintf(out, "tokens:     %d / %d\n", snap.TokensUsed, snap.TokenBudget)
	} else {
		fmt.Fprintf(out, "tokens:     %d (no budget)\n", snap.TokensUsed)
	}
	if !snap.StartedAt.IsZero() {
		fmt.Fprintf(out, "elapsed:    %s\n", time.Since(snap.StartedAt).Round(time.Second))
	}
	if snap.Summary != "" {
		fmt.Fprintf(out, "summary:    %s\n", snap.Summary)
	}
	if snap.BlockReason != "" {
		fmt.Fprintf(out, "blocked:    %s\n", snap.BlockReason)
	}
}
