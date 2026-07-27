// This file implements shell-style argument tokenization for prompt templates
// (US-001, #331). A template invocation like
//
//	/component Button "click handler"
//
// arrives at the expander as the raw string `Button "click handler"`; the
// expander needs positional args ($1, $@, ...), so the string must be split into
// ["Button", "click handler"] honoring shell quoting rules: double and single
// quotes group a single argument, surrounding quotes are stripped, and internal
// whitespace is preserved.
//
// Rather than hand-roll a tokenizer, we reuse a mature shell-quoting library
// (github.com/kballard/go-shellquote), per the project's "复用而非自实现" rule.
package runtime

import "github.com/kballard/go-shellquote"

// SplitArgs tokenizes a raw argument string using shell-style quoting rules.
// Double quotes ("a b") and single quotes ('a b') each group one argument;
// surrounding quotes are stripped and internal whitespace is preserved. An empty
// (or all-whitespace) input yields an empty non-nil slice with no error. An
// unterminated quote yields an error, so the caller can fall back to treating
// the whole string as $ARGUMENTS rather than feeding a malformed arg list to the
// template engine.
func SplitArgs(s string) ([]string, error) {
	parts, err := shellquote.Split(s)
	if err != nil {
		return nil, err
	}
	if parts == nil {
		return []string{}, nil
	}
	return parts, nil
}
