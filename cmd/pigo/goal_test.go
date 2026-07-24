// Tests for the /goal command's pure helpers: goalFollowUpDecision's branch
// logic (terminal states, token budget, turn cap, no-progress guard),
// parseGoalObjective / parseTokenBudget flag parsing, and goalTurnActivity's
// token/tool accounting.
package main

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
)

func TestGoalFollowUpDecision(t *testing.T) {
	tests := []struct {
		name     string
		snap     agenttool.GoalSnapshot
		wantCont bool
		wantTerm agenttool.GoalStatus
	}{
		{
			name:     "active continues",
			snap:     agenttool.GoalSnapshot{Status: agenttool.GoalActive, Iterations: 1},
			wantCont: true,
			wantTerm: agenttool.GoalIdle,
		},
		{
			name:     "complete stops",
			snap:     agenttool.GoalSnapshot{Status: agenttool.GoalComplete},
			wantCont: false,
			wantTerm: agenttool.GoalIdle,
		},
		{
			name:     "blocked stops",
			snap:     agenttool.GoalSnapshot{Status: agenttool.GoalBlocked},
			wantCont: false,
			wantTerm: agenttool.GoalIdle,
		},
		{
			name:     "budget exhausted",
			snap:     agenttool.GoalSnapshot{Status: agenttool.GoalActive, TokenBudget: 100, TokensUsed: 100},
			wantCont: false,
			wantTerm: agenttool.GoalBudgetLimited,
		},
		{
			name:     "budget under limit continues",
			snap:     agenttool.GoalSnapshot{Status: agenttool.GoalActive, TokenBudget: 100, TokensUsed: 50},
			wantCont: true,
			wantTerm: agenttool.GoalIdle,
		},
		{
			name:     "turn cap",
			snap:     agenttool.GoalSnapshot{Status: agenttool.GoalActive, Iterations: goalMaxAutomaticTurns},
			wantCont: false,
			wantTerm: agenttool.GoalPaused,
		},
		{
			name:     "no progress guard",
			snap:     agenttool.GoalSnapshot{Status: agenttool.GoalActive, NoProgress: goalMaxNoProgress},
			wantCont: false,
			wantTerm: agenttool.GoalPaused,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cont, term := goalFollowUpDecision(tt.snap)
			if cont != tt.wantCont {
				t.Errorf("cont = %v, want %v", cont, tt.wantCont)
			}
			if term != tt.wantTerm {
				t.Errorf("terminal = %q, want %q", term, tt.wantTerm)
			}
		})
	}
}

func TestParseGoalObjective(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		wantObj string
		wantBud int
		wantErr bool
	}{
		{name: "no flag", args: "create hello.txt", wantObj: "create hello.txt", wantBud: 0},
		{name: "tokens k", args: "--tokens 100k build the thing", wantObj: "build the thing", wantBud: 100000},
		{name: "tokens m", args: "--tokens 1m do it", wantObj: "do it", wantBud: 1000000},
		{name: "tokens bare", args: "--tokens 5000 go", wantObj: "go", wantBud: 5000},
		{name: "flag only no objective", args: "--tokens 50k", wantObj: "", wantBud: 50000},
		{name: "bad value", args: "--tokens abc do it", wantErr: true},
		{name: "zero value", args: "--tokens 0 do it", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, bud, err := parseGoalObjective(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got obj=%q bud=%d", obj, bud)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if obj != tt.wantObj {
				t.Errorf("objective = %q, want %q", obj, tt.wantObj)
			}
			if bud != tt.wantBud {
				t.Errorf("budget = %d, want %d", bud, tt.wantBud)
			}
		})
	}
}

func TestGoalTurnActivity(t *testing.T) {
	tail := []agentcore.AgentMessage{
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Usage:     &agentcore.Usage{OutputTokens: 40},
		},
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Usage:     &agentcore.Usage{OutputTokens: 60},
		},
	}
	tokens, hadTool := goalTurnActivity(tail)
	if tokens != 100 {
		t.Errorf("tokens = %d, want 100", tokens)
	}
	if hadTool {
		t.Error("hadTool = true, want false (no tool calls or results)")
	}

	withTool := []agentcore.AgentMessage{
		agentcore.ToolResultMessage{ToolName: "bash"},
	}
	_, hadTool = goalTurnActivity(withTool)
	if !hadTool {
		t.Error("hadTool = false, want true (tool result present)")
	}
}
