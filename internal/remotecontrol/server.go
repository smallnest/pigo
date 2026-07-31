package remotecontrol

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// spaFiles holds the embedded browser SPA. The placeholder shipped here is
// fleshed out by the web-SPA node; the server only needs a valid FS to embed.
//
//go:embed web
var spaFiles embed.FS

// Default configuration values (see tasks/spec-remote-control.md §3.2).
const (
	defaultPairTTL = 10 * time.Minute
	cookieName     = "pigo_rc"
	maxFrameBytes  = 64 * 1024

	// outputFlushInterval is the coalescing tick: adjacent output writes buffered
	// within one interval are sent as a single frame, so a fast stream produces a
	// few batched frames rather than one per write, while still hitting the PRD's
	// sub-100ms latency target (§8.2).
	outputFlushInterval = 16 * time.Millisecond
	// outputQueueMax bounds the pending buffer. A producer that would push it past
	// this blocks until the pump drains, applying backpressure — the terminal
	// mirror must not drop bytes (§5.4), so we never discard, only slow down.
	outputQueueMax = 256 * 1024
	// replayRingBytes bounds the rolling output history kept for late-join /
	// reconnect replay so a freshly connected browser sees recent context (§8.2).
	replayRingBytes = 256 * 1024
)

// Config controls a remote-control server instance.
type Config struct {
	PairTTL        time.Duration // pairing-token lifetime; 0 → defaultPairTTL
	Host           string        // LAN IP for the printed URL; "" → auto-detect
	Port           int           // 0 → auto-pick, fall back on conflict
	ConfirmTimeout time.Duration // 0 → wait forever for a remote decision

	// OnClientConnect, if set, is invoked (on the WebSocket goroutine) when a
	// browser pairs and connects, with the client's remote address. The REPL uses
	// it to print a one-line terminal notice so the operator notices remote access
	// (§7.3). It must not block for long.
	OnClientConnect func(remoteAddr string)
	// OnClientDisconnect, if set, is invoked when the controlling client's
	// WebSocket closes. It must not block for long.
	OnClientDisconnect func()
}

// Handler receives frames the browser sends. The REPL bridge implements it;
// tests supply a fake. Callbacks run on the WebSocket read goroutine and must
// not block for long.
type Handler interface {
	// OnInput is called when the client submits a prompt line.
	OnInput(text string)
	// OnDecide is called when the client answers a confirmation request.
	OnDecide(confirmID string, approve, always bool)
}

// serverState tracks the lifecycle for gating requests.
type serverState int

const (
	stateIdle serverState = iota
	stateListening
	stateEnded
)

// Server is an in-process HTTP + WebSocket server that mirrors the CLI session
// to a single paired browser on the LAN.
type Server struct {
	cfg     Config
	tokens  *TokenStore
	handler Handler
	spa     fs.FS

	mu         sync.Mutex
	state      serverState
	ln         net.Listener
	httpServer *http.Server
	host       string
	port       int

	client       *websocket.Conn
	clientCtx    context.Context
	clientCancel context.CancelFunc
	writeMu      sync.Mutex // serializes writes to client

	// Output coalescing + backpressure + replay (#445). outMu guards all of the
	// fields below; outCond signals both the pump (new pending output) and any
	// producer blocked by backpressure (pump drained pending). The pump is the
	// sole sender of output frames, so replay and live output can never interleave
	// or duplicate.
	outMu      sync.Mutex
	outCond    *sync.Cond
	outPending []byte     // coalesced, not-yet-sent output
	outClosed  bool       // set on Stop; releases blocked producers
	needReplay bool       // a fresh client connected; next flush replays the ring
	ring       ringBuffer // rolling last-N bytes for late-join replay
	pumpCancel context.CancelFunc
	pumpDone   chan struct{}
}

