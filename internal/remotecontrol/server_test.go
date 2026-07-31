package remotecontrol

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type fakeHandler struct {
	mu      sync.Mutex
	inputs  []string
	decides []decision
}

type decision struct {
	id      string
	approve bool
	always  bool
}

func (h *fakeHandler) OnInput(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inputs = append(h.inputs, text)
}

func (h *fakeHandler) OnDecide(id string, approve, always bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.decides = append(h.decides, decision{id, approve, always})
}

func (h *fakeHandler) lastInput() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.inputs) == 0 {
		return ""
	}
	return h.inputs[len(h.inputs)-1]
}

// startTestServer boots a server on loopback and returns it plus the pairing
// URL. The caller must Stop it.
func startTestServer(t *testing.T, h Handler) (*Server, string) {
	t.Helper()
	s := NewServer(Config{Host: "127.0.0.1", Port: 0}, h)
	url, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})
	return s, url
}

func TestHealthz(t *testing.T) {
	_, pairURL := startTestServer(t, nil)
	base := pairURL[:strings.Index(pairURL, "/pair")]
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("get healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
}

func TestPairRejectsBadToken(t *testing.T) {
	_, pairURL := startTestServer(t, nil)
	base := pairURL[:strings.Index(pairURL, "/pair")]
	resp, err := http.Get(base + "/pair?t=bogus")
	if err != nil {
		t.Fatalf("get pair: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad-token status = %d, want 401", resp.StatusCode)
	}
}

func TestPairSetsCookieAndServesSPA(t *testing.T) {
	_, pairURL := startTestServer(t, nil)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	resp, err := client.Get(pairURL)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { // followed redirect to /
		t.Fatalf("pair->root status = %d, want 200", resp.StatusCode)
	}

	// A second use of the same one-time token must fail.
	resp2, err := http.Get(pairURL)
	if err != nil {
		t.Fatalf("pair reuse: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token reuse status = %d, want 401", resp2.StatusCode)
	}
}

func TestRootRequiresAuth(t *testing.T) {
	_, pairURL := startTestServer(t, nil)
	base := pairURL[:strings.Index(pairURL, "/pair")]
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth root status = %d, want 401", resp.StatusCode)
	}
}

// sessionCred pairs and extracts the pigo_rc cookie value for WS dialing.
func sessionCred(t *testing.T, pairURL string) (base, cred string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // stop at the 302 to read the cookie
		},
	}
	resp, err := client.Get(pairURL)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			cred = c.Value
		}
	}
	if cred == "" {
		t.Fatal("no session cookie issued")
	}
	return pairURL[:strings.Index(pairURL, "/pair")], cred
}

func dialWS(t *testing.T, base, cred string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/ws"
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{cookieName + "=" + cred}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return websocket.Dial(ctx, wsURL, opts)
}

func TestWSRoundTrip(t *testing.T) {
	h := &fakeHandler{}
	s, pairURL := startTestServer(t, h)
	base, cred := sessionCred(t, pairURL)

	conn, _, err := dialWS(t, base, cred)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// First frame should be the connected status.
	var connected Frame
	if err := wsjson.Read(ctx, conn, &connected); err != nil {
		t.Fatalf("read connected: %v", err)
	}
	if connected.Type != FrameStatus || connected.State != StatusConnected {
		t.Fatalf("first frame = %+v, want status/connected", connected)
	}

	// Client -> server input reaches the handler.
	if err := wsjson.Write(ctx, conn, Frame{Type: FrameInput, Text: "hello"}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for h.lastInput() != "hello" {
		if time.Now().After(deadline) {
			t.Fatalf("handler never received input, got %q", h.lastInput())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Server -> client output reaches the browser.
	s.SendOutput("world")
	var out Frame
	if err := wsjson.Read(ctx, conn, &out); err != nil {
		t.Fatalf("read output: %v", err)
	}
	if out.Type != FrameOutput || out.Text != "world" {
		t.Fatalf("output frame = %+v, want output/world", out)
	}
}

func TestWSRejectsUnauth(t *testing.T) {
	_, pairURL := startTestServer(t, nil)
	base := pairURL[:strings.Index(pairURL, "/pair")]
	_, resp, err := dialWS(t, base, "not-a-valid-cred")
	if err == nil {
		t.Fatal("dial with bad cred succeeded, want failure")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestWSSingleClient(t *testing.T) {
	s, pairURL := startTestServer(t, &fakeHandler{})
	base, cred := sessionCred(t, pairURL)

	conn1, _, err := dialWS(t, base, cred)
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	defer conn1.Close(websocket.StatusNormalClosure, "")

	// Wait until the server registers the first client.
	deadline := time.Now().Add(time.Second)
	for !s.HasClient() {
		if time.Now().After(deadline) {
			t.Fatal("server never registered first client")
		}
		time.Sleep(5 * time.Millisecond)
	}

	_, resp, err := dialWS(t, base, cred)
	if err == nil {
		t.Fatal("second client connected, want rejection")
	}
	if resp != nil && resp.StatusCode != http.StatusConflict {
		t.Fatalf("second-client status = %d, want 409", resp.StatusCode)
	}
}
