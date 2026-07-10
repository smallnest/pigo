// Package session implements local JSONL session persistence and resume
// (US-024, #43). A session is stored as a single append-only JSONL file: the
// first line is a SessionHeader (schema version + metadata), and every
// subsequent line is one persisted message (user / assistant / toolResult),
// using the same "role"-discriminated encoding as agent.MessageList.
//
// The format is internally self-consistent and deliberately NOT wire-compatible
// with pi's session files (spec #16, 会话格式 decision #5): pigo owns the schema
// and versions it via SessionHeader.Version so future migrations have a hook.
//
// A persisted session round-trips into an agent.AgentContext, so a run can be
// resumed by feeding the reconstructed context to agent.ContinueRun and the
// transcript replays correctly in the TUI.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agent"
)

// SchemaVersion is the current session file schema version. It is written into
// every SessionHeader and checked on read; an unknown (higher) version is a
// hard error so a newer file is never silently misread by an older binary.
const SchemaVersion = 1

// SessionHeader is the first line of a session file: schema version plus the
// metadata needed to list and resume a session without reading its messages.
type SessionHeader struct {
	// Version is the schema version (SchemaVersion at write time).
	Version int `json:"version"`
	// ID is the session identifier, also the file stem (see FileName).
	ID string `json:"id"`
	// CreatedAt is when the session file was created (RFC 3339, UTC).
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the session was last appended to.
	UpdatedAt time.Time `json:"updatedAt"`
	// Model / Provider record what the session ran against, for display and to
	// re-establish the run configuration on resume. Optional.
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// SystemPrompt is the system prompt the session ran under. Persisted so the
	// resumed context is faithful. Optional.
	SystemPrompt string `json:"systemPrompt,omitempty"`
}

// FileName returns the on-disk file name for a session id (id + ".jsonl").
func FileName(id string) string { return id + ".jsonl" }

// NewID returns a time-ordered session id: a UTC timestamp stem that sorts
// lexicographically by creation time (e.g. "20260710-142530-uniq"). The suffix
// disambiguates sessions created within the same second.
func NewID(now time.Time) string {
	return fmt.Sprintf("%s-%06d", now.UTC().Format("20060102-150405"), now.UTC().Nanosecond()/1000%1_000_000)
}

// Store persists sessions as JSONL files under a directory (typically
// ~/.pigo/sessions). The zero value is unusable; construct with NewStore.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir, creating the directory if needed.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("session: store dir must not be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: create store dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store's root directory.
func (s *Store) Dir() string { return s.dir }

// path returns the on-disk path for a session id.
func (s *Store) path(id string) string { return filepath.Join(s.dir, FileName(id)) }

// Save writes header and messages to a fresh session file, overwriting any
// existing file for the same id. The header's Version is forced to
// SchemaVersion; CreatedAt/UpdatedAt are left as the caller set them. Save is
// the whole-session write; Append adds messages to an existing file.
func (s *Store) Save(header SessionHeader, messages agent.MessageList) error {
	header.Version = SchemaVersion
	if header.ID == "" {
		return fmt.Errorf("session: header ID must not be empty")
	}
	tmp := s.path(header.ID) + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("session: create %s: %w", tmp, err)
	}
	w := bufio.NewWriter(f)
	if err := writeSession(w, header, messages); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("session: flush %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("session: close %s: %w", tmp, err)
	}
	// Atomic replace so a reader never sees a half-written file.
	if err := os.Rename(tmp, s.path(header.ID)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("session: commit %s: %w", header.ID, err)
	}
	return nil
}

// writeSession emits the header line followed by one line per message.
func writeSession(w io.Writer, header SessionHeader, messages agent.MessageList) error {
	enc := json.NewEncoder(w)
	if err := enc.Encode(header); err != nil {
		return fmt.Errorf("session: encode header: %w", err)
	}
	for i, m := range messages {
		if err := enc.Encode(m); err != nil {
			return fmt.Errorf("session: encode message[%d]: %w", i, err)
		}
	}
	return nil
}

// Load reads a session file and returns its header and messages. It validates
// the schema version (an unknown version is rejected) and decodes each message
// line using the same role-discriminated logic as agent.MessageList.
func (s *Store) Load(id string) (SessionHeader, agent.MessageList, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		return SessionHeader{}, nil, fmt.Errorf("session: open %s: %w", id, err)
	}
	defer f.Close()
	return readSession(f)
}

