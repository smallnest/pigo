// Package headless drives pigo's non-interactive run paths: the print /
// stream-json headless run, the session listing/resume helpers, and the
// process-isolated sub-agent JSON-RPC server (--subagent-rpc).
//
// This file gives headless / stream-json runs the same session persistence and
// resume the interactive REPL has (cmd/pigo/interactive.go). Before this, a
// headless run built an in-memory AgentContext and threw it away on exit, so
// `--output-format stream-json` emitted no session id and `--resume`/`--continue`
// only worked in the REPL.
//
// Now a headless run is backed by a session file: resuming seeds the context
// from a prior session (and re-anchors the branch leaf), a fresh run creates a
// new session, and in both cases the run's newly produced messages are appended
// after it completes. The session id is threaded into the run so it appears in
// the first stream-json event (对标 pi/Claude Code) and can be passed back via
// --resume to continue the run.
package headless

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
)

// SessionStore returns the session store rooted at ~/.pigo/sessions (or under
// PIGO_HOME when set), creating the directory on first use. It is shared by the
// headless run path and the interactive REPL.
func SessionStore() (*session.Store, error) {
	dir := os.Getenv("PIGO_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		dir = filepath.Join(home, ".pigo")
	}
	return session.NewStore(filepath.Join(dir, "sessions"))
}

// PrintSessions prints the stored sessions, most-recent first, to out.
func PrintSessions(out io.Writer) error {
	store, err := SessionStore()
	if err != nil {
		return err
	}
	headers, err := store.List()
	if err != nil {
		return err
	}
	if len(headers) == 0 {
		fmt.Fprintln(out, "no sessions")
		return nil
	}
	for _, h := range headers {
		fmt.Fprintf(out, "%s\t%s\t%s\n", h.ID, h.UpdatedAt.Local().Format("2006-01-02 15:04"), h.Model)
	}
	return nil
}

// MostRecentSessionID returns the id of the most recently updated session, or
// "" if there are none.
func MostRecentSessionID() (string, error) {
	store, err := SessionStore()
	if err != nil {
		return "", err
	}
	headers, err := store.List()
	if err != nil {
		return "", err
	}
	if len(headers) == 0 {
		return "", nil
	}
	return headers[0].ID, nil
}

// headlessSession is the session state backing one headless run: the store, the
// header (whose ID is the session id emitted and used for resume), and the
// branch-tracking cursor (curLeaf/persisted) so the run's messages append as a
// branch descending from the resumed leaf rather than flattening the tree.
type headlessSession struct {
	store   *session.Store
	header  session.SessionHeader
	curLeaf string // active leaf id to descend from; "" for a fresh session
	// persisted is the number of agentCtx.Messages already on disk before the
	// run; persist appends only Messages[persisted:] as a new branch.
	persisted int
	// model/provider are the model and provider the run actually used, refreshed
	// onto the header before persisting so a resumed run does not write back the
	// original session's stale values (matching the REPL, repl.go persistTurn).
	model    string
	provider string
}

// openHeadlessSession resolves the session backing a headless run: it resumes an
// existing session when resumeID is set (seeding priorMsgs and re-anchoring the
// branch leaf) or creates a fresh session header otherwise. It returns the prior
// messages to seed into the context ahead of the new prompt, plus the session
// state used to persist the run afterward.
func openHeadlessSession(resumeID, model, providerName, sysPrompt string) (agentcore.MessageList, headlessSession, error) {
	store, err := SessionStore()
	if err != nil {
		return nil, headlessSession{}, err
	}
	now := time.Now().UTC()

	if resumeID != "" {
		h, entries, err := store.LoadEntries(resumeID)
		if err != nil {
			return nil, headlessSession{}, err
		}
		msgs := make(agentcore.MessageList, len(entries))
		for i, e := range entries {
			msgs[i] = e.Message
		}
		curLeaf := ""
		if len(entries) > 0 {
			curLeaf = entries[len(entries)-1].ID
		}
		// A resumed header keeps its own SystemPrompt when present so the run is
		// faithful to the original session.
		if h.SystemPrompt == "" {
			h.SystemPrompt = sysPrompt
		}
		return msgs, headlessSession{store: store, header: h, curLeaf: curLeaf, persisted: len(msgs), model: model, provider: providerName}, nil
	}

	header := session.SessionHeader{
		ID:           session.NewID(now),
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        model,
		Provider:     providerName,
		SystemPrompt: sysPrompt,
	}
	return nil, headlessSession{store: store, header: header, curLeaf: "", persisted: 0, model: model, provider: providerName}, nil
}

// persist appends the messages produced during the run — everything in
// agentCtx.Messages past what was already on disk — as a branch descending from
// the resumed leaf, matching how the REPL grows a session tree (AppendBranch).
// It is a no-op when the run produced nothing new. Errors are returned for the
// caller to surface; the run's output has already been emitted regardless.
func (hs *headlessSession) persist(agentCtx *agentcore.AgentContext) error {
	// Compaction can rebuild agentCtx.Messages to fewer entries than were on disk
	// before the run (loop.go maybeAutoCompact replaces the slice). Clamp the
	// cursor so the tail slice stays in bounds; when the context shrank there is
	// nothing new to append past what compaction kept.
	if hs.persisted > len(agentCtx.Messages) {
		hs.persisted = len(agentCtx.Messages)
	}
	tail := agentCtx.Messages[hs.persisted:]
	if len(tail) == 0 {
		return nil
	}
	// Refresh the header with the model/provider the run actually used so a
	// resumed session's metadata is not written back stale (matching the REPL).
	hs.header.Model = hs.model
	hs.header.Provider = hs.provider
	hs.header.UpdatedAt = time.Now().UTC()
	if _, err := hs.store.AppendBranch(hs.header, hs.curLeaf, tail); err != nil {
		return err
	}
	hs.persisted = len(agentCtx.Messages)
	return nil
}
