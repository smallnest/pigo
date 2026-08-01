package memory

// Scope identifies the memory dimension a file belongs to.
type Scope string

// Recognized scopes.
const (
	ScopeGlobal   Scope = "global"
	ScopeProjects Scope = "projects"
	ScopeSessions Scope = "sessions"
	ScopeCC       Scope = "cc"
)

// Type identifies the semantic kind of a memory file.
type Type string

// Recognized types.
const (
	TypeUser       Type = "user"
	TypeFeedback   Type = "feedback"
	TypeProject    Type = "project"
	TypeReference  Type = "reference"
	TypeCheckpoint Type = "checkpoint"
	TypeProgress   Type = "progress"
	TypeNotes      Type = "notes"
	TypeFree       Type = "free"
)

// schemaDDL is the idempotent set of DDL statements that create the memory
// storage schema: the content table, its secondary indexes, the FTS5 virtual
// table (external-content mode), and the three sync triggers that keep the FTS
// index consistent with the content table. All statements use IF NOT EXISTS so
// running the migration repeatedly is safe.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS memory_index (
  id INTEGER PRIMARY KEY,
  path TEXT NOT NULL UNIQUE,
  scope TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL,
  body TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  last_indexed_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS memory_index_scope_idx ON memory_index (scope, scope_id);
CREATE INDEX IF NOT EXISTS memory_index_type_idx ON memory_index (type);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
  body, content='memory_index', content_rowid='id',
  tokenize='unicode61 remove_diacritics 1'
);

CREATE TRIGGER IF NOT EXISTS memory_ai AFTER INSERT ON memory_index BEGIN
  INSERT INTO memory_fts(rowid, body) VALUES (new.id, new.body);
END;

CREATE TRIGGER IF NOT EXISTS memory_ad AFTER DELETE ON memory_index BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, body) VALUES('delete', old.id, old.body);
END;

CREATE TRIGGER IF NOT EXISTS memory_au AFTER UPDATE ON memory_index BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, body) VALUES('delete', old.id, old.body);
  INSERT INTO memory_fts(rowid, body) VALUES (new.id, new.body);
END;
`