// readSession decodes a session stream: header line first, then messages.
func readSession(r io.Reader) (SessionHeader, agent.MessageList, error) {
	sc := bufio.NewScanner(r)
	// Session lines can be large (long tool results); grow the buffer well past
	// the default 64KB token cap.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return SessionHeader{}, nil, fmt.Errorf("session: read header: %w", err)
		}
		return SessionHeader{}, nil, fmt.Errorf("session: empty file (no header)")
	}
	var header SessionHeader
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		return SessionHeader{}, nil, fmt.Errorf("session: parse header: %w", err)
	}
	if header.Version == 0 {
		return SessionHeader{}, nil, fmt.Errorf("session: header missing version")
	}
	if header.Version > SchemaVersion {
		return SessionHeader{}, nil, fmt.Errorf("session: file schema version %d newer than supported %d", header.Version, SchemaVersion)
	}

	var messages agent.MessageList
	for line := 2; sc.Scan(); line++ {
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue // tolerate blank lines
		}
		// Reuse MessageList's discriminated decoding by wrapping the single object
		// in a one-element array.
		var one agent.MessageList
		if err := json.Unmarshal([]byte("["+string(raw)+"]"), &one); err != nil {
			return SessionHeader{}, nil, fmt.Errorf("session: parse message line %d: %w", line, err)
		}
		messages = append(messages, one...)
	}
	if err := sc.Err(); err != nil {
		return SessionHeader{}, nil, fmt.Errorf("session: scan: %w", err)
	}
	return header, messages, nil
}

// List returns the headers of all sessions in the store, sorted by UpdatedAt
// descending (most recently used first). Files that fail to parse are skipped
// rather than failing the whole listing, so one corrupt session does not hide
// the rest.
func (s *Store) List() ([]SessionHeader, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: read store dir: %w", err)
	}
	var headers []SessionHeader
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		h, err := s.loadHeader(id)
		if err != nil {
			continue // skip unreadable/corrupt session
		}
		headers = append(headers, h)
	}
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].UpdatedAt.After(headers[j].UpdatedAt)
	})
	return headers, nil
}

// loadHeader reads only the header line of a session file (cheap for listing).
func (s *Store) loadHeader(id string) (SessionHeader, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		return SessionHeader{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	if !sc.Scan() {
		return SessionHeader{}, fmt.Errorf("session: empty file")
	}
	var header SessionHeader
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		return SessionHeader{}, err
	}
	if header.Version == 0 {
		return SessionHeader{}, fmt.Errorf("session: missing version")
	}
	return header, nil
}

// Resume loads a session and reconstructs an agent.AgentContext from it, ready
// to hand to agent.ContinueRun. The system prompt is taken from the header. The
// returned header lets the caller re-establish model/provider. It errors if the
// session has no messages or its last message is an assistant message (nothing
// to continue from — mirrors agentLoopContinue's precondition).
func (s *Store) Resume(id string) (*agent.AgentContext, SessionHeader, error) {
	header, messages, err := s.Load(id)
	if err != nil {
		return nil, SessionHeader{}, err
	}
	if len(messages) == 0 {
		return nil, SessionHeader{}, fmt.Errorf("session %s: no messages to resume", id)
	}
	if _, isAssistant := messages[len(messages)-1].(agent.AssistantMessage); isAssistant {
		return nil, SessionHeader{}, fmt.Errorf("session %s: last message is an assistant message, nothing to continue", id)
	}
	ctx := &agent.AgentContext{
		SystemPrompt: header.SystemPrompt,
		Messages:     messages,
	}
	return ctx, header, nil
}

// Append adds messages to an existing session file and bumps its UpdatedAt to
// updatedAt. It is the incremental-persistence path: after each turn the driver
// appends only the newly produced messages rather than rewriting the whole
// file. The header's UpdatedAt is rewritten in place (line 0), then the new
// messages are appended. If the session does not exist it is an error — use
// Save to create one first.
//
// Because the header lives on the first line and JSONL is otherwise
// append-only, updating UpdatedAt requires rewriting the file; Append does a
// load-modify-save under the hood, which is simple and correct for the session
// sizes pigo produces. Callers that never need UpdatedAt precision can batch
// with Save at session end instead.
func (s *Store) Append(id string, updatedAt time.Time, messages agent.MessageList) error {
	header, existing, err := s.Load(id)
	if err != nil {
		return err
	}
	header.UpdatedAt = updatedAt
	existing = append(existing, messages...)
	return s.Save(header, existing)
}
