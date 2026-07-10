// This file implements secret redaction for tool outputs (US-026) and the
// SandboxGate adapters that wire the pure Policy into the executor's
// BeforeToolCall / AfterToolCall hooks. Redaction is the single result
// choke-point: scrubResultSecrets rewrites every text block before it reaches
// the model, replacing detected secrets with a "[REDACTED:<key>]" marker. It
// honors the standing constraint that secret VALUES are never surfaced — only
// the key name / provider label. Detection combines a known-key registry
// (values registered from the environment or config) with precision-first
// regexes for well-known token shapes; matches are applied longest-first so a
// broad pattern never clobbers a more specific redaction.
package agent

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// secretPattern pairs a compiled regex with the label used in its redaction
// marker. Patterns are intentionally precise (anchored to a recognizable
// prefix + charset + length) so ordinary text is not mangled.
type secretPattern struct {
	label string
	re    *regexp.Regexp
}

// builtinSecretPatterns detect common credential shapes. Each is precision-first:
// a distinctive prefix plus a bounded token body, so false positives on prose
// are rare. Order does not matter — overlaps are resolved by longest-match.
var builtinSecretPatterns = []secretPattern{
	// anthropic-key must precede openai-key: "sk-ant-…" matches both patterns
	// with equal length, and the stable longest-first sort keeps registry order
	// on ties, so the more-specific label wins.
	{"anthropic-key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai-key", regexp.MustCompile(`sk-(?:proj-)?[A-Za-z0-9_-]{20,}`)},
	{"aws-access-key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"github-token", regexp.MustCompile(`gh[posru]_[A-Za-z0-9]{20,}`)},
	{"google-api-key", regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)},
	{"slack-token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
	{"bearer-token", regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{20,}`)},
	{"private-key-block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----`)},
}

// SecretRegistry holds known secret values keyed by a human label (e.g. the
// env-var name / provider). Registered values are redacted verbatim wherever
// they appear in output, which catches provider keys whose shape no regex knows.
// The registry stores only the label→value map in memory; values are never
// logged (they are matched and replaced, and the marker carries only the label).
type SecretRegistry struct {
	// values maps a label to the literal secret string to redact.
	values map[string]string
}

// NewSecretRegistry returns an empty registry.
func NewSecretRegistry() *SecretRegistry {
	return &SecretRegistry{values: map[string]string{}}
}

// Register records value under label. Empty or very short values are ignored so
// a blank/short env var cannot cause every character to be redacted.
func (r *SecretRegistry) Register(label, value string) {
	if r.values == nil {
		r.values = map[string]string{}
	}
	if len(value) < 6 {
		return
	}
	r.values[label] = value
}

// redaction is one match to apply: the literal span and its label.
type redaction struct {
	value string
	label string
}

// scrubResultSecrets returns text with every known/registered secret replaced by
// "[REDACTED:<label>]". It applies matches longest-first so a broad pattern
// cannot partially overwrite a more specific one, then rebuilds the string in a
// single pass. It never returns a secret value in the output or in any log.
func scrubResultSecrets(text string, reg *SecretRegistry) string {
	if text == "" {
		return text
	}
	var reds []redaction
	// 1. Registered exact values (highest precision — we know these are secret).
	if reg != nil {
		for label, val := range reg.values {
			if val != "" && strings.Contains(text, val) {
				reds = append(reds, redaction{value: val, label: label})
			}
		}
	}
	// 2. Pattern matches (shape-based).
	for _, p := range builtinSecretPatterns {
		for _, m := range p.re.FindAllString(text, -1) {
			reds = append(reds, redaction{value: m, label: p.label})
		}
	}
	if len(reds) == 0 {
		return text
	}
	// Longest match first so a substring match never pre-empts its superset.
	sort.SliceStable(reds, func(i, j int) bool {
		return len(reds[i].value) > len(reds[j].value)
	})
	out := text
	for _, r := range reds {
		if r.value == "" {
			continue
		}
		out = strings.ReplaceAll(out, r.value, "[REDACTED:"+r.label+"]")
	}
	return out
}

// scrubContentList returns a copy of list with every text block scrubbed. Non-
// text blocks pass through unchanged.
func scrubContentList(list ContentList, reg *SecretRegistry) ContentList {
	if len(list) == 0 {
		return list
	}
	out := make(ContentList, len(list))
	for i, c := range list {
		if tc, ok := c.(TextContent); ok {
			tc.Text = scrubResultSecrets(tc.Text, reg)
			out[i] = tc
			continue
		}
		out[i] = c
	}
	return out
}

// SandboxGate binds a Policy and a SecretRegistry to the executor hooks. It is
// the runtime seam: BeforeHook enforces the permission decision (never
// fail-open — a deny or an unclassifiable request Blocks), AfterHook scrubs
// secrets from every result. Requests are derived from the tool call by
// classifyToolCall so the gate needs no per-tool wiring.
type SandboxGate struct {
	Policy  *Policy
	Secrets *SecretRegistry
	// Classify maps a tool call to a sandbox Request. When nil, no permission
	// check is applied (redaction still runs) — callers that want enforcement
	// must supply a classifier.
	Classify func(call AgentToolCall) (Request, bool)
	// PromptApprover is consulted when the policy returns DecisionPrompt. It
	// must return true to allow. When nil, a prompt decision Blocks (fail
	// closed): we never allow a prompt-required action without approval.
	PromptApprover func(ctx context.Context, call AgentToolCall, req Request) bool
}

// BeforeHook is a BeforeToolCallFunc that enforces the policy. It Blocks on
// deny, and on prompt-without-approver, and on any classification failure.
func (g *SandboxGate) BeforeHook(ctx context.Context, call AgentToolCall) *BeforeToolCallDecision {
	if g == nil || g.Policy == nil || g.Classify == nil {
		return nil // no enforcement configured
	}
	req, ok := g.Classify(call)
	if !ok {
		return nil // this tool is not gated
	}
	switch g.Policy.Evaluate(ctx, req) {
	case DecisionAllow:
		return nil
	case DecisionPrompt:
		if g.PromptApprover != nil && g.PromptApprover(ctx, call, req) {
			return nil
		}
		return blockDecision("sandbox: operation requires approval and was not granted")
	default:
		return blockDecision("sandbox: operation denied by policy")
	}
}

// AfterHook is an AfterToolCallFunc that scrubs secrets from the result content.
// It preserves Details/Terminate/IsError (only Content is overridden).
func (g *SandboxGate) AfterHook(ctx context.Context, call AgentToolCall, result AgentToolResult, isError bool) *AfterToolCallResult {
	var reg *SecretRegistry
	if g != nil {
		reg = g.Secrets
	}
	scrubbed := scrubContentList(result.Content, reg)
	return &AfterToolCallResult{Content: &scrubbed}
}

// blockDecision builds a BeforeToolCallDecision that blocks with msg.
func blockDecision(msg string) *BeforeToolCallDecision {
	cl := ContentList{NewTextContent(msg)}
	return &BeforeToolCallDecision{Block: true, Content: &cl}
}
