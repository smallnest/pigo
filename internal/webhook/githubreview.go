// This file implements the GitHub ready-for-review webhook (issue #567,
// ported from dsh's GitHub review sessions): an opt-in, isolated HTTP endpoint
// that turns a PR's draft→ready transition into a read-only review session.
//
// Design, mirroring dsh's isolation contract:
//
//   - The endpoint mounts exactly one route (POST /github); every other path
//     and method is a bare 404, so the listener exposes nothing else even when
//     a TLS reverse proxy forwards a public URL to it.
//   - Authenticity: the payload body is verified against the shared webhook
//     secret via X-Hub-Signature-256 (HMAC-SHA256, constant-time compare).
//     The secret is supplied indirectly (an env-var name resolved by the CLI),
//     never inline in config.
//   - Replay protection: deliveries are deduplicated by the X-GitHub-Delivery
//     id (a bounded, TTL-pruned cache); duplicates answer 202 and re-run
//     nothing.
//   - Event filter: only pull_request events with action ready_for_review are
//     acted on; everything else answers 202 and is ignored. An optional repo
//     filter ("owner/name") restricts which repository triggers review.
//   - Delivery: on a match the server creates a persisted session carrying the
//     review prompt, then hands the request to the injected Runner in its own
//     goroutine, answering 200 immediately (GitHub expects a fast response).
//
// The review run is read-only by construction: its tool set is the read/grep/
// find subset, so the model can inspect the diff but cannot modify the
// workspace or execute commands.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
)

// maxWebhookBody bounds one webhook payload (GitHub caps PR payloads far
// below this; the limit only guards against unbounded reads).
const maxWebhookBody = 5 << 20

// deliveryTTL bounds how long a delivery id is remembered. GitHub retries
// within minutes, so a day is generous; the cache is pruned on insert.
const deliveryTTL = 24 * time.Hour

// reviewRunTimeout ceilings one detached review run. Reviews are unattended
// (nobody waits on the HTTP response), so the bound exists only to reap a hung
// provider, not to enforce a service level.
const reviewRunTimeout = 30 * time.Minute

// ReviewRequest is one accepted ready-for-review event, handed to Runner after
// the review session has been created and persisted.
type ReviewRequest struct {
	Repo      string // "owner/name"
	PRNumber  int
	PRTitle   string
	PRURL     string
	Branch    string
	SessionID string // the persisted review session the prompt was written to
	Prompt    string // the read-only review prompt (already the session's first turn)
}

// Runner executes one review request. It runs on its own goroutine, off the
// HTTP request path. The default runner (Server.DefaultRunner) drives a real
// read-only agent run; tests inject fakes.
type Runner func(ctx context.Context, req ReviewRequest) error

// Server is the isolated GitHub review webhook endpoint.
type Server struct {
	// Secret is the shared GitHub webhook secret (high-entropy); required.
	Secret string
	// Repo, when non-empty, restricts review to "owner/name"; empty accepts
	// every repository's events.
	Repo string
	// Store persists review sessions.
	Store *session.Store
	// Workspace is the directory the review runs in (and sessions attribute).
	Workspace string
	// Model / ProviderName / SysPrompt stamp the created session's header and
	// feed the default runner.
	Model        string
	ProviderName string
	SysPrompt    string
	// Provider is the resolved provider the default runner streams from.
	Provider provider.Provider
	// Runner is called (in its own goroutine) for each accepted event. Required.
	Runner Runner
	// Now is the clock for delivery-cache pruning and session stamps; tests pin it.
	Now func() time.Time

	seenMu sync.Mutex
	seen   map[string]time.Time
}

func (s *Server) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// readOnlyToolNames is the allowlist the review run is constrained to: the
// model can inspect the workspace but cannot write, edit, execute, or fan out.
var readOnlyToolNames = map[string]struct{}{"read": {}, "grep": {}, "find": {}}

// ReadOnlyTools returns the read-only subset of pigo's built-in tools, the tool
// set a review session runs with.
func ReadOnlyTools(cwd string) []agentcore.AgentTool {
	var out []agentcore.AgentTool
	for _, t := range run.BuiltinTools(cwd, false) {
		if _, ok := readOnlyToolNames[t.Name()]; ok {
			out = append(out, t)
		}
	}
	return out
}

// ReviewPrompt builds the read-only review prompt for one PR event.
func ReviewPrompt(req ReviewRequest) string {
	return fmt.Sprintf(`Review pull request #%d in %s: "%s" (%s).

You are in READ-ONLY review mode: the read/grep/find tools are available, but do
not modify files, run commands, or attempt to switch tools. Inspect the working
tree (which holds the PR's branch checked out) and produce:
1. A one-paragraph summary of what the PR changes.
2. Findings: correctness risks, edge cases, and security concerns, each with the
   file and line you base it on.
3. Suggested follow-ups, ordered by importance.`, req.PRNumber, req.Repo, req.PRTitle, req.PRURL)
}

