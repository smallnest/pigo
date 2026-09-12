// This file implements the shareable session export (issue #570, ported from
// pi's session-sharing practice): a Markdown rendering of a session suitable
// for archiving or publishing, with credential redaction ON by default so a
// transcript that touched real keys can be shared without leaking them.
//
// Redaction covers the credential shapes pi-share-hf warns about and more:
// sk-… API tokens, GitHub tokens (ghp_/gho_/ghu_/ghs_/ghr_), AWS AKIA ids,
// Slack xox… tokens, and env-shaped assignments (…_API_KEY=…,
// …_TOKEN=…, …_SECRET=…). Redaction applies to every text block in every
// message; the session header's system prompt is dropped unless explicitly
// included (it frequently carries environment details the user does not want
// published).
//
// The JSONL format stays the lossless resumable export (export.go); sharing a
// JSONL with redaction is allowed but breaks the round-trip by design — the
// flag exists for archival of transcripts that touched real secrets.
package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// redactPlaceholder replaces every credential-shaped match.
const redactPlaceholder = "[REDACTED]"

// redactionPatterns are the credential shapes stripped by default. Order
// matters only for overlapping shapes; each pattern is applied globally.
var redactionPatterns = []*regexp.Regexp{
	// Generic API tokens: sk-…, sk-proj-…, and similar dashed tokens.
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
	// GitHub tokens (classic PATs, OAuth, app, refresh).
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`),
	// AWS access key ids.
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	// Slack tokens.
	regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`),
	// Env-shaped assignments: NAME_API_KEY=…, NAME_TOKEN=…, NAME_SECRET=….
	regexp.MustCompile(`(?i)\b[A-Z0-9_]*(?:API_KEY|TOKEN|SECRET)[A-Z0-9_]*\s*=\s*[^\s"']+`),
}

// RedactText strips credential-shaped substrings, returning shareable text.
func RedactText(s string) string {
	for _, re := range redactionPatterns {
		s = re.ReplaceAllString(s, redactPlaceholder)
	}
	return s
}

// ShareOptions controls one shareable export.
type ShareOptions struct {
	// Redact strips credential-shaped substrings (default true in the CLI).
	Redact bool
	// IncludeSystemPrompt keeps the session's system prompt in the export. It
	// is dropped by default: it often embeds environment details.
	IncludeSystemPrompt bool
}

// markdownRedact redacts s when opts.Redact is set.
func (o ShareOptions) markdownRedact(s string) string {
	if o.Redact {
		return RedactText(s)
	}
	return s
}

// WriteMarkdown renders header + entries as a shareable Markdown transcript.
func WriteMarkdown(w io.Writer, header SessionHeader, entries []Entry, opts ShareOptions) error {
	fmt.Fprintf(w, "# Session %s\n\n", header.ID)
	fmt.Fprintf(w, "- Created: %s\n", header.CreatedAt.UTC().Format(time.RFC3339))
	if header.Model != "" {
		fmt.Fprintf(w, "- Model: %s\n", header.Model)
	}
	if header.Provider != "" {
		fmt.Fprintf(w, "- Provider: %s\n", header.Provider)
	}
	if header.SystemPrompt != "" && opts.IncludeSystemPrompt {
		fmt.Fprintf(w, "\n## System prompt\n\n%s\n", opts.markdownRedact(header.SystemPrompt))
	}
	io.WriteString(w, "\n---\n\n")
	for _, e := range entries {
		role := "unknown"
		var text string
		switch m := e.Message.(type) {
		case agentcore.UserMessage:
			role = "user"
			text = agentcore.ContentToText(m.Content)
		case agentcore.AssistantMessage:
			role = "assistant"
			text = agentcore.ContentToText(m.Content)
			for _, call := range m.ToolCalls() {
				text += fmt.Sprintf("\n\n> tool call: `%s`(%s)", call.Name, strings.TrimSpace(string(call.Arguments)))
			}
		case agentcore.ToolResultMessage:
			role = "tool result (" + m.ToolName + ")"
			text = agentcore.ContentToText(m.Content)
		}
		if text == "" {
			text = "_(no text)_"
		}
		fmt.Fprintf(w, "## %s\n\n%s\n\n", role, opts.markdownRedact(text))
	}
	return nil
}

// ExportShare writes the session identified by id to outPath in the share
// format named by format ("md" or "markdown" for Markdown, "json"/"jsonl" for
// the redactable JSONL, "html" for the self-contained HTML transcript). The
// parent directory of outPath must already exist. It returns the number of
// entries written.
func (s *Store) ExportShare(id, outPath, format string, opts ShareOptions) (int, error) {
	header, entries, err := s.LoadEntries(id)
	if err != nil {
		return 0, err
	}
	// Validate before creating the output file: a redacted JSONL is
	// contradictory (redaction corrupts the lossless round-trip), so it is a
	// caller error rather than a silent partial write.
	normalizedFormat := strings.ToLower(strings.TrimPrefix(format, "."))
	if normalizedFormat == "json" || normalizedFormat == "jsonl" {
		if opts.Redact {
			return 0, fmt.Errorf("redaction does not apply to the lossless JSONL export; use --no-redact for a resumable archive or the md format for sharing")
		}
	}
	f, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("session: create share export %s: %w", outPath, err)
	}
	defer f.Close()
	var writeErr error
	switch normalizedFormat {
	case "md", "markdown":
		writeErr = WriteMarkdown(f, header, entries, opts)
	case "json", "jsonl":
		writeErr = WriteJSONL(f, header, entries)
	case "html", "htm":
		writeErr = WriteHTML(f, header, entries)
	default:
		writeErr = fmt.Errorf("unknown share format %q (want md, json, or html)", format)
	}
	if writeErr != nil {
		return 0, writeErr
	}
	if err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("session: close share export %s: %w", outPath, err)
	}
	return len(entries), nil
}

// ExportFormatFromPath infers a share format from an output path's extension.
func ExportFormatFromPath(outPath string) string {
	switch strings.ToLower(filepath.Ext(outPath)) {
	case ".html", ".htm":
		return "html"
	case ".jsonl", ".json":
		return "json"
	default:
		return "md"
	}
}
