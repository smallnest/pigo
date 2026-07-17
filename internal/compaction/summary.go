// This file (US-003) covers summarization: the structured prompts, the
// conversation serializer, file-operation extraction, and GenerateSummary,
// which drives a provider stream to turn compacted history into a structured
// checkpoint summary. It mirrors pi's harness/compaction/compaction.ts prompts
// and utils.ts helpers, adapted to pigo's flat message list and Provider stream.
package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

// SUMMARIZATION_SYSTEM_PROMPT instructs the model to only emit the structured
// summary and never continue the conversation. Verbatim from pi.
const SUMMARIZATION_SYSTEM_PROMPT = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

// summarizationPrompt is the first-time summary template (pi's SUMMARIZATION_PROMPT).
const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// updateSummarizationPrompt incorporates new messages into an existing summary
// (pi's UPDATE_SUMMARIZATION_PROMPT).
const updateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// FileOperations accumulates the file paths a compaction range touched, split
// by access kind. Mirrors pi's FileOperations.
type FileOperations struct {
	// Read holds files read but not necessarily modified.
	Read map[string]struct{}
	// Written holds files written by full-file write operations.
	Written map[string]struct{}
	// Edited holds files modified by edit operations.
	Edited map[string]struct{}
}

// NewFileOps returns an empty file-operation accumulator.
func NewFileOps() FileOperations {
	return FileOperations{
		Read:    map[string]struct{}{},
		Written: map[string]struct{}{},
		Edited:  map[string]struct{}{},
	}
}

// extractFileOpsFromMessage adds file operations from an assistant message's
// tool calls to the accumulator, keyed on the tool name (read/write/edit) and
// the string "path" argument. Non-assistant messages are ignored, matching pi.
func extractFileOpsFromMessage(msg agentcore.Message, ops FileOperations) {
	a, ok := msg.(agentcore.AssistantMessage)
	if !ok {
		return
	}
	for _, call := range a.ToolCalls() {
		path := toolCallPath(call.Arguments)
		if path == "" {
			continue
		}
		switch call.Name {
		case "read":
			ops.Read[path] = struct{}{}
		case "write":
			ops.Written[path] = struct{}{}
		case "edit":
			ops.Edited[path] = struct{}{}
		}
	}
}

// toolCallPath extracts a string "path" argument from a tool call's raw
// arguments, returning "" when absent or not a string.
func toolCallPath(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var decoded struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &decoded); err != nil {
		return ""
	}
	return decoded.Path
}

// computeFileLists derives sorted read-only and modified (edited∪written) file
// lists, excluding modified files from the read-only list. Mirrors pi.
func computeFileLists(ops FileOperations) (readFiles, modifiedFiles []string) {
	modified := map[string]struct{}{}
	for f := range ops.Edited {
		modified[f] = struct{}{}
	}
	for f := range ops.Written {
		modified[f] = struct{}{}
	}
	for f := range ops.Read {
		if _, isMod := modified[f]; !isMod {
			readFiles = append(readFiles, f)
		}
	}
	for f := range modified {
		modifiedFiles = append(modifiedFiles, f)
	}
	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)
	return readFiles, modifiedFiles
}

