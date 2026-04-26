package permission

import "testing"

func TestMatchWildcard_Exact(t *testing.T) {
	if !MatchWildcard("bash", "bash") {
		t.Fatal("expected match")
	}
	if MatchWildcard("bash", "ls") {
		t.Fatal("expected no match")
	}
}

func TestMatchWildcard_Star(t *testing.T) {
	if !MatchWildcard("anything", "*") {
		t.Fatal("expected match")
	}
	if !MatchWildcard("foo/bar/baz", "*") {
		t.Fatal("expected match")
	}
	if !MatchWildcard("rm -rf /", "rm *") {
		t.Fatal("expected rm * to match rm -rf /")
	}
	if !MatchWildcard("rm -rf /", "rm -rf *") {
		t.Fatal("expected")
	}
}

func TestMatchWildcard_TrailingSpaceStarOptional(t *testing.T) {
	// OpenCode quirk: "ls *" should match "ls" AND "ls -la"
	if !MatchWildcard("ls", "ls *") {
		t.Fatal("expected ls to match 'ls *'")
	}
	if !MatchWildcard("ls -la", "ls *") {
		t.Fatal("expected ls -la to match 'ls *'")
	}
	// But "ls" should NOT match "lsX *" (different prefix)
	if MatchWildcard("ls", "lsX *") {
		t.Fatal("expected no match")
	}
}

func TestMatchWildcard_QuestionMark(t *testing.T) {
	if !MatchWildcard("cat", "ca?") {
		t.Fatal("expected match")
	}
	if MatchWildcard("cats", "ca?") {
		t.Fatal("expected no match (anchored)")
	}
}

func TestMatchWildcard_PathSeparatorNormalize(t *testing.T) {
	if !MatchWildcard(`a\b\c`, "a/b/*") {
		t.Fatal("expected backslash to normalize")
	}
}

func TestMatchWildcard_RegexSpecialsEscaped(t *testing.T) {
	// Pattern with literal . should not match arbitrary char
	if MatchWildcard("ax", "a.") {
		t.Fatal("'.' should be literal in pattern")
	}
	if !MatchWildcard("a.", "a.") {
		t.Fatal("expected exact match")
	}
}

func TestMatchWildcard_EmptyPattern(t *testing.T) {
	if !MatchWildcard("", "") {
		t.Fatal("empty matches empty")
	}
	if MatchWildcard("anything", "") {
		t.Fatal("empty pattern shouldn't match nonempty")
	}
}

func TestMatchWildcard_CachedSafe(t *testing.T) {
	// Just exercise the cache twice; verify no panic and results consistent.
	r1 := MatchWildcard("foo", "f*")
	r2 := MatchWildcard("foo", "f*")
	if !r1 || !r2 {
		t.Fatal("expected matches")
	}
}
