// Package session implements local JSONL session persistence and resume
// (US-024, #43). A session is stored as a single append-only JSONL file: the
// first line is a SessionHeader (schema version + metadata), and every
// subsequent line is one persisted message (user / assistant / toolResult),
// using the same "role"-discriminated encoding as agentcore.MessageList.
//
// The format is internally self-consistent and deliberately NOT wire-compatible
// with pi's session files (spec #16, 会话格式 decision #5): pigo owns the schema
// and versions it via SessionHeader.Version so future migrations have a hook.
//
// A persisted session round-trips into an agentcore.AgentContext via Load, so a
// run can be resumed by feeding the reconstructed context to a fresh run and the
// transcript replays correctly in the REPL.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// SchemaVersion is the current session file schema version. It is written into
// every SessionHeader and checked on read; an unknown (higher) version is a
// hard error so a newer file is never silently misread by an older binary.
//
// v2 adds the inline "compaction" message role (US-003): a compaction
// checkpoint persisted as one message line.
//
// v3 gives every persisted entry an id/parentId, forming a tree (US-005, #121)
// — the prerequisite for fork/clone/tree navigation. Each message line is
// wrapped as {"id","parentId","timestamp","message":{…}}. v1/v2 files (bare
// message lines) remain fully readable: readSession migrates them on load by
// synthesizing ids and chaining parentId to the previous entry, so old sessions
// still load and resume.
const SchemaVersion = 3

// sessionScanBufInit / sessionScanBufMax bound the line scanner used to read a
// session file. A single line holds one message, which can be large (a long
// tool result), so the max is raised well past bufio.Scanner's 64KiB default.
const (
	sessionScanBufInit = 64 * 1024
	sessionScanBufMax  = 16 * 1024 * 1024
)

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

// Entry wraps one persisted message with the tree metadata introduced in schema
// v3 (US-005, #121): a stable ID plus the ParentID it descends from. A linear
// session is the degenerate tree where every entry's ParentID is the previous
// entry's ID; the first entry has an empty ParentID (a root). The wrapper is
// what lets a session fork/clone later — PathToLeaf walks the ParentID chain to
// reconstruct the linear conversation feeding any leaf.
type Entry struct {
	// ID is this entry's stable identifier (see newEntryID). Unique within a file.
	ID string
	// ParentID is the ID this entry descends from; empty for a root entry.
	ParentID string
	// Timestamp is when the entry was persisted (RFC 3339, UTC).
	Timestamp time.Time
	// Message is the wrapped agent message (user / assistant / toolResult / …).
	Message agentcore.Message
}

// entryWire is the on-disk JSON shape of an Entry: the message is carried as a
// raw object so it round-trips through MessageList's role-discriminated decoder
// (agentcore.Message is a sealed interface with no default unmarshaler).
type entryWire struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

// MarshalJSON emits the entry as {"id","parentId","timestamp","message":{…}}.
func (e Entry) MarshalJSON() ([]byte, error) {
	mb, err := json.Marshal(e.Message)
	if err != nil {
		return nil, fmt.Errorf("session: encode entry message: %w", err)
	}
	return json.Marshal(entryWire{ID: e.ID, ParentID: e.ParentID, Timestamp: e.Timestamp, Message: mb})
}

// UnmarshalJSON decodes an entry line, decoding the inner message with the same
// discriminated logic as agentcore.MessageList (by wrapping it in a one-element
// array).
func (e *Entry) UnmarshalJSON(data []byte) error {
	var w entryWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	e.ID = w.ID
	e.ParentID = w.ParentID
	e.Timestamp = w.Timestamp
	if len(w.Message) == 0 {
		return fmt.Errorf("session: entry missing message")
	}
	var one agentcore.MessageList
	if err := json.Unmarshal([]byte("["+string(w.Message)+"]"), &one); err != nil {
		return fmt.Errorf("session: decode entry message: %w", err)
	}
	if len(one) != 1 {
		return fmt.Errorf("session: entry decoded to %d messages, want 1", len(one))
	}
	e.Message = one[0]
	return nil
}

// newEntryID returns a fresh 8-hex-character entry id (4 random bytes). This
// mirrors pi's generateEntryId (uuidv7().slice(-8)) in width; pigo does not need
// the time-ordering of uuidv7 because entry order is already given by the
// ParentID chain, so a simple random id suffices and collisions within a single
// file are astronomically unlikely.
func newEntryID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never fails in practice; fall back to a timestamp-derived
		// id so a session write never aborts on this path.
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

