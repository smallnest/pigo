package memory

import "regexp"

// ftsTokenRe matches contiguous runs of Unicode letters (incl. CJK), numbers,
// and underscore. Everything else — whitespace, punctuation, FTS5 operator
// characters — is treated as a separator. \p{L} deliberately includes CJK
// letters so queries like "配置文件" tokenize into a single searchable run.
var ftsTokenRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// buildFtsQuery builds an FTS5 MATCH expression from a free-form user query.
//
// FTS5's MATCH grammar has its own operators and special characters
// (`"`, `(`, `)`, `*`, `:`, `^`, `-`, `.`, `{`, `}`). Passing a raw user string
// containing any of these crashes the parser. We tokenize on non-word runs,
// wrap each token in phrase quotes (which turn it into a literal-word search
// that ignores FTS5 special chars), and OR-join.
//
// OR (not AND): AND-join requires EVERY query word to appear in a document, so
// a single descriptive word the user added that is absent from the stored text
// zeroes the whole query even when most tokens match. OR lets BM25 rank by how
// many / how rare the matched tokens are; the caller applies a relative score
// floor to drop common-word-only noise (see Store.Search).
//
// Returns "" when no usable tokens are extracted. Callers treat that as "empty
// query, no results" and send no SQL.
func buildFtsQuery(raw string) string {
	matches := ftsTokenRe.FindAllString(raw, -1)
	if len(matches) == 0 {
		return ""
	}

	quoted := make([]string, 0, len(matches))
	for _, tok := range matches {
		// Strip any embedded double quotes, then wrap the token as a phrase.
		stripped := removeQuotes(tok)
		if stripped == "" {
			continue
		}
		quoted = append(quoted, `"`+stripped+`"`)
	}
	if len(quoted) == 0 {
		return ""
	}

	out := quoted[0]
	for _, q := range quoted[1:] {
		out += " OR " + q
	}
	return out
}

// removeQuotes strips every double-quote character from s.
func removeQuotes(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r != '"' {
			out = append(out, r)
		}
	}
	return string(out)
}
