package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// stubTool is a minimal AgentTool for registry tests.
type stubTool struct {
	name   string
	schema string
}

func (s stubTool) Name() string                     { return s.name }
func (s stubTool) Description() string              { return "stub" }
func (s stubTool) Schema() json.RawMessage          { return json.RawMessage(s.schema) }
func (s stubTool) ExecutionMode() ToolExecutionMode { return ToolExecutionParallel }
func (s stubTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
	return AgentToolResult{Content: ContentList{NewTextContent("ok")}}, nil
}

const personSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string"},
    "age":  {"type": "integer"}
  },
  "required": ["name"],
  "additionalProperties": false
}`

func newTestRegistry(t *testing.T) *ToolRegistry {
	t.Helper()
	r := NewToolRegistry()
	if err := r.Register(stubTool{name: "person", schema: personSchema}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return r
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := newTestRegistry(t)
	tool, ok := r.Get("person")
	if !ok || tool.Name() != "person" {
		t.Fatalf("Get(person) failed: %v %v", tool, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("Get(missing) should report not found")
	}
	if got := r.List(); len(got) != 1 || got[0].Name() != "person" {
		t.Errorf("List wrong: %+v", got)
	}
}

func TestRegistryDuplicateRejected(t *testing.T) {
	r := newTestRegistry(t)
	if err := r.Register(stubTool{name: "person", schema: personSchema}); err == nil {
		t.Fatal("expected duplicate registration to error")
	}
}

func TestRegistryValidArgs(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("person", json.RawMessage(`{"name":"ada","age":36}`))
	if errs != nil {
		t.Fatalf("valid args reported errors: %+v", errs)
	}
}

func TestRegistryMissingRequiredField(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("person", json.RawMessage(`{"age":36}`))
	if len(errs) == 0 {
		t.Fatal("expected error for missing required field 'name'")
	}
}

func TestRegistryTypeError(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("person", json.RawMessage(`{"name":"ada","age":"old"}`))
	if len(errs) == 0 {
		t.Fatal("expected type error for age")
	}
	// The offending field should be located at /age.
	found := false
	for _, e := range errs {
		if e.Field == "/age" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a field error at /age, got %+v", errs)
	}
}

func TestRegistryUnknownTool(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("nope", json.RawMessage(`{}`))
	if len(errs) != 1 || errs[0].Field != "" {
		t.Fatalf("expected single root error for unknown tool, got %+v", errs)
	}
}

func TestRegistryNoSchemaSkipsValidation(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(stubTool{name: "free", schema: ""}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if errs := r.Validate("free", json.RawMessage(`{"anything":true}`)); errs != nil {
		t.Fatalf("no-schema tool should skip validation, got %+v", errs)
	}
}

func TestValidationErrorResultShape(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("person", json.RawMessage(`{"age":36}`))
	res := ValidationErrorResult("person", errs)
	if len(res.Content) == 0 {
		t.Fatal("expected content in validation error result")
	}
	if _, ok := res.Details.([]FieldError); !ok {
		t.Errorf("expected Details to carry []FieldError, got %T", res.Details)
	}
	if res.Terminate != nil {
		t.Error("validation failure must not terminate the run")
	}
}

func TestRegistryBadSchemaRejectedAtRegister(t *testing.T) {
	r := NewToolRegistry()
	err := r.Register(stubTool{name: "bad", schema: `{"type": 123}`})
	if err == nil {
		t.Fatal("expected invalid schema to fail at registration")
	}
}
