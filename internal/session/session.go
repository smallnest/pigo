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
	"errors"
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
	// ParentSession is the id of the session this one was forked/cloned from
	// (US-006, #122). Empty for a session created from scratch. It records
	// lineage only; a fork is otherwise a fully independent session file.
	ParentSession string `json:"parentSession,omitempty"`
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

// TreeLine pairs one entry with its rendered display line, so a caller can print
// the tree AND map a 1-based selection index back to the entry it refers to (the
// slice order is the render order — a stable pre-order DFS). See RenderTreeLines.
type TreeLine struct {
	Entry Entry
	Text  string
}

// RenderTreeLines renders the entry forest as human-readable lines for a pure
// line REPL — no TUI, no cursor control (US-007, #123). Each entry becomes one
// line with ├─/└─ connectors showing structure; the entry whose id == leafID is
// tagged "← current" so the active branch is obvious. Roots (entries with an
// empty or dangling ParentID) anchor the forest; children are ordered by
// timestamp then id for stable output. The returned slice is in render order, so
// element i corresponds to the i-th printed line (and 1-based selector n → [n-1]).
func RenderTreeLines(entries []Entry, leafID string) []TreeLine {
	present := make(map[string]bool, len(entries))
	for _, e := range entries {
		present[e.ID] = true
	}
	childrenOf := make(map[string][]Entry, len(entries))
	var roots []Entry
	for _, e := range entries {
		if e.ParentID == "" || !present[e.ParentID] {
			roots = append(roots, e)
		} else {
			childrenOf[e.ParentID] = append(childrenOf[e.ParentID], e)
		}
	}
	sortEntries(roots)
	for k := range childrenOf {
		sortEntries(childrenOf[k])
	}

	var lines []TreeLine
	var walk func(e Entry, prefix string, isRoot, isLast bool)
	walk = func(e Entry, prefix string, isRoot, isLast bool) {
		connector := ""
		if !isRoot {
			if isLast {
				connector = "└─ "
			} else {
				connector = "├─ "
			}
		}
		marker := ""
		if e.ID == leafID {
			marker = "  ← current"
		}
		lines = append(lines, TreeLine{Entry: e, Text: prefix + connector + entrySummary(e) + marker})

		childPrefix := prefix
		if !isRoot {
			if isLast {
				childPrefix += "   "
			} else {
				childPrefix += "│  "
			}
		}
		kids := childrenOf[e.ID]
		for i, k := range kids {
			walk(k, childPrefix, false, i == len(kids)-1)
		}
	}
	for i, r := range roots {
		walk(r, "", true, i == len(roots)-1)
	}
	return lines
}

// sortEntries orders entries by timestamp, breaking ties by id so the render is
// deterministic even when entries share a timestamp (common within one turn).
func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if !es[i].Timestamp.Equal(es[j].Timestamp) {
			return es[i].Timestamp.Before(es[j].Timestamp)
		}
		return es[i].ID < es[j].ID
	})
}

// entrySummary renders a one-line, role-tagged preview of an entry's message for
// the tree display (a full message would wrap and ruin the ASCII structure).
func entrySummary(e Entry) string {
	switch m := e.Message.(type) {
	case agentcore.UserMessage:
		return "user: " + treeOneLine(agentcore.ContentToText(m.Content))
	case agentcore.AssistantMessage:
		text := treeOneLine(agentcore.ContentToText(m.Content))
		calls := m.ToolCalls()
		if len(calls) > 0 {
			names := make([]string, len(calls))
			for i, c := range calls {
				names[i] = c.Name
			}
			tools := "[→ " + strings.Join(names, ", ") + "]"
			if text != "" {
				return "assistant: " + text + " " + tools
			}
			return "assistant " + tools
		}
		return "assistant: " + text
	case agentcore.ToolResultMessage:
		return "tool result: " + treeOneLine(agentcore.ContentToText(m.Content))
	case agentcore.CompactionMessage:
		return "compaction: " + treeOneLine(m.Summary)
	default:
		return e.Message.Role()
	}
}

// treeOneLine collapses a possibly multi-line message into a single trimmed,
// truncated line so tree rows stay on one physical line.
func treeOneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	const max = 72
	if len(s) > max {
		s = s[:max] + " …"
	}
	return s
}

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
	return s.atomicWrite(header.ID, func(w io.Writer) error {
		return writeSession(w, header, messages)
	})
}

