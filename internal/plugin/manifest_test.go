// Tests for the plugin wire types (#261). These assert the commands/call
// request and result types round-trip through JSON marshal/unmarshal so the
// on-the-wire shape (field names, omitempty behavior) stays stable.
package plugin

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCommandCallParamsRoundTrip(t *testing.T) {
	want := CommandCallParams{
		Name: "review",
		Args: json.RawMessage(`{"path":"foo.go","verbose":true}`),
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Params must name its argument field "arguments" to match CallParams.
	var shape struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatalf("decode shape: %v", err)
	}
	if shape.Name != want.Name {
		t.Errorf("name = %q, want %q", shape.Name, want.Name)
	}

	var got CommandCallParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if !json.Valid(got.Args) || string(got.Args) != string(want.Args) {
		t.Errorf("Args = %s, want %s", got.Args, want.Args)
	}
}

func TestCommandCallResultRoundTrip(t *testing.T) {
	want := CommandCallResult{
		Prompt: "Please summarize the following changes.",
		Notifications: []CommandNotification{
			{Message: "loaded 3 files", Type: "info"},
			{Message: "1 file skipped", Type: "warning"},
			{Message: "done"},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got CommandCallResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

// TestCommandCallResultOmitEmpty confirms empty optional fields drop out of the
// wire form, keeping notifications-free results minimal.
func TestCommandCallResultOmitEmpty(t *testing.T) {
	data, err := json.Marshal(CommandCallResult{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(data); got != "{}" {
		t.Errorf("empty result marshaled to %s, want {}", got)
	}
}
