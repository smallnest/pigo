// Tests for the goal tools (对标 pi-goal): goal_complete validation (empty and
// contradictory summaries are rejected, a valid summary marks complete and
// terminates the run), goal_blocked validation, and GoalState counter/lifecycle
// behavior. Mirrors todo_tool_test.go's structure.
package agenttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// execGoalComplete runs the goal_complete tool with the given JSON args.
func execGoalComplete(t *testing.T, tool *GoalCompleteTool, args string) agentcore.AgentToolResult {
	t.Helper()
	res, err := tool.Execute(context.Background(), "call-1", json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	return res
}

func isErrorResult(res agentcore.AgentToolResult) bool {
	// An error result carries no Terminate and its text is the error message;
	// the tools return errorResult(...) which has Terminate=nil.
	return res.Terminate == nil
}

func TestGoalToolsRegister(t *testing.T) {
	reg := NewToolRegistry()
	st := NewGoalState()
	if err := reg.Register(&GoalCompleteTool{State: st}); err != nil {
		t.Fatalf("Register goal_complete: %v", err)
	}
	if err := reg.Register(&GoalBlockedTool{State: st}); err != nil {
		t.Fatalf("Register goal_blocked: %v", err)
	}
	if _, ok := reg.Get("goal_complete"); !ok {
		t.Fatal("goal_complete not found after Register")
	}
	if _, ok := reg.Get("goal_blocked"); !ok {
		t.Fatal("goal_blocked not found after Register")
	}
}

func TestGoalCompleteValidSummary(t *testing.T) {
	st := NewGoalState()
	st.Start("g1", "do the thing", 0)
	tool := &GoalCompleteTool{State: st}

	res := execGoalComplete(t, tool, `{"summary":"created hello.txt with the requested contents"}`)
	if res.Terminate == nil || !*res.Terminate {
		t.Fatalf("expected Terminate=true, got %v", res.Terminate)
	}
	if snap := st.Snapshot(); snap.Status != GoalComplete {
		t.Errorf("status = %q, want complete", snap.Status)
	}
}

func TestGoalCompleteRejectsEmpty(t *testing.T) {
	st := NewGoalState()
	st.Start("g1", "do the thing", 0)
	tool := &GoalCompleteTool{State: st}

	res := execGoalComplete(t, tool, `{"summary":"   "}`)
	if !isErrorResult(res) {
		t.Fatal("expected error result for empty summary")
	}
	if snap := st.Snapshot(); snap.Status != GoalActive {
		t.Errorf("status = %q, want still active after rejected summary", snap.Status)
	}
}

func TestGoalCompleteRejectsContradictory(t *testing.T) {
	st := NewGoalState()
	st.Start("g1", "do the thing", 0)
	tool := &GoalCompleteTool{State: st}

	for _, summary := range []string{
		`{"summary":"the goal is not complete yet"}`,
		`{"summary":"tests still fail but I stopped"}`,
		`{"summary":"任务未完成"}`,
	} {
		res := execGoalComplete(t, tool, summary)
		if !isErrorResult(res) {
			t.Fatalf("expected error result for contradictory summary %s", summary)
		}
	}
	if snap := st.Snapshot(); snap.Status != GoalActive {
		t.Errorf("status = %q, want still active", snap.Status)
	}
}

func TestGoalBlockedRecordsReason(t *testing.T) {
	st := NewGoalState()
	st.Start("g1", "do the thing", 0)
	tool := &GoalBlockedTool{State: st}

	res, err := tool.Execute(context.Background(), "c1",
		json.RawMessage(`{"reason":"missing API key","evidence":"env AUTH_TOKEN is empty"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Terminate == nil || !*res.Terminate {
		t.Fatalf("expected Terminate=true, got %v", res.Terminate)
	}
	snap := st.Snapshot()
	if snap.Status != GoalBlocked {
		t.Errorf("status = %q, want blocked", snap.Status)
	}
	if snap.BlockReason != "missing API key" {
		t.Errorf("block reason = %q", snap.BlockReason)
	}
}

func TestGoalBlockedRequiresEvidence(t *testing.T) {
	st := NewGoalState()
	st.Start("g1", "do the thing", 0)
	tool := &GoalBlockedTool{State: st}

	res, err := tool.Execute(context.Background(), "c1",
		json.RawMessage(`{"reason":"stuck","evidence":""}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !isErrorResult(res) {
		t.Fatal("expected error result when evidence is empty")
	}
	if snap := st.Snapshot(); snap.Status != GoalActive {
		t.Errorf("status = %q, want still active", snap.Status)
	}
}

func TestGoalStateRecordIteration(t *testing.T) {
	st := NewGoalState()
	st.Start("g1", "obj", 1000)

	st.RecordIteration(100, true) // tool activity resets no-progress
	st.RecordIteration(50, false) // no tool activity
	st.RecordIteration(50, false)

	snap := st.Snapshot()
	if snap.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", snap.Iterations)
	}
	if snap.TokensUsed != 200 {
		t.Errorf("tokensUsed = %d, want 200", snap.TokensUsed)
	}
	if snap.NoProgress != 2 {
		t.Errorf("noProgress = %d, want 2", snap.NoProgress)
	}
	if snap.TokenBudget != 1000 {
		t.Errorf("tokenBudget = %d, want 1000", snap.TokenBudget)
	}
}

func TestGoalStateClear(t *testing.T) {
	st := NewGoalState()
	st.Start("g1", "obj", 0)
	st.Clear()
	if snap := st.Snapshot(); snap.Status != GoalIdle || snap.Objective != "" {
		t.Errorf("after Clear: status=%q objective=%q, want idle/empty", snap.Status, snap.Objective)
	}
}

func TestGoalStateResume(t *testing.T) {
	st := NewGoalState()
	st.Start("g1", "obj", 1000)
	// Simulate a run that tripped a safety guard.
	st.RecordIteration(500, false)
	st.RecordIteration(500, false)
	st.SetStatus(GoalPaused)

	st.Resume()
	snap := st.Snapshot()
	if snap.Status != GoalActive {
		t.Errorf("status = %q, want active after Resume", snap.Status)
	}
	if snap.Iterations != 0 || snap.NoProgress != 0 || snap.TokensUsed != 0 {
		t.Errorf("Resume should clear transient counters: iterations=%d noProgress=%d tokensUsed=%d",
			snap.Iterations, snap.NoProgress, snap.TokensUsed)
	}
	if snap.Objective != "obj" || snap.TokenBudget != 1000 {
		t.Errorf("Resume should preserve objective/budget: objective=%q budget=%d", snap.Objective, snap.TokenBudget)
	}
}
