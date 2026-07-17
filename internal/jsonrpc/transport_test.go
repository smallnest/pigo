package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain lets this test binary double as a mock JSON-RPC server subprocess.
// When JSONRPC_TEST_SERVER is set the process runs the echo server and exits;
// otherwise it runs the normal test suite. This is the standard Go pattern for
// exercising a subprocess transport without shipping a separate helper binary.
func TestMain(m *testing.M) {
	switch os.Getenv("JSONRPC_TEST_SERVER") {
	case "echo":
		runEchoServer()
		return
	case "silent":
		// Read and discard everything, never reply — used for timeout tests.
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
		}
		return
	}
	os.Exit(m.Run())
}

// runEchoServer replies to each request: method "echo" returns its params,
// method "fail" returns a JSON-RPC error, notifications produce no reply.
func runEchoServer() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	w := bufio.NewWriter(os.Stdout)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue // notification: no response
		}
		var resp Response
		resp.JSONRPC = Version
		resp.ID = req.ID
		switch req.Method {
		case "fail":
			resp.Error = &Error{Code: -32000, Message: "boom"}
		default:
			resp.Result = req.Params
			if resp.Result == nil {
				resp.Result = json.RawMessage(`null`)
			}
		}
		out, _ := json.Marshal(&resp)
		out = append(out, '\n')
		_, _ = w.Write(out)
		_ = w.Flush()
	}
}

// newTestClient starts this test binary as a mock server in the given mode.
func newTestClient(t *testing.T, mode string) *Client {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	c, err := NewClient(Config{
		Command: exe,
		Env:     append(os.Environ(), "JSONRPC_TEST_SERVER="+mode),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestCallEcho(t *testing.T) {
	c := newTestClient(t, "echo")
	ctx := context.Background()

	raw, err := c.Call(ctx, "echo", map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got["hello"] != "world" {
		t.Fatalf("got %v, want hello=world", got)
	}
}

func TestCallServerError(t *testing.T) {
	c := newTestClient(t, "echo")
	_, err := c.Call(context.Background(), "fail", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rpcErr *Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *jsonrpc.Error, got %T: %v", err, err)
	}
	if rpcErr.Code != -32000 || !strings.Contains(rpcErr.Message, "boom") {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
}

// TestConcurrentCalls verifies responses correlate to the right caller when many
// requests are in flight at once (id-based correlation).
func TestConcurrentCalls(t *testing.T) {
	c := newTestClient(t, "echo")
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, err := c.Call(ctx, "echo", map[string]int{"n": i})
			if err != nil {
				errs[i] = err
				return
			}
			var got map[string]int
			if err := json.Unmarshal(raw, &got); err != nil {
				errs[i] = err
				return
			}
			if got["n"] != i {
				errs[i] = fmt.Errorf("call %d got n=%d", i, got["n"])
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
}

// TestCallContextTimeout verifies Call returns when the context is cancelled and
// the server never replies.
func TestCallContextTimeout(t *testing.T) {
	c := newTestClient(t, "silent")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Call(ctx, "echo", nil)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Call blocked too long: %v", time.Since(start))
	}
}

func TestNotifyNoResponse(t *testing.T) {
	c := newTestClient(t, "echo")
	if err := c.Notify("ping", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// A subsequent Call must still work (notification produced no stray reply).
	if _, err := c.Call(context.Background(), "echo", nil); err != nil {
		t.Fatalf("Call after Notify: %v", err)
	}
}

func TestCallAfterClose(t *testing.T) {
	c := newTestClient(t, "echo")
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Call(context.Background(), "echo", nil); err == nil {
		t.Fatal("expected error calling closed client")
	}
}

func TestNewClientEmptyCommand(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestIDRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"number", `123`},
		{"string", `"abc"`},
	} {
		var id ID
		if err := json.Unmarshal([]byte(tc.raw), &id); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		out, err := json.Marshal(id)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		if string(out) != tc.raw {
			t.Fatalf("%s: round-trip got %s want %s", tc.name, out, tc.raw)
		}
	}
	var bad ID
	if err := json.Unmarshal([]byte(`true`), &bad); err == nil {
		t.Fatal("expected error for boolean id")
	}
}
