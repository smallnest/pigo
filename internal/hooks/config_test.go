package hooks

import "testing"

func ptr[T any](v T) *T { return &v }

func TestHookConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		h       HookConfig
		wantErr bool
	}{
		{"valid command", HookConfig{Type: "command", Command: "echo hi"}, false},
		{"empty type defaults ok", HookConfig{Command: "echo hi"}, false},
		{"empty command", HookConfig{Type: "command", Command: ""}, true},
		{"whitespace command", HookConfig{Type: "command", Command: "   "}, true},
		{"wrong type", HookConfig{Type: "wasm", Command: "echo hi"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.h.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestHookConfigTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name string
		h    HookConfig
		want int
	}{
		{"nil timeout uses default", HookConfig{}, DefaultTimeoutSeconds},
		{"positive override", HookConfig{Timeout: ptr(10)}, 10},
		{"zero override ignored", HookConfig{Timeout: ptr(0)}, DefaultTimeoutSeconds},
		{"negative override ignored", HookConfig{Timeout: ptr(-5)}, DefaultTimeoutSeconds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.h.TimeoutSeconds(); got != tt.want {
				t.Fatalf("TimeoutSeconds() = %d, want %d", got, tt.want)
			}
		})
	}
}
