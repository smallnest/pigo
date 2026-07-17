package compaction

import "github.com/smallnest/pigo/internal/agentcore"

// CutPointResult describes the cut selected for compaction, mirroring pi's
// CutPointResult (adapted to pigo's flat message list).
type CutPointResult struct {
	// FirstKeptIndex is the index of the first message retained after
	// compaction; everything before it is summarized.
	FirstKeptIndex int
	// TurnStartIndex is the index of the user message that starts the turn the
	// cut falls inside, or -1 when the cut lands on a clean turn boundary.
	TurnStartIndex int
	// IsSplitTurn reports whether the cut splits an in-progress assistant turn.
	IsSplitTurn bool
}

// isValidCutPoint reports whether a message may serve as a cut point. A cut may
// land on a user or assistant message but never on a toolResult, because a
// toolResult must stay attached to the toolCall that produced it (pi semantics).
func isValidCutPoint(msg agentcore.Message) bool {
	switch msg.Role() {
	case agentcore.RoleUser, agentcore.RoleAssistant:
		return true
	default: // toolResult and any custom kinds are not cuttable.
		return false
	}
}

// findValidCutPoints returns the indices of all messages that may serve as cut
// points, in ascending order.
func findValidCutPoints(msgs []agentcore.Message) []int {
	var pts []int
	for i, m := range msgs {
		if isValidCutPoint(m) {
			pts = append(pts, i)
		}
	}
	return pts
}

// findTurnStartIndex scans backwards from idx to find the user message that
// starts the turn containing idx, returning -1 if none is found.
func findTurnStartIndex(msgs []agentcore.Message, idx int) int {
	for i := idx; i >= 0; i-- {
		if msgs[i].Role() == agentcore.RoleUser {
			return i
		}
	}
	return -1
}

// FindCutPoint finds the compaction cut that keeps approximately
// keepRecentTokens worth of the most recent messages. It accumulates token
// estimates from the newest message backwards; once the retained budget is
// reached it snaps to the nearest valid cut point at or after that message,
// never splitting a toolCall from its toolResult. This mirrors pi's findCutPoint.
//
// When no valid cut point exists, it keeps everything (FirstKeptIndex 0).
func FindCutPoint(msgs []agentcore.Message, keepRecentTokens int) CutPointResult {
	cutPoints := findValidCutPoints(msgs)
	if len(cutPoints) == 0 {
		return CutPointResult{FirstKeptIndex: 0, TurnStartIndex: -1, IsSplitTurn: false}
	}

	// Default to the earliest valid cut point (keep as much as possible) when
	// the retained budget is never reached.
	cutIndex := cutPoints[0]

	accumulated := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		accumulated += EstimateTokens(msgs[i])
		if accumulated >= keepRecentTokens {
			// Snap to the nearest valid cut point at or after i, so the kept
			// window starts on a cuttable boundary.
			for _, c := range cutPoints {
				if c >= i {
					cutIndex = c
					break
				}
			}
			break
		}
	}

	cutOnUser := msgs[cutIndex].Role() == agentcore.RoleUser
	turnStart := -1
	if !cutOnUser {
		turnStart = findTurnStartIndex(msgs, cutIndex)
	}

	return CutPointResult{
		FirstKeptIndex: cutIndex,
		TurnStartIndex: turnStart,
		IsSplitTurn:    !cutOnUser && turnStart != -1,
	}
}