// NewServer builds a server. handler may be nil (frames from the client are
// then ignored), which is useful for output-only smoke tests.
func NewServer(cfg Config, handler Handler) *Server {
	if cfg.PairTTL <= 0 {
		cfg.PairTTL = defaultPairTTL
	}
	sub, err := fs.Sub(spaFiles, "web")
	if err != nil {
		// The embed path is a compile-time constant, so this cannot fail in a
		// correctly built binary; fall back to the raw FS defensively.
		sub = spaFiles
	}
	s := &Server{
		cfg:     cfg,
		tokens:  NewTokenStore(),
		handler: handler,
		spa:     sub,
		state:   stateIdle,
		ring:    ringBuffer{max: replayRingBytes},
	}
	s.outCond = sync.NewCond(&s.outMu)
	return s
}

// SetHandler installs the handler that receives client frames. It exists to
// break the construction cycle between the server (a Bridge's Sink) and the
// Bridge (the server's Handler): build the server, build the Bridge with the
// server as Sink, then SetHandler(bridge). It must be called before Start so
// the WebSocket read goroutine never observes a mid-flight swap.
func (s *Server) SetHandler(h Handler) {
	s.mu.Lock()
	s.handler = h
	s.mu.Unlock()
}

// Start resolves a LAN address, binds a listener, mints a one-time pairing
// token, begins serving, and returns the full pairing URL to print. It is
// non-blocking: the HTTP server runs on its own goroutine.
func (s *Server) Start() (string, error) {
	host := s.cfg.Host
	if host == "" {
		detected, err := DetectRoutableIP()
		if err != nil {
			return "", err
		}
		host = detected
	}
	ln, port, err := ListenFreePort(host, s.cfg.Port)
	if err != nil {
		return "", err
	}
	token, err := s.tokens.NewPairing(s.cfg.PairTTL)
	if err != nil {
		ln.Close()
		return "", err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/pair", s.handlePair)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/", s.handleRoot)

	pumpCtx, pumpCancel := context.WithCancel(context.Background())
	pumpDone := make(chan struct{})

	s.mu.Lock()
	s.ln = ln
	s.host = host
	s.port = port
	s.httpServer = &http.Server{Handler: mux}
	s.state = stateListening
	s.pumpCancel = pumpCancel
	s.pumpDone = pumpDone
	s.mu.Unlock()

	go s.httpServer.Serve(ln)
	go s.outputPump(pumpCtx, pumpDone)

	return fmt.Sprintf("http://%s:%d/pair?t=%s", host, port, token), nil
}

// Stop notifies the client, closes the WebSocket, shuts down the HTTP server,
// and wipes all tokens. It is safe to call more than once.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.state == stateEnded {
		s.mu.Unlock()
		return nil
	}
	s.state = stateEnded
	srv := s.httpServer
	client := s.client
	cancel := s.clientCancel
	pumpCancel := s.pumpCancel
	pumpDone := s.pumpDone
	s.mu.Unlock()

	// Release any producer blocked on backpressure, then stop the output pump and
	// wait for its final flush so the last buffered output reaches the client
	// before we announce the session end.
	s.outMu.Lock()
	s.outClosed = true
	s.outCond.Broadcast()
	s.outMu.Unlock()
	if pumpCancel != nil {
		pumpCancel()
		<-pumpDone
	}

	if client != nil {
		// Best-effort: tell the browser the session ended, then close.
		s.writeFrame(client, Frame{Type: FrameStatus, State: StatusEnded})
		client.Close(websocket.StatusNormalClosure, "session ended")
	}
	if cancel != nil {
		cancel()
	}
	s.tokens.Clear()
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handlePair validates the one-time token, issues a session cookie, and
// redirects to the SPA root. Invalid/expired/used tokens get 401.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if s.ended() {
		http.Error(w, "session ended", http.StatusGone)
		return
	}
	token := r.URL.Query().Get("t")
	if token == "" || !s.tokens.ConsumePairing(token) {
		http.Error(w, "pairing link invalid or expired", http.StatusUnauthorized)
		return
	}
	cred, err := s.tokens.IssueSession()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    cred,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleRoot serves the SPA to authenticated clients and an instructional page
// otherwise.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s.ended() {
		http.Error(w, "session ended", http.StatusGone)
		return
	}
	if !s.authed(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8">` +
			`<p>Open the pairing link printed in your terminal to connect.</p>`))
		return
	}
	http.FileServer(http.FS(s.spa)).ServeHTTP(w, r)
}