// githubPullEvent is the minimal pull_request webhook payload shape.
type githubPullEvent struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
}

// Handler returns the isolated HTTP handler: exactly POST /github; every other
// method/path is a bare 404 so the listener exposes nothing else.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/github", s.handleGitHub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/github" && r.Method == http.MethodPost {
			mux.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

// handleGitHub verifies, filters, and dispatches one webhook delivery.
func (s *Server) handleGitHub(w http.ResponseWriter, r *http.Request) {
	if s.Secret == "" {
		http.Error(w, "webhook secret not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	if !verifySignature(r.Header.Get("X-Hub-Signature-256"), s.Secret, body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if delivery == "" || !s.rememberDelivery(delivery) {
		// A missing delivery id or a replay: acknowledge without acting.
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, "duplicate or unidentifiable delivery; ignored")
		return
	}
	if event := r.Header.Get("X-GitHub-Event"); event != "pull_request" {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "event %q ignored; only pull_request is handled\n", event)
		return
	}

	var ev githubPullEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "malformed payload", http.StatusBadRequest)
		return
	}
	if ev.Action != "ready_for_review" || (s.Repo != "" && ev.Repository.FullName != s.Repo) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "pull_request action %q for %q ignored\n", ev.Action, ev.Repository.FullName)
		return
	}

	req := ReviewRequest{
		Repo:     ev.Repository.FullName,
		PRNumber: ev.PullRequest.Number,
		PRTitle:  ev.PullRequest.Title,
		PRURL:    ev.PullRequest.HTMLURL,
		Branch:   ev.PullRequest.Head.Ref,
		Prompt:   ReviewPrompt(ReviewRequest{Repo: ev.Repository.FullName, PRNumber: ev.PullRequest.Number, PRTitle: ev.PullRequest.Title, PRURL: ev.PullRequest.HTMLURL}),
	}
	if err := s.createReviewSession(&req); err != nil {
		http.Error(w, "session creation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The run outlives the HTTP exchange: r.Context() is canceled the moment
	// this handler returns, so the runner gets a detached context with a
	// generous ceiling instead.
	runCtx, cancel := context.WithTimeout(context.Background(), reviewRunTimeout)
	go func() {
		defer cancel()
		_ = s.Runner(runCtx, req)
	}()
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "review session %s created for %s#%d\n", req.SessionID, req.Repo, req.PRNumber)
}

// createReviewSession persists the review session with the review prompt as its
// first (user) turn, so /resume can continue it and the run's input is durable
// before the runner starts.
func (s *Server) createReviewSession(req *ReviewRequest) error {
	now := s.clock()
	header := session.SessionHeader{
		ID:           session.NewID(now),
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        s.Model,
		Provider:     s.ProviderName,
		SystemPrompt: s.SysPrompt,
		Cwd:          s.Workspace,
	}
	msgs := agentcore.MessageList{agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(req.Prompt)},
	}}
	if err := s.Store.Save(header, msgs); err != nil {
		return err
	}
	req.SessionID = header.ID
	return nil
}

// rememberDelivery records a delivery id, pruning expired entries first, and
// reports whether it had not been seen before.
func (s *Server) rememberDelivery(id string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if s.seen == nil {
		s.seen = make(map[string]time.Time)
	}
	now := s.clock()
	for k, at := range s.seen {
		if now.Sub(at) > deliveryTTL {
			delete(s.seen, k)
		}
	}
	if _, dup := s.seen[id]; dup {
		return false
	}
	s.seen[id] = now
	return true
}

// verifySignature checks X-Hub-Signature-256 ("sha256=<hex>") against the body
// with a constant-time compare.
func verifySignature(header, secret string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// DefaultRunner drives a real read-only agent run for one review request: the
// session (already persisted with the prompt) is resumed into context, the run
// streams against the server's provider with only the read-only tools, and the
// produced messages are appended back to the same session.
func (s *Server) DefaultRunner() Runner {
	return func(ctx context.Context, req ReviewRequest) error {
		_, entries, err := s.Store.LoadEntries(req.SessionID)
		if err != nil {
			return err
		}
		msgs := make(agentcore.MessageList, len(entries))
		for i, e := range entries {
			msgs[i] = e.Message
		}
		agentCtx := &agentcore.AgentContext{Messages: msgs, Tools: ReadOnlyTools(s.Workspace)}
		creds := provider.NewCredentialStore(nil)
		reg := agenttool.NewToolRegistry()
		for _, t := range agentCtx.Tools {
			_ = reg.Register(t)
		}
		cfg := run.NewConfig(s.Model, s.ProviderName, "", s.Provider, creds, reg, nil, nil)
		stream := runtime.StartRun(ctx, agentCtx, cfg)
		if _, err := runtime.DrainStream(ctx, stream, runtime.StreamHandler{}); err != nil {
			return err
		}
		if len(agentCtx.Messages) > len(msgs) {
			return s.Store.Append(req.SessionID, s.clock().UTC(), agentCtx.Messages[len(msgs):])
		}
		return nil
	}
}