// formatFileOperations renders the file lists as <read-files>/<modified-files>
// metadata blocks appended to a summary, or "" when both lists are empty.
func formatFileOperations(readFiles, modifiedFiles []string) string {
	var sections []string
	if len(readFiles) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(readFiles, "\n")+"\n</read-files>")
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(modifiedFiles, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

// toolResultMaxChars caps a tool result's serialized text in the summarization
// prompt, matching pi's TOOL_RESULT_MAX_CHARS.
const toolResultMaxChars = 2000

// truncateForSummary caps text at maxChars, appending a truncation marker.
func truncateForSummary(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return fmt.Sprintf("%s\n\n[... %d more characters truncated]", text[:maxChars], len(text)-maxChars)
}

// textOf concatenates the text blocks of a content list.
func textOf(content agentcore.ContentList) string {
	var b strings.Builder
	for _, c := range content {
		if t, ok := c.(agentcore.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// serializeConversation renders messages as a plain-text transcript for the
// summarization prompt, mirroring pi's serializeConversation: user text,
// assistant thinking/text/tool-call lines, and truncated tool results.
func serializeConversation(msgs []agentcore.Message) string {
	var parts []string
	for _, msg := range msgs {
		switch m := msg.(type) {
		case agentcore.UserMessage:
			if s := textOf(m.Content); s != "" {
				parts = append(parts, "[User]: "+s)
			}
		case agentcore.AssistantMessage:
			var textParts, thinkingParts, toolCalls []string
			for _, block := range m.Content {
				switch c := block.(type) {
				case agentcore.TextContent:
					textParts = append(textParts, c.Text)
				case agentcore.ThinkingContent:
					thinkingParts = append(thinkingParts, c.Thinking)
				case agentcore.ToolCallContent:
					toolCalls = append(toolCalls, formatToolCall(c))
				}
			}
			if len(thinkingParts) > 0 {
				parts = append(parts, "[Assistant thinking]: "+strings.Join(thinkingParts, "\n"))
			}
			if len(textParts) > 0 {
				parts = append(parts, "[Assistant]: "+strings.Join(textParts, "\n"))
			}
			if len(toolCalls) > 0 {
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(toolCalls, "; "))
			}
		case agentcore.ToolResultMessage:
			if s := textOf(m.Content); s != "" {
				parts = append(parts, "[Tool result]: "+truncateForSummary(s, toolResultMaxChars))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// formatToolCall renders a tool call as name(key=value, ...) with each value
// JSON-encoded, mirroring pi's serialization of tool-call arguments.
func formatToolCall(c agentcore.ToolCallContent) string {
	if len(c.Arguments) == 0 {
		return c.Name + "()"
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(c.Arguments, &m); err != nil {
		// Not an object: fall back to the raw argument text.
		return fmt.Sprintf("%s(%s)", c.Name, string(c.Arguments))
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic order (Go map iteration is random)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+string(m[k]))
	}
	return fmt.Sprintf("%s(%s)", c.Name, strings.Join(pairs, ", "))
}

// GenerateSummary drives a provider stream to summarize msgs into a structured
// checkpoint. When previousSummary is non-empty it uses the update template and
// embeds the prior summary in <previous-summary> tags; otherwise it uses the
// first-time template. maxTokens is bounded to min(0.8*reserveTokens,
// model.MaxOutputTokens) as in pi. The returned text is the concatenation of
// the assistant response's text blocks. A terminal error/aborted response is
// surfaced as an error.
func GenerateSummary(
	ctx context.Context,
	stream provider.StreamFn,
	model provider.Model,
	msgs []agentcore.Message,
	reserveTokens int,
	previousSummary string,
	cfg provider.StreamConfig,
) (string, error) {
	base := summarizationPrompt
	if previousSummary != "" {
		base = updateSummarizationPrompt
	}

	conversation := serializeConversation(msgs)
	var b strings.Builder
	b.WriteString("<conversation>\n")
	b.WriteString(conversation)
	b.WriteString("\n</conversation>\n\n")
	if previousSummary != "" {
		b.WriteString("<previous-summary>\n")
		b.WriteString(previousSummary)
		b.WriteString("\n</previous-summary>\n\n")
	}
	b.WriteString(base)

	promptMsg := agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(b.String())},
	}

	// Bound the summary output to min(0.8*reserveTokens, model max output).
	maxTokens := (reserveTokens * 8) / 10
	if model.MaxOutputTokens > 0 && model.MaxOutputTokens < maxTokens {
		maxTokens = model.MaxOutputTokens
	}
	extra := map[string]any{}
	for k, v := range cfg.Extra {
		extra[k] = v
	}
	if maxTokens > 0 {
		extra["max_tokens"] = maxTokens
	}
	cfg.Extra = extra

	llm := provider.LlmContext{
		SystemPrompt: SUMMARIZATION_SYSTEM_PROMPT,
		Messages:     agentcore.MessageList{promptMsg},
	}
	s, err := stream(ctx, model.ID, llm, cfg)
	if err != nil {
		return "", fmt.Errorf("compaction: build summary stream: %w", err)
	}

	// Drain events so the stream's result is populated, then read the final
	// message. Failures ride the stream as a terminal message per the provider
	// contract, so inspect StopReason rather than only the returned error.
	for range s.Events() {
	}
	final, resErr := s.Result(ctx)
	if resErr != nil {
		return "", fmt.Errorf("compaction: summary stream: %w", resErr)
	}
	switch final.StopReason {
	case agentcore.StopReasonAborted:
		return "", fmt.Errorf("compaction: summarization aborted: %s", final.ErrorMessage)
	case agentcore.StopReasonError:
		return "", fmt.Errorf("compaction: summarization failed: %s", final.ErrorMessage)
	}

	summary := textOf(final.Content)
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("compaction: summarization produced empty output")
	}
	return summary, nil
}