// PathToLeaf walks the ParentID chain from the entry identified by leafID back
// to its root and returns the entries in root→leaf order — the linear
// conversation that feeds the given leaf (US-005 acceptance criterion c). An
// empty leafID (or an unknown one) yields nil. If the chain references a missing
// parent the walk stops at the last resolvable ancestor rather than failing, so
// a partially corrupt file still yields the recoverable prefix.
func PathToLeaf(entries []Entry, leafID string) []Entry {
	if leafID == "" {
		return nil
	}
	byID := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	var rev []Entry
	seen := make(map[string]bool, len(entries))
	for id := leafID; id != ""; {
		e, ok := byID[id]
		if !ok || seen[id] {
			break // missing parent or a cycle: stop at the last good ancestor
		}
		seen[id] = true
		rev = append(rev, e)
		id = e.ParentID
	}
	// rev is leaf→root; reverse to root→leaf.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
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
func (s *Store) Save(header SessionHeader, messages agentcore.MessageList) error {
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

// writeSession emits the header line followed by one entry line per message.
// Each message is wrapped in an Entry: ids are generated fresh and ParentID is
// chained to the previous entry so a linear session is persisted as a linear
// tree (schema v3). The chain is what PathToLeaf later walks.
func writeSession(w io.Writer, header SessionHeader, messages agentcore.MessageList) error {
	enc := json.NewEncoder(w)
	if err := enc.Encode(header); err != nil {
		return fmt.Errorf("session: encode header: %w", err)
	}
	now := time.Now().UTC()
	parentID := ""
	for i, m := range messages {
		e := Entry{ID: newEntryID(), ParentID: parentID, Timestamp: now, Message: m}
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("session: encode message[%d]: %w", i, err)
		}
		parentID = e.ID
	}
	return nil
}

// Load reads a session file and returns its header and messages. It validates
// the schema version (an unknown version is rejected) and decodes each entry
// line, returning the messages in file order (root→leaf for a linear session).
// Old v1/v2 files (bare message lines) are migrated transparently. Load is the
// linear view; LoadEntries exposes the id/parentId tree metadata.
func (s *Store) Load(id string) (SessionHeader, agentcore.MessageList, error) {
	header, entries, err := s.LoadEntries(id)
	if err != nil {
		return SessionHeader{}, nil, err
	}
	msgs := make(agentcore.MessageList, len(entries))
	for i, e := range entries {
		msgs[i] = e.Message
	}
	return header, msgs, nil
}

// LoadEntries reads a session file and returns its header plus the tree entries
// (id/parentId + message) in file order. v1/v2 files are migrated on load:
// every bare message line is wrapped in an Entry with a synthesized id and its
// ParentID chained to the previous entry, so old sessions load and resume
// exactly as they did before (US-005 acceptance criterion b/d).
func (s *Store) LoadEntries(id string) (SessionHeader, []Entry, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		return SessionHeader{}, nil, fmt.Errorf("session: open %s: %w", id, err)
	}
	defer f.Close()
	return readSession(f)
}

// readSession decodes a session stream: header line first, then entries. For
// schema v3 each line is an Entry ({id,parentId,timestamp,message}). For older
// v1/v2 files each line is a bare message; readSession migrates them by
// synthesizing ids and chaining parentId to the previous entry.
func readSession(r io.Reader) (SessionHeader, []Entry, error) {
	sc := bufio.NewScanner(r)
	// Session lines can be large (long tool results); grow the buffer well past
	// the default 64KB token cap.
	sc.Buffer(make([]byte, 0, sessionScanBufInit), sessionScanBufMax)

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
	// v3+ lines are wrapped entries; v1/v2 lines are bare messages that we migrate.
	wrapped := header.Version >= 3

	var entries []Entry
	parentID := ""
	for line := 2; sc.Scan(); line++ {
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue // tolerate blank lines
		}
		var e Entry
		if wrapped {
			if err := json.Unmarshal(raw, &e); err != nil {
				return SessionHeader{}, nil, fmt.Errorf("session: parse entry line %d: %w", line, err)
			}
		} else {
			// Migrate a bare v1/v2 message line: reuse MessageList's discriminated
			// decoding, then synthesize the tree metadata (id + parentId chain).
			var one agentcore.MessageList
			if err := json.Unmarshal([]byte("["+string(raw)+"]"), &one); err != nil {
				return SessionHeader{}, nil, fmt.Errorf("session: parse message line %d: %w", line, err)
			}
			if len(one) != 1 {
				return SessionHeader{}, nil, fmt.Errorf("session: message line %d decoded to %d messages, want 1", line, len(one))
			}
			e = Entry{ID: newEntryID(), ParentID: parentID, Timestamp: header.UpdatedAt, Message: one[0]}
		}
		entries = append(entries, e)
		parentID = e.ID
	}
	if err := sc.Err(); err != nil {
		return SessionHeader{}, nil, fmt.Errorf("session: scan: %w", err)
	}
	return header, entries, nil
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
	sc.Buffer(make([]byte, 0, sessionScanBufInit), sessionScanBufMax)
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

// Append adds messages to an existing session file and bumps its UpdatedAt to
// updatedAt. It is the incremental-persistence primitive: a driver that wants to
// grow a session turn-by-turn appends only the newly produced messages rather
// than rewriting the whole file itself. If the session does not exist it is an
// error — use Save to create one first.
//
// Because the header lives on the first line and JSONL is otherwise
// append-only, updating UpdatedAt requires rewriting the file; Append does a
// load-modify-save under the hood, which is simple and correct for the session
// sizes pigo produces.
func (s *Store) Append(id string, updatedAt time.Time, messages agentcore.MessageList) error {
	header, existing, err := s.Load(id)
	if err != nil {
		return err
	}
	header.UpdatedAt = updatedAt
	existing = append(existing, messages...)
	return s.Save(header, existing)
}
