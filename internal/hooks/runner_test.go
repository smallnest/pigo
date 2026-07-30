package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunnerRunStdinAndEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh -c not available on windows")
	}
	dir := t.TempDir()
	outFile := filepath.Join(dir, "captured.json")
	envFile := filepath.Join(dir, "env.txt")

	r := &Runner{ProjectDir: dir}
	h := HookConfig{Command: "cat > " + outFile + "; printf '%s\\n%s\\n%s\\n' \"$PIGO_SESSION_ID\" \"$PIGO_PROJECT_DIR\" \"$PIGO_EVENT_TYPE\" > " + envFile}
	input := HookInput{EventType: "PreToolUse", SessionID: "sess-1", ProjectDir: dir, ToolName: "bash"}

	if _, err := r.Run(context.Background(), h, input); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	var got HookInput
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("stdin was not valid JSON: %v (%s)", err, data)
	}
	if got.EventType != "PreToolUse" || got.SessionID != "sess-1" || got.ToolName != "bash" {
		t.Fatalf("unexpected decoded stdin: %+v", got)
	}

	envData, _ := os.ReadFile(envFile)
	lines := strings.Split(strings.TrimSpace(string(envData)), "\n")
	if len(lines) != 3 || lines[0] != "sess-1" || lines[1] != dir || lines[2] != "PreToolUse" {
		t.Fatalf("unexpected env: %v", lines)
	}
}

func TestRunnerExitCodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh -c not available on windows")
	}
	dir := t.TempDir()
	r := &Runner{ProjectDir: dir}
	ctx := context.Background()

	t.Run("exit 0 with json", func(t *testing.T) {
		out, err := r.Run(ctx, HookConfig{Command: `echo '{"additionalContext":"hi"}'`}, HookInput{})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if out.AdditionalContext != "hi" {
			t.Fatalf("unexpected out: %+v", out)
		}
	})

	t.Run("exit 0 non-json is no-op", func(t *testing.T) {
		out, err := r.Run(ctx, HookConfig{Command: `echo hello world`}, HookInput{})
		if err != nil || out.blocks() || out.AdditionalContext != "" {
			t.Fatalf("expected no-op, got out=%+v err=%v", out, err)
		}
	})

	t.Run("exit 2 blocks with stderr reason", func(t *testing.T) {
		out, err := r.Run(ctx, HookConfig{Command: `echo "denied" >&2; exit 2`}, HookInput{})
		if err != nil {
			t.Fatalf("exit 2 should not be an error, got %v", err)
		}
		if !out.blocks() || out.Reason != "denied" {
			t.Fatalf("unexpected out: %+v", out)
		}
	})

	t.Run("exit 1 is failure", func(t *testing.T) {
		_, err := r.Run(ctx, HookConfig{Command: `echo boom >&2; exit 1`}, HookInput{})
		if err == nil {
			t.Fatal("expected error for exit 1")
		}
	})

	t.Run("command not found is failure", func(t *testing.T) {
		_, err := r.Run(ctx, HookConfig{Command: `this-command-does-not-exist-pigo`}, HookInput{})
		if err == nil {
			t.Fatal("expected error for missing command")
		}
	})
}

func TestRunnerTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh -c not available on windows")
	}
	r := &Runner{ProjectDir: t.TempDir()}
	start := time.Now()
	_, err := r.Run(context.Background(), HookConfig{Command: "sleep 5", Timeout: ptr(1)}, HookInput{})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout took too long: %v", time.Since(start))
	}
}

func TestRunnerOutputCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh -c not available on windows")
	}
	var buf cappedBuffer
	buf.limit = 10
	n, _ := buf.Write([]byte("0123456789abcdef"))
	if n != 16 {
		t.Fatalf("Write should report full length, got %d", n)
	}
	if len(buf.Bytes()) != 10 {
		t.Fatalf("expected 10 bytes retained, got %d", len(buf.Bytes()))
	}
	if !buf.truncated() {
		t.Fatal("expected truncated to be true")
	}
}
