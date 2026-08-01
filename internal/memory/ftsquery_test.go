package memory

import "testing"

func TestBuildFtsQueryMultiWordOrJoin(t *testing.T) {
	got := buildFtsQuery("permission deadlock retry")
	want := `"permission" OR "deadlock" OR "retry"`
	if got != want {
		t.Fatalf("buildFtsQuery multi-word: got %q want %q", got, want)
	}
}

func TestBuildFtsQuerySingleToken(t *testing.T) {
	if got := buildFtsQuery("checkpoint"); got != `"checkpoint"` {
		t.Fatalf("buildFtsQuery single: got %q", got)
	}
}

func TestBuildFtsQueryCJKTokensKept(t *testing.T) {
	// CJK letters are \p{L} and must survive tokenization. Whitespace splits
	// them into separate tokens; punctuation is a separator.
	got := buildFtsQuery("配置文件 端口")
	want := `"配置文件" OR "端口"`
	if got != want {
		t.Fatalf("buildFtsQuery CJK: got %q want %q", got, want)
	}
}

func TestBuildFtsQueryPunctuationStripped(t *testing.T) {
	// FTS5 special chars and punctuation become separators; underscores and
	// digits are word characters.
	got := buildFtsQuery(`port: 5433 (postgres-db)  foo_bar`)
	want := `"port" OR "5433" OR "postgres" OR "db" OR "foo_bar"`
	if got != want {
		t.Fatalf("buildFtsQuery punctuation: got %q want %q", got, want)
	}
}

func TestBuildFtsQueryStripsEmbeddedQuotes(t *testing.T) {
	// A token can only contain word chars, so a raw double-quote is a
	// separator; but guard the strip explicitly.
	got := buildFtsQuery(`say "hello"`)
	want := `"say" OR "hello"`
	if got != want {
		t.Fatalf("buildFtsQuery quotes: got %q want %q", got, want)
	}
}

func TestBuildFtsQueryEmptyAndWhitespace(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n", "!!! ??? ... ---", "()[]{}"} {
		if got := buildFtsQuery(in); got != "" {
			t.Fatalf("buildFtsQuery(%q): got %q want empty", in, got)
		}
	}
}
