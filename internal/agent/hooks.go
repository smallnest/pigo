package agent

// ThinkingLevel is the unified reasoning-effort enum (agent layer). It keeps
// pi's full 6 levels; providers map it to their own wire format via a
// per-model ThinkingLevelMap (decision #10).
type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
)

// ThinkingLevelMap maps a unified level to a provider-specific wire value.
// A nil value means "supported but disabled at this level"; an absent key means
// "this level is not supported by the model". The pointer is what distinguishes
// those two cases, so it must stay *string.
type ThinkingLevelMap map[ThinkingLevel]*string

// AfterToolCallResult is the optional override returned by the afterToolCall
// hook. Every field is a pointer so the loop can distinguish "not provided"
// (nil) from "provided, possibly zero" — pi expresses this with `??`, Go needs
// pointers. Fields are applied with field-level replacement, no deep merge
// (FR-5).
type AfterToolCallResult struct {
	Content   *ContentList
	Details   *any
	Terminate *bool
	IsError   *bool
}

// AgentLoopTurnUpdate is the optional result of the prepareNextTurn hook: it can
// swap the context, model, or thinking level for the next turn. Pointer fields
// distinguish "not provided" from an explicit value; ThinkingLevel is
// three-state (nil = keep, &"off" = disable, &level = set).
type AgentLoopTurnUpdate struct {
	Context       *AgentContext
	Model         *string
	ThinkingLevel *ThinkingLevel
}
