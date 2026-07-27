// This file implements the prompt-template expansion engine (US-002, #332): it
// turns a template body plus tokenized invocation args into the final prompt
// text, supporting pi's positional/default/slice syntax.
//
// Supported placeholders (对标 https://pi.dev/docs/latest/prompt-templates):
//   - $1, $2, ... $N      : Nth positional arg (1-indexed; out-of-range -> "")
//   - $@, $ARGUMENTS      : all args joined by a single space
//   - ${1:-default}       : arg 1 when present and non-empty, else `default`
//   - ${@:-default}, ${ARGUMENTS:-default} : all args when non-empty, else default
//   - ${@:N}              : args from the Nth onward (1-indexed), joined
//   - ${@:N:L}            : L args starting at N, joined
//
// A single left-to-right pass expands both braced ${...} and bare $N/$@ forms.
// Because the pass consumes a ${...} as one unit, a bare $1 never matches inside
// ${1:-...}, and a default literal containing $ is not re-expanded (the
// substituted text is appended verbatim and the scan advances past it).
package runtime

import (
	"strconv"
	"strings"
)

// ExpandTemplate expands template against the tokenized args. A template with no
// placeholder preserves ParseUserCommand's behavior: with no args it is returned
// verbatim, with args they are appended after a blank line (joined by spaces).
func ExpandTemplate(template string, args []string) string {
	if !hasPlaceholder(template) {
		if len(args) == 0 {
			return template
		}
		return template + "\n\n" + strings.Join(args, " ")
	}
	var b strings.Builder
	i := 0
	n := len(template)
	for i < n {
		c := template[i]
		if c == '$' && i+1 < n {
			next := template[i+1]
			if next == '{' {
				end := strings.IndexByte(template[i+2:], '}')
				if end < 0 {
					// No closing brace: emit the '$' literally and continue.
					b.WriteByte(c)
					i++
					continue
				}
				inner := template[i+2 : i+2+end]
				b.WriteString(expandBraced(inner, args))
				i += 2 + end + 1
				continue
			}
			if next == '@' {
				b.WriteString(strings.Join(args, " "))
				i += 2
				continue
			}
			if isDigitByte(next) {
				j := i + 1
				for j < n && isDigitByte(template[j]) {
					j++
				}
				idx, _ := strconv.Atoi(template[i+1 : j])
				if idx >= 1 && idx <= len(args) {
					b.WriteString(args[idx-1])
				}
				i = j
				continue
			}
			if strings.HasPrefix(template[i+1:], "ARGUMENTS") {
				b.WriteString(strings.Join(args, " "))
				i += 1 + len("ARGUMENTS")
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// hasPlaceholder reports whether s contains any template placeholder: ${...},
// $@, $<digits>, or $ARGUMENTS. A bare '$' not followed by one of these is not a
// placeholder (emitted literally), so a template like "price $5" still counts.
func hasPlaceholder(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if i+1 >= len(s) {
			return false
		}
		next := s[i+1]
		if next == '{' || next == '@' || isDigitByte(next) {
			return true
		}
		if strings.HasPrefix(s[i+1:], "ARGUMENTS") {
			return true
		}
	}
	return false
}

// expandBraced expands the content inside ${...} (without the surrounding
// braces). It dispatches on the three forms: name:-default, @:N[:L], or plain.
func expandBraced(inner string, args []string) string {
	// default form: name:-default
	if idx := strings.Index(inner, ":-"); idx >= 0 {
		return expandDefaulted(inner[:idx], inner[idx+2:], args)
	}
	// slice form: @:N or @:N:L
	if idx := strings.Index(inner, ":"); idx >= 0 {
		if name := inner[:idx]; name != "@" && name != "ARGUMENTS" {
			return "" // slicing only applies to all-args
		}
		return expandSlice(inner[idx+1:], args)
	}
	// plain: N, @, or ARGUMENTS
	return expandPlain(inner, args)
}

// expandPlain expands a bare braced name (no :- or :): a positional index, @,
// or ARGUMENTS. An unknown name (e.g. ${foo}) expands to "".
func expandPlain(name string, args []string) string {
	if name == "@" || name == "ARGUMENTS" {
		return strings.Join(args, " ")
	}
	if idx, err := strconv.Atoi(name); err == nil {
		if idx >= 1 && idx <= len(args) {
			return args[idx-1]
		}
		return ""
	}
	return ""
}

// expandDefaulted expands name:-default: the named positional (when in range and
// non-empty) or all-args (when the join is non-empty), otherwise the literal
// default. The default is not re-expanded.
func expandDefaulted(name, def string, args []string) string {
	if name == "@" || name == "ARGUMENTS" {
		if joined := strings.Join(args, " "); joined != "" {
			return joined
		}
		return def
	}
	if idx, err := strconv.Atoi(name); err == nil {
		if idx >= 1 && idx <= len(args) && args[idx-1] != "" {
			return args[idx-1]
		}
		return def
	}
	return def
}

// expandSlice expands the N (or N:L) part of ${@:N} / ${@:N:L}: args from the Nth
// onward (1-indexed), optionally limited to L, joined by spaces. N<1 or beyond
// the arg list yields "".
func expandSlice(rest string, args []string) string {
	parts := strings.SplitN(rest, ":", 2)
	start, _ := strconv.Atoi(parts[0])
	if start < 1 {
		return ""
	}
	begin := start - 1
	if begin >= len(args) {
		return ""
	}
	end := len(args)
	if len(parts) == 2 {
		if l, err := strconv.Atoi(parts[1]); err == nil && l >= 0 {
			end = begin + l
			if end > len(args) {
				end = len(args)
			}
		}
	}
	return strings.Join(args[begin:end], " ")
}

func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }
