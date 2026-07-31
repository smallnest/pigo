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
)

// Config controls a remote-control server instance.
type Config struct {
	PairTTL        time.Duration // pairing-token lifetime; 0 → defaultPairTTL
	Host           string        // LAN IP for the printed URL; "" → auto-detect
	Port           int           // 0 → auto-pick, fall back on conflict
	ConfirmTimeout time.Duration // 0 → wait forever for a remote decision
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
	return &Server{
		cfg:     cfg,
		tokens:  NewTokenStore(),
		handler: handler,
		spa:     sub,
		state:   stateIdle,
	}
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

	s.mu.Lock()
	s.ln = ln
	s.host = host
	s.port = port
	s.httpServer = &http.Server{Handler: mux}
	s.state = stateListening
	s.mu.Unlock()

	go s.httpServer.Serve(ln)

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
	s.mu.Unlock()

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

	// Announce connection to the client.
	s.writeFrame(conn, Frame{Type: FrameStatus, State: StatusConnected})

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

// SendOutput streams session text to the client.
func (s *Server) SendOutput(text string) {
	s.Broadcast(Frame{Type: FrameOutput, Text: text})
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

// ErrNotStarted is returned by operations that require a running server.
var ErrNotStarted = errors.New("remotecontrol: server not started")
