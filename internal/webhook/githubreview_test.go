// Tests for the GitHub ready-for-review webhook (issue #567): signature
// verification, replay protection, event filtering, the isolated-route
// contract, and the end-to-end path from a valid delivery to a persisted
// read-only review session.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/session"
)

const testPayload = `{"action":"ready_for_review","repository":{"full_name":"smallnest/pigo"},"pull_request":{"number":42,"title":"Add scheduler","html_url":"https://github.com/smallnest/pigo/pull/42","head":{"ref":"feat/scheduler"}}}`

// signedRequest builds a POST /github request with a valid HMAC signature.
func signedRequest(t *testing.T, secret, body, delivery, event string) *http.Request {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	req, err := http.NewRequest(http.MethodPost, "/github", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	return req
}

type recordingRunner struct {
	mu    sync.Mutex
	calls []ReviewRequest
}

func (r *recordingRunner) run(_ context.Context, req ReviewRequest) error {
	r.mu.Lock()
	r.calls = append(r.calls, req)
	r.mu.Unlock()
	return nil
}

func (r *recordingRunner) waitCalls(t *testing.T, n int) []ReviewRequest {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := len(r.calls)
		r.mu.Unlock()
		if got >= n {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.calls
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("runner received %d calls, want %d", len(r.calls), n)
	return nil
}

func newTestServer(t *testing.T) (*Server, *recordingRunner) {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	srv := &Server{
		Secret: "test-secret",
		Store:  store,
		Runner: runner.run,
	}
	return srv, runner
}

// TestWebhookRejectsBadSignature verifies missing, malformed, and wrong
// signatures are rejected with 401 and never reach the runner.
func TestWebhookRejectsBadSignature(t *testing.T) {
	srv, runner := newTestServer(t)
	h := srv.Handler()

	// No signature header.
	req := signedRequest(t, srv.Secret, testPayload, "d-1", "pull_request")
	req.Header.Del("X-Hub-Signature-256")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature = %d, want 401", resp.Code)
	}

	// Wrong secret.
	resp = httptest.NewRecorder()
	h.ServeHTTP(resp, signedRequest(t, "other-secret", testPayload, "d-2", "pull_request"))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d, want 401", resp.Code)
	}

	// Malformed header prefix.
	resp = httptest.NewRecorder()
	req = signedRequest(t, srv.Secret, testPayload, "d-3", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha1=deadbeef")
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("sha1 prefix = %d, want 401", resp.Code)
	}

	if calls := runner.waitCalls(t, 0); len(calls) != 0 {
		t.Fatalf("runner called for %d rejected requests", len(calls))
	}
}

// TestWebhookReplayProtection verifies the same delivery id acts once and a
// missing delivery id never acts.
func TestWebhookReplayProtection(t *testing.T) {
	srv, runner := newTestServer(t)
	h := srv.Handler()

	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, signedRequest(t, srv.Secret, testPayload, "delivery-1", "pull_request"))
	if resp.Code != http.StatusOK {
		t.Fatalf("first delivery = %d, want 200", resp.Code)
	}

	// Replay: same delivery id, acknowledged but not acted on.
	resp = httptest.NewRecorder()
	h.ServeHTTP(resp, signedRequest(t, srv.Secret, testPayload, "delivery-1", "pull_request"))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("replay = %d, want 202", resp.Code)
	}

	// Missing delivery id: acknowledged but not acted on.
	resp = httptest.NewRecorder()
	h.ServeHTTP(resp, signedRequest(t, srv.Secret, testPayload, "", "pull_request"))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("missing delivery id = %d, want 202", resp.Code)
	}

	if calls := runner.waitCalls(t, 1); len(calls) != 1 {
		t.Fatalf("runner called %d times, want exactly 1", len(calls))
	}
}

// TestWebhookEventFiltering verifies only pull_request/ready_for_review events
// for the configured repo act; everything else is 202-ignored.
func TestWebhookEventFiltering(t *testing.T) {
	srv, runner := newTestServer(t)
	srv.Repo = "smallnest/pigo"
	h := srv.Handler()

	cases := []struct {
		name    string
		event   string
		payload string
	}{
		{"non pull_request event", "ping", testPayload},
		{"non ready action", "pull_request", strings.Replace(testPayload, `"ready_for_review"`, `"opened"`, 1)},
		{"repo filter mismatch", "pull_request", strings.Replace(testPayload, `"smallnest/pigo"`, `"other/repo"`, 1)},
	}
	for _, tc := range cases {
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, signedRequest(t, srv.Secret, tc.payload, "d-"+tc.name, tc.event))
		if resp.Code != http.StatusAccepted {
			t.Errorf("%s = %d, want 202", tc.name, resp.Code)
		}
	}

	// The accepted event still passes the repo filter.
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, signedRequest(t, srv.Secret, testPayload, "d-accept", "pull_request"))
	if resp.Code != http.StatusOK {
		t.Fatalf("accepted event = %d, want 200", resp.Code)
	}
	if calls := runner.waitCalls(t, 1); len(calls) != 1 || calls[0].Repo != "smallnest/pigo" {
		t.Fatalf("runner calls = %+v, want one smallnest/pigo review", calls)
	}
}

