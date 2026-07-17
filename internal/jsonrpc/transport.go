// This file implements the subprocess JSON-RPC client (US-014/#116). A Client
// launches an external executable, writes requests to its stdin and reads
// newline-delimited JSON-RPC messages from its stdout on a background reader
// goroutine. Requests are correlated to responses by id through a pending-call
// map, so Call is safe for concurrent use: each caller blocks only on its own
// response channel until the reply arrives, the context is cancelled, or the
// child exits.
package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// closeGrace bounds how long Close waits for a child to exit on its own after
// stdin is closed, before force-killing it.
const closeGrace = 5 * time.Second

// ErrClosed is returned by Call once the client has been closed or the child
// process has exited.
var ErrClosed = errors.New("jsonrpc: client closed")

// Client is a JSON-RPC 2.0 client bound to a subprocess over its stdio.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	writeMu sync.Mutex // serializes writes to the child's stdin
	nextID  atomic.Int64

	mu       sync.Mutex          // guards pending and closed
	pending  map[string]chan res // id -> waiter
	closed   bool
	closeErr error

	done chan struct{} // closed when the reader goroutine exits
}

// res carries a decoded response (or a transport-level error) to a waiter.
type res struct {
	resp *Response
	err  error
}

// Config describes the subprocess to launch.
type Config struct {
	// Command is the executable path.
	Command string
	// Args are the process arguments (excluding the command itself).
	Args []string
	// Env is the child's environment (os/exec form: "KEY=value"). When nil the
	// child inherits the parent environment.
	Env []string
	// Dir is the child's working directory; empty means the parent's.
	Dir string
	// Stderr optionally receives the child's stderr (e.g. for logging). When nil
	// the child's stderr is discarded.
	Stderr io.Writer
}

// NewClient starts the subprocess and begins reading its stdout. The caller must
// Close the client to terminate the child and release resources.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Command == "" {
		return nil, errors.New("jsonrpc: empty command")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = cfg.Env
	cmd.Dir = cfg.Dir
	cmd.Stderr = cfg.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("jsonrpc: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("jsonrpc: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("jsonrpc: start %q: %w", cfg.Command, err)
	}

	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[string]chan res),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// readLoop reads newline-delimited JSON messages from the child's stdout and
// dispatches each response to its waiter. It exits when stdout hits EOF/error,
// failing all outstanding calls.
func (c *Client) readLoop() {
	defer close(c.done)
	scanner := bufio.NewScanner(c.stdout)
	// MCP/plugin payloads (e.g. tool schemas) can be large; raise the line cap.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			// A line we can't parse as a response is skipped (it may be a
			// server->client request/notification, which this minimal client
			// does not handle).
			continue
		}
		if resp.ID == nil {
			continue // notification from server; nothing to correlate
		}
		c.deliver(resp.ID.String(), res{resp: &resp})
	}

	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.failAll(fmt.Errorf("jsonrpc: reader stopped: %w", err))
}

// deliver hands a response to its waiter (if still registered).
func (c *Client) deliver(id string, r res) {
	c.mu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		ch <- r
	}
}

// failAll completes every outstanding call with err and marks the client closed.
func (c *Client) failAll(err error) {
	c.mu.Lock()
	if c.closeErr == nil {
		c.closeErr = err
	}
	c.closed = true
	pending := c.pending
	c.pending = make(map[string]chan res)
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- res{err: err}
	}
}

// Call sends a request and waits for the matching response. It returns the
// decoded Result on success, the server Error as error on an error response, or
// a transport/context error. Call is safe for concurrent use.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := NumID(c.nextID.Add(1))
	req, err := newRequest(&id, method, params)
	if err != nil {
		return nil, err
	}

	ch := make(chan res, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, c.closedErr()
	}
	c.pending[id.String()] = ch
	c.mu.Unlock()

	if err := c.write(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id.String())
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id.String())
		c.mu.Unlock()
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if r.resp.Error != nil {
			return nil, r.resp.Error
		}
		return r.resp.Result, nil
	}
}

// Notify sends a notification (no id, no response expected).
func (c *Client) Notify(method string, params any) error {
	req, err := newRequest(nil, method, params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return c.closedErr()
	}
	c.mu.Unlock()
	return c.write(req)
}

// write serializes a message and writes it as one newline-terminated line.
// Writes are serialized so concurrent callers don't interleave bytes on stdin.
func (c *Client) write(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("jsonrpc: marshal request: %w", err)
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(data); err != nil {
		return fmt.Errorf("jsonrpc: write: %w", err)
	}
	return nil
}

// closedErr reports why the client is closed, defaulting to ErrClosed.
func (c *Client) closedErr() error {
	if c.closeErr != nil {
		return c.closeErr
	}
	return ErrClosed
}

// Close closes the child's stdin (signalling graceful shutdown), waits briefly
// for the process to exit, and kills it if it does not. It is idempotent.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.done
		return nil
	}
	c.closed = true
	if c.closeErr == nil {
		c.closeErr = ErrClosed
	}
	c.mu.Unlock()

	// Closing stdin lets a well-behaved child exit on its own.
	_ = c.stdin.Close()

	// Wait for the child; kill if it doesn't stop promptly. The grace timer
	// bounds Close so a child that ignores stdin EOF and keeps stdout open
	// (a hung plugin/server) cannot block us forever.
	waitErr := make(chan error, 1)
	go func() { waitErr <- c.cmd.Wait() }()

	grace := time.NewTimer(closeGrace)
	defer grace.Stop()

	select {
	case <-waitErr:
		<-c.done
		return nil
	case <-c.done:
		// reader saw EOF; give Wait a brief moment, then force kill.
	case <-grace.C:
		// child hasn't exited within the grace period; force it.
	}

	select {
	case <-waitErr:
	default:
		_ = c.cmd.Process.Kill()
		<-waitErr
	}
	<-c.done
	return nil
}
