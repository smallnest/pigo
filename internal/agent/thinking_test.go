package agent

import "testing"

func strp(s string) *string { return &s }

// TestResolveThinkingThreeState covers the three-state map semantics: supported
// with a wire value, supported-but-disabled (nil value), and unsupported
// (absent key / nil map).
func TestResolveThinkingThreeState(t *testing.T) {
	m := ThinkingLevelMap{
		ThinkingHigh: strp("high-effort"),
		ThinkingLow:  nil, // supported but disabled
	}
	if wire, ok := ResolveThinking(m, ThinkingHigh); !ok || wire != "high-effort" {
		t.Errorf("high: got (%q, %v), want (high-effort, true)", wire, ok)
	}
	if wire, ok := ResolveThinking(m, ThinkingLow); !ok || wire != "" {
		t.Errorf("low (nil value): got (%q, %v), want (\"\", true)", wire, ok)
	}
	if _, ok := ResolveThinking(m, ThinkingXHigh); ok {
		t.Error("xhigh (absent key) must be unsupported")
	}
	if _, ok := ResolveThinking(nil, ThinkingHigh); ok {
		t.Error("nil map must be unsupported")
	}
}

// TestClampReasoning verifies xhigh clamps down to the highest supported level.
func TestClampReasoning(t *testing.T) {
	m := ThinkingLevelMap{
		ThinkingLow:    strp("l"),
		ThinkingMedium: strp("m"),
		ThinkingHigh:   strp("h"),
	}
	// xhigh unsupported → clamps to high.
	if lvl, ok := clampReasoning(m, ThinkingXHigh); !ok || lvl != ThinkingHigh {
		t.Errorf("clamp xhigh: got (%v, %v), want (high, true)", lvl, ok)
	}
	// medium supported → itself.
	if lvl, ok := clampReasoning(m, ThinkingMedium); !ok || lvl != ThinkingMedium {
		t.Errorf("clamp medium: got (%v, %v), want (medium, true)", lvl, ok)
	}
	// nil map → not found.
	if _, ok := clampReasoning(nil, ThinkingHigh); ok {
		t.Error("clamp on nil map must report not found")
	}
	// map without any level ≤ requested → not found.
	only := ThinkingLevelMap{ThinkingXHigh: strp("x")}
	if _, ok := clampReasoning(only, ThinkingLow); ok {
		t.Error("clamp low with only-xhigh map must report not found")
	}
}

// TestTranslateAnthropicThinking covers off, disabled-tier, budget fallback, and
// unsupported model.
func TestTranslateAnthropicThinking(t *testing.T) {
	m := ThinkingLevelMap{
		ThinkingLow:    strp("l"),
		ThinkingMedium: strp("m"),
		ThinkingHigh:   strp("h"),
	}
	if got := TranslateAnthropicThinking(m, ThinkingOff); got.Enabled {
		t.Errorf("off must be disabled, got %+v", got)
	}
	// xhigh clamps to high → high budget.
	if got := TranslateAnthropicThinking(m, ThinkingXHigh); !got.Enabled || got.BudgetTokens != defaultBudgets[ThinkingHigh] {
		t.Errorf("xhigh: got %+v, want enabled with high budget", got)
	}
	// medium → medium budget.
	if got := TranslateAnthropicThinking(m, ThinkingMedium); !got.Enabled || got.BudgetTokens != defaultBudgets[ThinkingMedium] {
		t.Errorf("medium: got %+v, want medium budget", got)
	}
	// nil map (no thinking support) → disabled.
	if got := TranslateAnthropicThinking(nil, ThinkingHigh); got.Enabled {
		t.Errorf("nil map must be disabled, got %+v", got)
	}
	// minimal tier → disabled (no budget).
	mm := ThinkingLevelMap{ThinkingMinimal: strp("min")}
	if got := TranslateAnthropicThinking(mm, ThinkingMinimal); got.Enabled {
		t.Errorf("minimal must be disabled, got %+v", got)
	}
}

// TestTranslateGoogleThinking covers explicit wire, clamp fallback, and off.
func TestTranslateGoogleThinking(t *testing.T) {
	m := ThinkingLevelMap{
		ThinkingHigh:   strp("HIGH"),
		ThinkingMedium: strp("MEDIUM"),
	}
	if got := TranslateGoogleThinking(m, ThinkingOff); got.ThinkingLevel != "" {
		t.Errorf("off must be empty, got %+v", got)
	}
	if got := TranslateGoogleThinking(m, ThinkingHigh); got.ThinkingLevel != "HIGH" {
		t.Errorf("high: got %+v, want HIGH", got)
	}
	// xhigh clamps to high → HIGH wire.
	if got := TranslateGoogleThinking(m, ThinkingXHigh); got.ThinkingLevel != "HIGH" {
		t.Errorf("xhigh clamp: got %+v, want HIGH", got)
	}
	// unsupported model → empty.
	if got := TranslateGoogleThinking(nil, ThinkingHigh); got.ThinkingLevel != "" {
		t.Errorf("nil map must be empty, got %+v", got)
	}
}
