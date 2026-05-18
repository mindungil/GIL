Implement a small regex matcher in Go in the current working directory.

API:
- `Match(pattern, input string) bool`

Supported syntax:
- Literal characters (letters, digits, etc.)
- `.` — matches any single character
- `*` — matches zero or more of the preceding element (Kleene star)
- `^` — anchors the match to the beginning of the input
- `$` — anchors the match to the end of the input
- Escape: `\.`, `\*`, `\\` etc. (a `\` followed by any character is a literal match of that character)

Semantics: `Match` returns true if the pattern matches any substring of the input (unless anchored). Empty pattern matches any input. Empty input matches `^$` and `.*` but not `a`.

Include `go test ./...` with at least 10 table-driven cases covering each feature:
- literal exact match
- literal partial match (no anchor)
- `.` matches one character
- `*` with preceding literal: `a*` matches `""`, `"a"`, `"aa"`, etc.
- `.*` matches anything
- `^foo` matches `"foobar"` but not `"xfoo"`
- `bar$` matches `"foobar"` but not `"barx"`
- `^foo$` exact match
- escape: `a\.b` matches `"a.b"` but not `"acb"`
- combined: `^h.llo$` matches `"hello"` and `"hallo"`

Initialize go.mod yourself. Done when `go test ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.
