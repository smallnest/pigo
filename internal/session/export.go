// This file implements session export/import (US-008, #124): a session can be
// exported to a self-contained JSONL or HTML file, and a JSONL export can be
// imported back as a fresh, resumable session. The JSONL form is the same
// role-discriminated schema the store persists, so an export → import round-trip
// is lossless (message contents and the id/parentId tree survive verbatim); the
// HTML form is a read-only, self-contained transcript with inline styles and no
// external network resources, suitable for sharing.
package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteJSONL writes header + entries as JSONL in the store's on-disk schema
// (header line first, then one entry line each, ids/parentIds preserved). It is
// the export counterpart to writeSessionEntries and the exact input ReadJSONL
// expects, so a WriteJSONL → ReadJSONL round-trip is lossless.
func WriteJSONL(w io.Writer, header SessionHeader, entries []Entry) error {
	header.Version = SchemaVersion
	return writeSessionEntries(w, header, entries)
}

// ReadJSONL decodes a JSONL export (as produced by WriteJSONL or a raw session
// file) into a header and entries, migrating v1/v2 bare-message files the same
// way LoadEntries does. It is the import counterpart to WriteJSONL.
func ReadJSONL(r io.Reader) (SessionHeader, []Entry, error) {
	return readSession(r)
}

// Export writes the session identified by id to outPath. The format is chosen
// by outPath's extension: ".html"/".htm" produces a self-contained HTML
// transcript; anything else (including ".jsonl") produces JSONL. The parent
// directory of outPath must already exist. It returns the number of entries
// written so a caller can report progress.
func (s *Store) Export(id, outPath string) (int, error) {
	header, entries, err := s.LoadEntries(id)
	if err != nil {
		return 0, err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("session: create export %s: %w", outPath, err)
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(outPath))
	if ext == ".html" || ext == ".htm" {
		if err := WriteHTML(f, header, entries); err != nil {
			return 0, err
		}
	} else {
		if err := WriteJSONL(f, header, entries); err != nil {
			return 0, err
		}
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("session: finalize export %s: %w", outPath, err)
	}
	return len(entries), nil
}

// Import reads a JSONL export at inPath and materializes it as a fresh session
// in the store: a new id (derived from now) is assigned, the original id is
// recorded as ParentSession for lineage, and the entries are written verbatim
// (ids/parentIds preserved) so the tree — and thus PathToLeaf/resume — behaves
// exactly as in the source. It returns the new header and the imported entries.
// An HTML file (or any non-JSONL input) fails to parse and returns an error
// rather than importing garbage.
func (s *Store) Import(inPath string, now time.Time) (SessionHeader, []Entry, error) {
	f, err := os.Open(inPath)
	if err != nil {
		return SessionHeader{}, nil, fmt.Errorf("session: open import %s: %w", inPath, err)
	}
	defer f.Close()
	srcHeader, entries, err := ReadJSONL(f)
	if err != nil {
		return SessionHeader{}, nil, err
	}
	newHeader := SessionHeader{
		ID:            NewID(now),
		CreatedAt:     now,
		UpdatedAt:     now,
		Model:         srcHeader.Model,
		Provider:      srcHeader.Provider,
		SystemPrompt:  srcHeader.SystemPrompt,
		ParentSession: srcHeader.ID,
	}
	if err := s.SaveEntries(newHeader, entries); err != nil {
		return SessionHeader{}, nil, err
	}
	return newHeader, entries, nil
}

// WriteHTML writes a self-contained HTML transcript of the session: inline CSS
// only (no external stylesheets, fonts, scripts, or network resources), role
// color-coding, and tool-call/result blocks. All message text is HTML-escaped
// so a transcript containing markup or a crafted "</script>" cannot break out of
// its container or inject active content (defensive against a hostile session).
func WriteHTML(w io.Writer, header SessionHeader, entries []Entry) error {
	var b strings.Builder
	b.WriteString(htmlHead(header))
	for _, e := range entries {
		b.WriteString(renderEntryHTML(e))
	}
	b.WriteString(htmlFoot())
	_, err := io.WriteString(w, b.String())
	return err
}