// handleWS upgrades to a WebSocket for the single controlling client.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if s.ended() {
		http.Error(w, "session ended", http.StatusGone)
		return
	}
	if !s.authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	if s.client != nil {
		s.mu.Unlock()
		http.Error(w, "another device is controlling this session", http.StatusConflict)
		return
	}
	s.mu.Unlock()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrameBytes)

	ctx, cancel := context.WithCancel(r.Context())
	s.mu.Lock()
	s.client = conn
	s.clientCtx = ctx
	s.clientCancel = cancel
	s.mu.Unlock()

	// Announce connection to the client, then arm a replay so the pump resends the
	// rolling scrollback (last replayRingBytes) as the first output frame. Live
	// output produced after this point is appended behind the snapshot by the
	// pump, so ordering is preserved and nothing is duplicated.
	s.writeFrame(conn, Frame{Type: FrameStatus, State: StatusConnected})
	s.outMu.Lock()
	s.needReplay = true
	s.outCond.Signal()
	s.outMu.Unlock()

	// Notify the terminal operator that a client connected (§7.3).
	if s.cfg.OnClientConnect != nil {
		s.cfg.OnClientConnect(r.RemoteAddr)
	}

	defer func() {
		cancel()
		s.mu.Lock()
		if s.client == conn {
			s.client = nil
			s.clientCtx = nil
			s.clientCancel = nil
		}
		s.mu.Unlock()
		conn.Close(websocket.StatusNormalClosure, "")
		if s.cfg.OnClientDisconnect != nil {
			s.cfg.OnClientDisconnect()
		}
	}()

	for {
		var f Frame
		if err := wsjson.Read(ctx, conn, &f); err != nil {
			return // client disconnected or context cancelled
		}
		s.dispatch(f)
	}
}

// dispatch routes an inbound client frame to the handler.
func (s *Server) dispatch(f Frame) {
	if s.handler == nil {
		return
	}
	switch f.Type {
	case FrameInput:
		s.handler.OnInput(f.Text)
	case FrameDecide:
		s.handler.OnDecide(f.ConfirmID, f.Approve, f.Always)
	}
}

// Broadcast sends a frame to the connected client, if any. It is safe to call
// from any goroutine.
func (s *Server) Broadcast(f Frame) {
	s.mu.Lock()
	conn := s.client
	s.mu.Unlock()
	if conn == nil {
		return
	}
	s.writeFrame(conn, f)
}

// SendOutput streams session text to the client. It never drops bytes: the text
// is appended to the ring (for replay) and to the pending buffer that the pump
// coalesces and flushes. If pending output has grown past outputQueueMax the
// call blocks until the pump drains it, applying backpressure to the producer
// rather than discarding output (§5.4, §8.4). It returns immediately once the
// server is stopping so a shutting-down producer never wedges.
func (s *Server) SendOutput(text string) {
	if text == "" {
		return
	}
	b := []byte(text)

	s.outMu.Lock()
	defer s.outMu.Unlock()

	// Always record into the ring so a late-joining / reconnecting client can be
	// replayed the recent scrollback, even while a producer is momentarily blocked
	// on backpressure below.
	s.ring.write(b)

	// Backpressure: wait until the pump has drained enough that appending stays
	// within the bound. Bail out if the server is stopping.
	for !s.outClosed && len(s.outPending) >= outputQueueMax {
		s.outCond.Wait()
	}
	if s.outClosed {
		return
	}
	s.outPending = append(s.outPending, b...)
	s.outCond.Signal()
}

// SendConfirm asks the client to approve a risky tool call.
func (s *Server) SendConfirm(confirmID, tool, summary string) {
	s.Broadcast(Frame{Type: FrameConfirm, ConfirmID: confirmID, Tool: tool, Summary: summary})
}

// HasClient reports whether a controlling client is currently connected.
func (s *Server) HasClient() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client != nil
}

// Addr returns the bound host:port, or "" before Start.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return net.JoinHostPort(s.host, fmt.Sprint(s.port))
}

func (s *Server) writeFrame(conn *websocket.Conn, f Frame) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = wsjson.Write(ctx, conn, f)
}

func (s *Server) ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == stateEnded
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return s.tokens.ValidateSession(c.Value)
}

