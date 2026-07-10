package provider

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func strp(s string) *string { return &s }

// TestResolveThinkingThreeState covers the three-state map semantics: supported
// with a wire value, supported-but-disabled (nil value), and unsupported
// (absent key / nil map).
func TestResolveThinkingThreeState(t *testing.T) {
	m := agentcore.ThinkingLevelMap{
		agentcore.ThinkingHigh: strp("high-effort"),
		agentcore.ThinkingLow:  nil, // supported but disabled
	}
	if wire, ok := ResolveThinking(m, agentcore.ThinkingHigh); !ok || wire != "high-effort" {
		t.Errorf("high: got (%q, %v), want (high-effort, true)", wire, ok)
	}
	if wire, ok := ResolveThinking(m, agentcore.ThinkingLow); !ok || wire != "" {
		t.Errorf("low (nil value): got (%q, %v), want (\"\", true)", wire, ok)
	}
	if _, ok := ResolveThinking(m, agentcore.ThinkingXHigh); ok {
		t.Error("xhigh (absent key) must be unsupported")
	}
	if _, ok := ResolveThinking(nil, agentcore.ThinkingHigh); ok {
		t.Error("nil map must be unsupported")
	}
}

// TestClampReasoning verifies xhigh clamps down to the highest supported level.
func TestClampReasoning(t *testing.T) {
	m := agentcore.ThinkingLevelMap{
		agentcore.ThinkingLow:    strp("l"),
		agentcore.ThinkingMedium: strp("m"),
		agentcore.ThinkingHigh:   strp("h"),
	}
	// xhigh unsupported → clamps to high.
	if lvl, ok := clampReasoning(m, agentcore.ThinkingXHigh); !ok || lvl != agentcore.ThinkingHigh {
		t.Errorf("clamp xhigh: got (%v, %v), want (high, true)", lvl, ok)
	}
	// medium supported → itself.
	if lvl, ok := clampReasoning(m, agentcore.ThinkingMedium); !ok || lvl != agentcore.ThinkingMedium {
		t.Errorf("clamp medium: got (%v, %v), want (medium, true)", lvl, ok)
	}
	// nil map → not found.
	if _, ok := clampReasoning(nil, agentcore.ThinkingHigh); ok {
		t.Error("clamp on nil map must report not found")
	}
	// map without any level ≤ requested → not found.
	only := agentcore.ThinkingLevelMap{agentcore.ThinkingXHigh: strp("x")}
	if _, ok := clampReasoning(only, agentcore.ThinkingLow); ok {
		t.Error("clamp low with only-xhigh map must report not found")
	}
}

// TestTranslateAnthropicThinking covers off, disabled-tier, budget fallback, and
// unsupported model.
func TestTranslateAnthropicThinking(t *testing.T) {
	m := agentcore.ThinkingLevelMap{
		agentcore.ThinkingLow:    strp("l"),
		agentcore.ThinkingMedium: strp("m"),
		agentcore.ThinkingHigh:   strp("h"),
	}
	if got := TranslateAnthropicThinking(m, agentcore.ThinkingOff); got.Enabled {
		t.Errorf("off must be disabled, got %+v", got)
	}
	// xhigh clamps to high → high budget.
	if got := TranslateAnthropicThinking(m, agentcore.ThinkingXHigh); !got.Enabled || got.BudgetTokens != defaultBudgets[agentcore.ThinkingHigh] {
		t.Errorf("xhigh: got %+v, want enabled with high budget", got)
	}
	// medium → medium budget.
	if got := TranslateAnthropicThinking(m, agentcore.ThinkingMedium); !got.Enabled || got.BudgetTokens != defaultBudgets[agentcore.ThinkingMedium] {
		t.Errorf("medium: got %+v, want medium budget", got)
	}
	// nil map (no thinking support) → disabled.
	if got := TranslateAnthropicThinking(nil, agentcore.ThinkingHigh); got.Enabled {
		t.Errorf("nil map must be disabled, got %+v", got)
	}
	// minimal tier → disabled (no budget).
	mm := agentcore.ThinkingLevelMap{agentcore.ThinkingMinimal: strp("min")}
	if got := TranslateAnthropicThinking(mm, agentcore.ThinkingMinimal); got.Enabled {
		t.Errorf("minimal must be disabled, got %+v", got)
	}
}

// TestTranslateGoogleThinking covers explicit wire, clamp fallback, and off.
func TestTranslateGoogleThinking(t *testing.T) {
	m := agentcore.ThinkingLevelMap{
		agentcore.ThinkingHigh:   strp("HIGH"),
		agentcore.ThinkingMedium: strp("MEDIUM"),
	}
	if got := TranslateGoogleThinking(m, agentcore.ThinkingOff); got.ThinkingLevel != "" {
		t.Errorf("off must be empty, got %+v", got)
	}
	if got := TranslateGoogleThinking(m, agentcore.ThinkingHigh); got.ThinkingLevel != "HIGH" {
		t.Errorf("high: got %+v, want HIGH", got)
	}
	// xhigh clamps to high → HIGH wire.
	if got := TranslateGoogleThinking(m, agentcore.ThinkingXHigh); got.ThinkingLevel != "HIGH" {
		t.Errorf("xhigh clamp: got %+v, want HIGH", got)
	}
	// unsupported model → empty.
	if got := TranslateGoogleThinking(nil, agentcore.ThinkingHigh); got.ThinkingLevel != "" {
		t.Errorf("nil map must be empty, got %+v", got)
	}
}