// TestWebhookRouteIsolation verifies the isolated-endpoint contract: only
// POST /github exists; every other path or method is a bare 404.
func TestWebhookRouteIsolation(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/github"},
		{http.MethodGet, "/"},
		{http.MethodPost, "/"},
		{http.MethodPost, "/github/extra"},
		{http.MethodPost, "/api"},
	} {
		req, err := http.NewRequest(tc.method, tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, req)
		if resp.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, resp.Code)
		}
	}
}

// TestWebhookCreatesReadOnlyReviewSession is the end-to-end proof: one valid
// delivery creates a persisted session whose first turn is the read-only
// review prompt, and the runner receives the request with the session id.
func TestWebhookCreatesReadOnlyReviewSession(t *testing.T) {
	srv, runner := newTestServer(t)
	h := srv.Handler()

	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, signedRequest(t, srv.Secret, testPayload, "delivery-e2e", "pull_request"))
	if resp.Code != http.StatusOK {
		t.Fatalf("delivery = %d, want 200", resp.Code)
	}

	calls := runner.waitCalls(t, 1)
	req := calls[0]
	if req.PRNumber != 42 || req.PRTitle != "Add scheduler" || req.Branch != "feat/scheduler" {
		t.Fatalf("runner request = %+v, want the parsed PR fields", req)
	}
	if req.SessionID == "" {
		t.Fatal("runner request carries no session id")
	}

	// The session is durable before the runner starts, with the review prompt
	// as its first user turn.
	header, entries, err := srv.Store.LoadEntries(req.SessionID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("session entries = %d, want 1", len(entries))
	}
	u, ok := entries[0].Message.(agentcore.UserMessage)
	if !ok {
		t.Fatalf("first entry = %T, want user message", entries[0].Message)
	}
	text := agentcore.ContentToText(u.Content)
	for _, want := range []string{"READ-ONLY", "#42", "Add scheduler"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt missing %q:\n%s", want, text)
		}
	}
	if header.ID != req.SessionID {
		t.Errorf("header.ID = %q, want %q", header.ID, req.SessionID)
	}

	// The tool set is read-only: exactly read/grep/find.
	names := map[string]bool{}
	for _, t2 := range ReadOnlyTools("/") {
		names[t2.Name()] = true
	}
	if len(names) != 3 || !names["read"] || !names["grep"] || !names["find"] {
		t.Fatalf("ReadOnlyTools = %v, want exactly read/grep/find", names)
	}
}

// fakeStreamProvider answers every request with one text turn, enough to drive
// the default runner through StartRun/DrainStream.
type fakeStreamProvider struct{ calls int }

func (p *fakeStreamProvider) Name() string { return "fake" }
func (p *fakeStreamProvider) Models() []provider.Model {
	return []provider.Model{{Provider: "fake", ID: "fake"}}
}
func (p *fakeStreamProvider) StreamCompletion(ctx context.Context, _ provider.CompletionRequest) (*provider.AssistantMessageEventStream, error) {
	p.calls++
	s := provider.NewAssistantMessageEventStream(0)
	go func() {
		_ = s.Emit(ctx, provider.StreamStartEvent{Partial: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}})
		_ = s.Emit(ctx, provider.StreamDoneEvent{Message: agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content:   agentcore.ContentList{agentcore.NewTextContent("review complete")},
		}})
		s.Close()
	}()
	return s, nil
}

// TestDefaultRunnerPersistsReply verifies the default runner resumes the
// review session, runs the loop read-only, and appends the reply to the same
// session file.
func TestDefaultRunnerPersistsReply(t *testing.T) {
	srv, _ := newTestServer(t)
	prov := &fakeStreamProvider{}
	srv.Provider = prov
	srv.Model = "fake"
	srv.ProviderName = "fake"
	runner := srv.DefaultRunner()

	req := ReviewRequest{Repo: "smallnest/pigo", PRNumber: 42, PRTitle: "Add scheduler", Prompt: ReviewPrompt(ReviewRequest{Repo: "smallnest/pigo", PRNumber: 42, PRTitle: "Add scheduler"})}
	if err := srv.createReviewSession(&req); err != nil {
		t.Fatalf("createReviewSession: %v", err)
	}
	if err := runner(context.Background(), req); err != nil {
		t.Fatalf("runner: %v", err)
	}

	_, entries, err := srv.Store.LoadEntries(req.SessionID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("session entries = %d, want 2 (prompt + reply)", len(entries))
	}
	a, ok := entries[1].Message.(agentcore.AssistantMessage)
	if !ok || agentcore.ContentToText(a.Content) != "review complete" {
		t.Fatalf("second entry = %T %+v, want the assistant reply", entries[1].Message, entries[1].Message)
	}
	if prov.calls != 1 {
		t.Errorf("provider called %d times, want 1", prov.calls)
	}
}