// outputPump is the sole producer of output frames. It coalesces buffered
// output on a fixed tick and flushes it as a single frame, so a fast stream
// becomes a few batched frames while staying under the latency target: the
// first write after an idle period flushes immediately, and writes arriving
// during the following outputFlushInterval are batched. It also owns replay:
// when a client
// (re)connects, it prepends the ring snapshot before the live pending buffer.
// Being the only writer of output frames, replay and live output can never
// interleave or duplicate. It exits after a final flush when its context is
// cancelled (on Stop).
func (s *Server) outputPump(ctx context.Context, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(outputFlushInterval)
	defer ticker.Stop()

	// A tiny goroutine wakes the Cond wait below whenever the context is
	// cancelled, so the pump does not sleep past shutdown.
	go func() {
		<-ctx.Done()
		s.outMu.Lock()
		s.outCond.Broadcast()
		s.outMu.Unlock()
	}()

	for {
		s.outMu.Lock()
		// Wait until there is something to do: pending output, an armed replay, or
		// shutdown.
		for len(s.outPending) == 0 && !s.needReplay && ctx.Err() == nil {
			s.outCond.Wait()
		}
		replay := s.needReplay
		s.needReplay = false
		var snapshot []byte
		if replay {
			snapshot = s.ring.snapshot()
		}
		// Coalesce: take everything buffered so far as one batch.
		pending := s.outPending
		s.outPending = nil
		// Wake any producer blocked on backpressure now that pending is drained.
		s.outCond.Broadcast()
		stopping := ctx.Err() != nil
		s.outMu.Unlock()

		s.mu.Lock()
		conn := s.client
		s.mu.Unlock()

		if conn != nil {
			// Replay first so the reconnecting browser restores context, then the
			// live batch. The ring already contains everything appended via
			// SendOutput, so on a fresh connection the snapshot covers pending too;
			// avoid double-sending by preferring the snapshot when it is present.
			if len(snapshot) > 0 {
				s.writeFrame(conn, Frame{Type: FrameOutput, Text: string(snapshot)})
			} else if len(pending) > 0 {
				s.writeFrame(conn, Frame{Type: FrameOutput, Text: string(pending)})
			}
		}

		if stopping {
			// Final drain done; exit.
			return
		}

		// Rate the loop on the ticker so bursts coalesce instead of spinning.
		select {
		case <-ctx.Done():
			// Loop once more to perform the final flush of anything buffered while
			// we were writing above.
			s.finalFlush()
			return
		case <-ticker.C:
		}
	}
}

// finalFlush drains any remaining pending output once during shutdown so the
// last bytes reach the client before the session-ended notice.
func (s *Server) finalFlush() {
	s.outMu.Lock()
	pending := s.outPending
	s.outPending = nil
	s.outCond.Broadcast()
	s.outMu.Unlock()
	if len(pending) == 0 {
		return
	}
	s.mu.Lock()
	conn := s.client
	s.mu.Unlock()
	if conn != nil {
		s.writeFrame(conn, Frame{Type: FrameOutput, Text: string(pending)})
	}
}

// ringBuffer is a bounded rolling byte buffer holding the most recent max bytes
// of session output for late-join / reconnect replay. It is not safe for
// concurrent use; the server guards it with outMu.
type ringBuffer struct {
	buf []byte
	max int
}

// write appends p, discarding oldest bytes so the buffer never exceeds max.
func (r *ringBuffer) write(p []byte) {
	if r.max <= 0 {
		return
	}
	if len(p) >= r.max {
		// Keep only the trailing max bytes of the new data.
		r.buf = append(r.buf[:0], p[len(p)-r.max:]...)
		return
	}
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		// Drop the oldest overflow. Copy down so the backing array does not grow
		// without bound over the life of the session.
		drop := len(r.buf) - r.max
		r.buf = append(r.buf[:0], r.buf[drop:]...)
	}
}

// snapshot returns a copy of the current contents.
func (r *ringBuffer) snapshot() []byte {
	if len(r.buf) == 0 {
		return nil
	}
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

// ErrNotStarted is returned by operations that require a running server.
var ErrNotStarted = errors.New("remotecontrol: server not started")
