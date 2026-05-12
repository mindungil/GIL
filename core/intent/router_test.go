package intent

import (
	"context"
	"testing"
)

// The router is intentionally a stub now (see router.go header
// comment). These tests pin that contract:
//
//   - Classify always returns KindForward for non-empty input
//   - Slash dispatch is the caller's responsibility (the router
//     never sees slash-prefixed input)
//   - SessionContext is accepted for API stability but doesn't
//     change the result
//
// All natural-language verb detection (was: regex pattern table),
// length thresholds (was: isTooVague), and meta-question routing
// (was: interrogativeRE) live in the daemon's LLM-driven loop now,
// per design.md §2.6.

func TestRouter_AlwaysForwards_Greetings(t *testing.T) {
	r := NewRouter()
	for _, in := range []string{
		"안녕",
		"ㅎㅇ",
		"hi",
		"hello",
		"ok",
		"thanks",
		"asdf",
		"🎉",
	} {
		got := r.Classify(context.Background(), in, SessionContext{})
		if got.Kind != KindForward {
			t.Errorf("Classify(%q) must forward; got kind=%d", in, got.Kind)
		}
	}
}

func TestRouter_AlwaysForwards_Tasks(t *testing.T) {
	r := NewRouter()
	for _, in := range []string{
		"add a fibonacci function to math.go",
		"fix the bug in main.go where the writer leaks",
		"테스트 추가해줘 /tmp/scratch에",
		"refactor the auth middleware",
		"너 무슨 모델임?",
	} {
		got := r.Classify(context.Background(), in, SessionContext{})
		if got.Kind != KindForward {
			t.Errorf("Classify(%q) must forward; got kind=%d", in, got.Kind)
		}
	}
}

func TestRouter_AlwaysForwards_VerbWords(t *testing.T) {
	// "diff", "merge", "status" — historically client-side regex
	// would have classified these as verb invocations. Now they
	// forward; the daemon decides what they mean in context. (User
	// can always type `/diff`, `/merge`, `/status` to dispatch
	// explicitly via the slash escape hatch — but that's slash
	// handling in the caller, not router business.)
	r := NewRouter()
	for _, in := range []string{
		"diff",
		"merge",
		"status",
		"start",
		"show me the diff",
		"apply it",
		"what's the status",
	} {
		got := r.Classify(context.Background(), in, SessionContext{})
		if got.Kind != KindForward {
			t.Errorf("Classify(%q) must forward; verb regex tables removed", in)
		}
	}
}

func TestRouter_EmptyInputForwards(t *testing.T) {
	// Empty / whitespace input forwards (caller drops it before
	// reaching the daemon at the textinput layer).
	r := NewRouter()
	for _, in := range []string{"", "   ", "\n", "\t  \n"} {
		got := r.Classify(context.Background(), in, SessionContext{})
		if got.Kind != KindForward {
			t.Errorf("Classify(%q) must forward; got kind=%d", in, got.Kind)
		}
	}
}

func TestRouter_SessionContextIgnored(t *testing.T) {
	// SessionContext is on the API for stability but doesn't change
	// the classification — the daemon already has the context.
	r := NewRouter()
	idle := SessionContext{}
	active := SessionContext{
		ActiveSessionID: "01KQEPABCXYZ",
		Phase:           "interview",
		RecentSessions: []SessionRef{
			{ID: "01KQ", Slug: "first"},
			{ID: "02KQ", Slug: "second"},
		},
	}
	for _, ctx := range []SessionContext{idle, active} {
		got := r.Classify(context.Background(), "hi", ctx)
		if got.Kind != KindForward {
			t.Errorf("Classify with ctx=%+v must forward; got kind=%d", ctx, got.Kind)
		}
	}
}
