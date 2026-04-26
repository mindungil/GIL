// Package permission implements allow/ask/deny rule evaluation for tool calls.
// Inspired by OpenCode's permission/evaluate.ts (last-matching wins) and its
// util/wildcard.ts (regex-based glob with `*`/`?` and the "ls *" trailing-
// optional quirk).
package permission

import (
	"regexp"
	"strings"
	"sync"
)

// MatchWildcard reports whether str matches the wildcard pattern.
// Pattern syntax:
//
//	*       — zero or more of anything
//	?       — any single character
//
// Trailing " *" (space then star) is treated as optional, so "ls *" matches
// both "ls" and "ls -la" (this matches OpenCode's behavior).
//
// All path separators are normalized to "/" for cross-platform consistency
// (so a Windows-style "a\b" is matched against pattern "a/b").
//
// Both str and pattern must match the WHOLE input (anchored).
//
// Lifted from opencode/packages/opencode/src/util/wildcard.ts match().
func MatchWildcard(str, pattern string) bool {
	if str != "" {
		str = strings.ReplaceAll(str, `\`, "/")
	}
	if pattern != "" {
		pattern = strings.ReplaceAll(pattern, `\`, "/")
	}
	re := compileWildcard(pattern)
	return re.MatchString(str)
}

var (
	wildcardCache    sync.Map // pattern → *regexp.Regexp
	wildcardEscapeRE = regexp.MustCompile(`[.+^${}()|\[\]\\]`)
)

func compileWildcard(pattern string) *regexp.Regexp {
	if cached, ok := wildcardCache.Load(pattern); ok {
		return cached.(*regexp.Regexp)
	}
	// 1) Escape regex special chars EXCEPT * and ?
	escaped := wildcardEscapeRE.ReplaceAllString(pattern, `\$0`)
	// 2) * → .*, ? → .
	escaped = strings.ReplaceAll(escaped, "*", ".*")
	escaped = strings.ReplaceAll(escaped, "?", ".")
	// 3) Trailing " .*" → "( .*)?"  (OpenCode quirk: pattern "ls *" matches "ls" too)
	if strings.HasSuffix(escaped, " .*") {
		escaped = escaped[:len(escaped)-3] + "( .*)?"
	}
	// 4) Anchor and use s flag (. matches newline) for Go regex's (?s) syntax
	re := regexp.MustCompile(`(?s)^` + escaped + `$`)
	wildcardCache.Store(pattern, re)
	return re
}