// SaveEntries writes header plus the given entries verbatim — preserving each
// entry's id/parentId — to a fresh session file, overwriting any existing file
// for header.ID. Unlike Save (which generates fresh ids and a linear chain from
// a MessageList), SaveEntries persists an already-known tree, which is what Fork
// needs: it copies a path of existing entries into a new session file without
// disturbing their identifiers. The header's Version is forced to SchemaVersion.
func (s *Store) SaveEntries(header SessionHeader, entries []Entry) error {
	header.Version = SchemaVersion
	if header.ID == "" {
		return fmt.Errorf("session: header ID must not be empty")
	}
	return s.atomicWrite(header.ID, func(w io.Writer) error {
		return writeSessionEntries(w, header, entries)
	})
}

// atomicWrite writes a session file for id by streaming through write into a
// temp file and atomically renaming it into place, so a concurrent reader never
// sees a half-written file. It is the shared write plumbing behind Save and
// SaveEntries.
func (s *Store) atomicWrite(id string, write func(w io.Writer) error) error {
	tmp := s.path(id) + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("session: create %s: %w", tmp, err)
	}
	w := bufio.NewWriter(f)
	if err := write(w); err != nil {
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
	if err := os.Rename(tmp, s.path(id)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("session: commit %s: %w", id, err)
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

// writeSessionEntries emits the header line followed by the given entries
// verbatim, preserving their ids and parentIds. It is the whole-tree counterpart
// to writeSession (which synthesizes a fresh linear chain from a MessageList).
func writeSessionEntries(w io.Writer, header SessionHeader, entries []Entry) error {
	enc := json.NewEncoder(w)
	if err := enc.Encode(header); err != nil {
		return fmt.Errorf("session: encode header: %w", err)
	}
	for i, e := range entries {
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("session: encode entry[%d]: %w", i, err)
		}
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

// AppendBranch appends messages as a chain descending from parentLeafID,
// preserving every existing entry — and therefore any other branches — in the
// session file (US-007, #123). It is the tree-aware counterpart to Append (which
// rewrites the file as a single linear chain): where Append flattens, AppendBranch
// grows the on-disk tree, so switching the active leaf to a historical entry and
// continuing produces a real sibling branch rather than truncating history.
//
// Each message becomes a fresh entry (new id, ParentID chained to the previous
// one, first chained to parentLeafID). An empty parentLeafID roots the new chain.
// header (Version forced to SchemaVersion, UpdatedAt as the caller set it) is
// rewritten as line 1. It returns the id of the new leaf — the last appended
// entry — so the caller can track the active branch. If the session file does not
// yet exist it is created (the fresh-session first-turn case).
func (s *Store) AppendBranch(header SessionHeader, parentLeafID string, messages agentcore.MessageList) (string, error) {
	if header.ID == "" {
		return "", fmt.Errorf("session: header ID must not be empty")
	}
	var entries []Entry
	if _, existing, err := s.LoadEntries(header.ID); err == nil {
		entries = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	now := time.Now().UTC()
	parent := parentLeafID
	leaf := parentLeafID
	for _, m := range messages {
		e := Entry{ID: newEntryID(), ParentID: parent, Timestamp: now, Message: m}
		entries = append(entries, e)
		parent = e.ID
		leaf = e.ID
	}
	if err := s.SaveEntries(header, entries); err != nil {
		return "", err
	}
	return leaf, nil
}

// Fork creates a new session whose contents are the linear path from the root
// down to leafID in the source session, copied verbatim (each entry keeps its
// id/parentId). The new session gets a fresh id (derived from now) and its
// header carries ParentSession = sourceID so the lineage is recorded. It returns
// the new session's header and its entries.
//
// This is the primitive behind /fork and /clone (US-006, #122):
//
//   - /clone passes the current leaf id → the entire current conversation is
//     duplicated into an independent session (position "at").
//   - /fork passes a historical user message's PARENT id → the new session holds
//     everything up to but excluding that message, so the user re-prompts from
//     that point on a fresh branch (position "before").
//
// Because the copy lands in a brand-new file, appending to either the source or
// the fork never touches the other — the two branches are fully isolated. An
// empty leafID copies nothing but the header (an empty new session rooted at the
// source), which is the correct behavior for forking before the very first
// message.
func (s *Store) Fork(sourceID, leafID string, now time.Time) (SessionHeader, []Entry, error) {
	srcHeader, entries, err := s.LoadEntries(sourceID)
	if err != nil {
		return SessionHeader{}, nil, err
	}
	path := PathToLeaf(entries, leafID)
	newHeader := SessionHeader{
		ID:            NewID(now),
		CreatedAt:     now,
		UpdatedAt:     now,
		Model:         srcHeader.Model,
		Provider:      srcHeader.Provider,
		SystemPrompt:  srcHeader.SystemPrompt,
		ParentSession: sourceID,
	}
	if err := s.SaveEntries(newHeader, path); err != nil {
		return SessionHeader{}, nil, err
	}
	return newHeader, path, nil
}
