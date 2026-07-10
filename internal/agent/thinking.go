// This file implements the three-stage thinking normalization (US-007 support,
// decision #10): a unified ThinkingLevel → per-model ThinkingLevelMap → per-
// provider wire format. ThinkingLevel and ThinkingLevelMap live in hooks.go;
// this file adds the resolution + per-provider translation + budget fallback.
package agent

// thinkingOrder ranks levels from lowest to highest effort. It backs
// clampReasoning and any level-comparison logic.
var thinkingOrder = map[ThinkingLevel]int{
	ThinkingOff:     0,
	ThinkingMinimal: 1,
	ThinkingLow:     2,
	ThinkingMedium:  3,
	ThinkingHigh:    4,
	ThinkingXHigh:   5,
}

// ResolveThinking maps a unified level through a model's ThinkingLevelMap.
//
// Return contract mirrors the map's three-state semantics:
//   - (wire, true)  — level supported; wire is the provider value (may be "" if
//     the map stores an empty string, e.g. "disabled at this level").
//   - ("", false)   — level not supported by this model (absent key), or the map
//     is nil (model does not support thinking at all).
//
// A nil *string value in the map is treated as "supported but disabled": it
// resolves to ("", true).
func ResolveThinking(m ThinkingLevelMap, level ThinkingLevel) (string, bool) {
	if m == nil {
		return "", false
	}
	wire, ok := m[level]
	if !ok {
		return "", false
	}
	if wire == nil {
		return "", true
	}
	return *wire, true
}

// clampReasoning lowers a level to the highest one the model actually supports,
// walking down the effort order. xhigh→high is the common case (decision #10:
// xhigh falls back to high when a model tops out there). Returns the clamped
// level and whether any supported level was found.
func clampReasoning(m ThinkingLevelMap, level ThinkingLevel) (ThinkingLevel, bool) {
	if m == nil {
		return level, false
	}
	if _, ok := m[level]; ok {
		return level, true
	}
	// Walk down from the requested level to the lowest, returning the first
	// supported level (highest supported ≤ requested).
	want := thinkingOrder[level]
	best := ThinkingLevel("")
	bestRank := -1
	for lvl := range m {
		r := thinkingOrder[lvl]
		if r <= want && r > bestRank {
			best, bestRank = lvl, r
		}
	}
	if bestRank < 0 {
		return level, false
	}
	return best, true
}

// defaultBudgets is the fallback token budget per level for providers that
// express thinking as a token budget (e.g. Anthropic budgetTokens) when a model
// does not carry an explicit wire value. off/minimal have no budget.
var defaultBudgets = map[ThinkingLevel]int{
	ThinkingLow:    4096,
	ThinkingMedium: 8192,
	ThinkingHigh:   16384,
	ThinkingXHigh:  32768,
}

// AnthropicThinking is the Anthropic wire shape: either a token budget
// (thinkingEnabled + budgetTokens) or nothing when disabled.
type AnthropicThinking struct {
	Enabled      bool `json:"thinkingEnabled"`
	BudgetTokens int  `json:"budgetTokens,omitempty"`
}

// TranslateAnthropicThinking maps a unified level to Anthropic's wire form. When
// the model's map carries no explicit budget, it falls back to defaultBudgets,
// clamping xhigh→high style down to a supported level first.
func TranslateAnthropicThinking(m ThinkingLevelMap, level ThinkingLevel) AnthropicThinking {
	if level == ThinkingOff {
		return AnthropicThinking{Enabled: false}
	}
	resolved, ok := clampReasoning(m, level)
	if !ok {
		// Model does not support thinking at all → disabled.
		return AnthropicThinking{Enabled: false}
	}
	if resolved == ThinkingOff || resolved == ThinkingMinimal {
		return AnthropicThinking{Enabled: false}
	}
	budget := defaultBudgets[resolved]
	return AnthropicThinking{Enabled: true, BudgetTokens: budget}
}

// GoogleThinking is the Google wire shape: thinkingConfig.thinkingLevel.
type GoogleThinking struct {
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

// TranslateGoogleThinking maps a unified level to Google's thinkingConfig. It
// prefers the model's explicit wire value; when absent it clamps to a supported
// level and uses the unified level string as the wire value.
func TranslateGoogleThinking(m ThinkingLevelMap, level ThinkingLevel) GoogleThinking {
	if level == ThinkingOff {
		return GoogleThinking{}
	}
	if wire, ok := ResolveThinking(m, level); ok {
		return GoogleThinking{ThinkingLevel: wire}
	}
	resolved, ok := clampReasoning(m, level)
	if !ok {
		return GoogleThinking{}
	}
	if wire, ok := ResolveThinking(m, resolved); ok && wire != "" {
		return GoogleThinking{ThinkingLevel: wire}
	}
	return GoogleThinking{ThinkingLevel: string(resolved)}
}
