// This file implements the tool registry (US-014): tools are registered by
// name, and their arguments are validated against a per-tool JSON Schema
// (santhosh-tekuri/jsonschema v6) before execution. Validation failures are
// turned into a field-level error tool result rather than a Go error, so the
// model receives actionable feedback in the loop.
package agenttool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/smallnest/pigo/internal/agentcore"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// schemaPrinter renders jsonschema error kinds. LocalizedString dereferences
// the printer, so it must be non-nil.
var schemaPrinter = message.NewPrinter(language.English)

// ToolRegistry stores tools by name and validates call arguments against each
// tool's declared JSON Schema. It is safe for concurrent use.
type ToolRegistry struct {
	mu       sync.RWMutex
	tools    map[string]agentcore.AgentTool
	compiled map[string]*jsonschema.Schema
}

// NewToolRegistry returns an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:    make(map[string]agentcore.AgentTool),
		compiled: make(map[string]*jsonschema.Schema),
	}
}

// Register adds a tool, compiling its JSON Schema up front so bad schemas fail
// at registration rather than on first call. A duplicate name is an error. A
// tool whose Schema() is empty is registered with no validation.
func (r *ToolRegistry) Register(tool agentcore.AgentTool) error {
	name := tool.Name()
	if name == "" {
		return fmt.Errorf("registry: tool has empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("registry: tool %q already registered", name)
	}
	if raw := tool.Schema(); len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		sch, err := compileSchema(name, raw)
		if err != nil {
			return fmt.Errorf("registry: tool %q schema: %w", name, err)
		}
		r.compiled[name] = sch
	}
	r.tools[name] = tool
	return nil
}

// Get returns the tool registered under name and whether it was found.
func (r *ToolRegistry) Get(name string) (agentcore.AgentTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools sorted by name (stable ordering for
// deterministic provider tool declarations).
func (r *ToolRegistry) List() []agentcore.AgentTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]agentcore.AgentTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// FieldError is a single validation failure located at a JSON-pointer path
// within the arguments.
type FieldError struct {
	Field   string `json:"field"`   // JSON pointer, e.g. "/path" or "" for root
	Message string `json:"message"` // human-readable reason
}

// Validate checks args against the tool's compiled schema. It returns nil when
// the tool has no schema or the arguments are valid; otherwise it returns the
// flattened field-level errors. An unknown tool name is reported as a single
// root-level error.
func (r *ToolRegistry) Validate(name string, args json.RawMessage) []FieldError {
	r.mu.RLock()
	_, known := r.tools[name]
	sch, hasSchema := r.compiled[name]
	r.mu.RUnlock()

	if !known {
		return []FieldError{{Field: "", Message: fmt.Sprintf("unknown tool %q", name)}}
	}
	if !hasSchema {
		return nil
	}

	var inst any
	dec := json.NewDecoder(bytes.NewReader(nonEmptyJSON(args)))
	dec.UseNumber()
	if err := dec.Decode(&inst); err != nil {
		return []FieldError{{Field: "", Message: fmt.Sprintf("arguments are not valid JSON: %v", err)}}
	}

	if err := sch.Validate(inst); err != nil {
		var verr *jsonschema.ValidationError
		if as := asValidationError(err); as != nil {
			verr = as
		}
		if verr != nil {
			return flattenValidationError(verr)
		}
		return []FieldError{{Field: "", Message: err.Error()}}
	}
	return nil
}

// ValidationErrorResult builds an error AgentToolResult describing the given
// field errors, for the loop to hand back to the model (FR: field-level error
// tool result). Terminate is left nil (a validation failure never ends the run).
func ValidationErrorResult(toolName string, errs []FieldError) agentcore.AgentToolResult {
	var b strings.Builder
	fmt.Fprintf(&b, "Invalid arguments for tool %q:\n", toolName)
	for _, e := range errs {
		field := e.Field
		if field == "" {
			field = "(root)"
		}
		fmt.Fprintf(&b, "  - %s: %s\n", field, e.Message)
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(strings.TrimRight(b.String(), "\n"))},
		Details: errs,
	}
}

// compileSchema compiles a raw JSON Schema document held in memory.
func compileSchema(name string, raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	// A synthetic in-memory URL; each tool gets its own so schemas never clash.
	loc := "mem:///" + name + ".json"
	if err := c.AddResource(loc, doc); err != nil {
		return nil, err
	}
	return c.Compile(loc)
}

// flattenValidationError walks the ValidationError tree and returns one
// FieldError per leaf cause (the most specific failures), falling back to the
// node itself when it has no causes.
func flattenValidationError(e *jsonschema.ValidationError) []FieldError {
	var out []FieldError
	var walk func(n *jsonschema.ValidationError)
	walk = func(n *jsonschema.ValidationError) {
		if len(n.Causes) == 0 {
			out = append(out, FieldError{
				Field:   jsonPointer(n.InstanceLocation),
				Message: n.ErrorKind.LocalizedString(schemaPrinter),
			})
			return
		}
		for _, c := range n.Causes {
			walk(c)
		}
	}
	walk(e)
	if len(out) == 0 {
		out = append(out, FieldError{Field: jsonPointer(e.InstanceLocation), Message: e.Error()})
	}
	return out
}

// jsonPointer renders an instance-location path as a JSON pointer.
func jsonPointer(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	var b strings.Builder
	for _, tok := range loc {
		b.WriteByte('/')
		tok = strings.ReplaceAll(tok, "~", "~0")
		tok = strings.ReplaceAll(tok, "/", "~1")
		b.WriteString(tok)
	}
	return b.String()
}

// asValidationError extracts a *jsonschema.ValidationError from err if present.
func asValidationError(err error) *jsonschema.ValidationError {
	if verr, ok := err.(*jsonschema.ValidationError); ok {
		return verr
	}
	return nil
}

// nonEmptyJSON treats empty arguments as an empty object so schemas with only
// optional properties validate, and "required" violations are reported.
func nonEmptyJSON(args json.RawMessage) []byte {
	if len(bytes.TrimSpace(args)) == 0 {
		return []byte("{}")
	}
	return args
}
