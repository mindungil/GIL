package intent

import (
	"context"
	"testing"
)

func TestRouter_BareVerbs(t *testing.T) {
	r := NewRouter()
	cases := []struct {
		in   string
		want Verb
	}{
		{"sessions", VerbSessions},
		{"list", VerbSessions},
		{"new", VerbNew},
		{"spec", VerbSpec},
		{"status", VerbStatus},
		{"diff", VerbDiff},
		{"merge", VerbMerge},
		{"apply", VerbMerge},
		{"run", VerbRun},
		{"start", VerbRun},
		{"quit", VerbQuit},
		{"exit", VerbQuit},
		{"help", VerbHelp},
		{"  STATUS  ", VerbStatus},
	}
	for _, c := range cases {
		got := r.Classify(context.Background(), c.in, SessionContext{})
		if got.Kind != KindVerb || got.Verb != c.want {
			t.Errorf("Classify(%q) = {kind=%d verb=%q}, want verb=%q", c.in, got.Kind, got.Verb, c.want)
		}
	}
}

func TestRouter_PhrasePatterns(t *testing.T) {
	r := NewRouter()
	cases := []struct {
		in   string
		want Verb
	}{
		{"show me the diff", VerbDiff},
		{"can I see the changes?", VerbDiff},
		{"preview the patch", VerbDiff},
		{"apply it", VerbMerge},
		{"merge it", VerbMerge},
		{"save it", VerbMerge},
		{"approve the diff", VerbMerge},
		{"show me the spec", VerbSpec},
		{"what's the spec", VerbSpec},
		{"how's it going?", VerbStatus},
		{"what's running?", VerbStatus},
		{"show progress", VerbStatus},
		{"list my sessions", VerbSessions},
		{"what do I have", VerbSessions},
		{"start a new session", VerbNew},
		{"new task", VerbNew},
		{"start over", VerbNew},
		{"start the run", VerbRun},
		{"kick off", VerbRun},
		{"run it", VerbRun},
		{"let's run", VerbRun},
		{"what can you do", VerbHelp},
		{"goodbye", VerbQuit},
		{"see ya", VerbQuit},
	}
	for _, c := range cases {
		got := r.Classify(context.Background(), c.in, SessionContext{})
		if got.Kind != KindVerb || got.Verb != c.want {
			t.Errorf("Classify(%q) = {kind=%d verb=%q rationale=%q}, want verb=%q",
				c.in, got.Kind, got.Verb, got.Rationale, c.want)
		}
	}
}

func TestRouter_Forward(t *testing.T) {
	r := NewRouter()
	conversational := []string{
		"add a dark mode toggle to the settings page",
		"yes, use postgres",
		"the OAuth flow should redirect to /callback",
		"actually let me think about that",
		"why does the test fail when I run go test ./pkg/foo?",
		"help me debug this OAuth flow",   // not the help verb
		"can you run the tests for me",    // not the run verb (no session-action signal)
		"",
	}
	for _, in := range conversational {
		got := r.Classify(context.Background(), in, SessionContext{})
		if got.Kind != KindForward {
			t.Errorf("Classify(%q) should forward, got kind=%d verb=%q rationale=%q",
				in, got.Kind, got.Verb, got.Rationale)
		}
	}
}

func TestRouter_SwitchByID(t *testing.T) {
	r := NewRouter()
	got := r.Classify(context.Background(), "switch to 01KQEPABCXYZ", SessionContext{})
	if got.Kind != KindVerb || got.Verb != VerbSwitch {
		t.Fatalf("expected switch verb, got kind=%d verb=%q", got.Kind, got.Verb)
	}
	if got.Args["target"] != "01KQEPABCXYZ" {
		t.Errorf("expected target=01KQEPABCXYZ, got %q", got.Args["target"])
	}
}

func TestRouter_SwitchBySlug(t *testing.T) {
	r := NewRouter()
	ctx := SessionContext{
		RecentSessions: []SessionRef{
			{ID: "01KQEP000001", Slug: "add dark mode"},
			{ID: "01KQEP000002", Slug: "fix oauth"},
		},
	}
	got := r.Classify(context.Background(), "switch to the dark mode one", ctx)
	if got.Kind != KindVerb || got.Verb != VerbSwitch {
		t.Fatalf("expected switch verb, got kind=%d verb=%q clarification=%q",
			got.Kind, got.Verb, got.Clarification)
	}
	if got.Args["target"] != "01KQEP000001" {
		t.Errorf("expected target=01KQEP000001, got %q", got.Args["target"])
	}
}

func TestRouter_SwitchAmbiguous_NoMatch(t *testing.T) {
	r := NewRouter()
	ctx := SessionContext{
		RecentSessions: []SessionRef{
			{ID: "01KQEP000001", Slug: "add dark mode"},
		},
	}
	got := r.Classify(context.Background(), "switch to the postgres one", ctx)
	if got.Kind != KindAmbiguous {
		t.Errorf("expected ambiguous, got kind=%d", got.Kind)
	}
	if got.Clarification == "" {
		t.Error("ambiguous classification must include clarification")
	}
}

func TestRouter_SwitchBare_AsksForTarget(t *testing.T) {
	r := NewRouter()
	got := r.Classify(context.Background(), "switch", SessionContext{
		RecentSessions: []SessionRef{{ID: "01KQEP000001", Slug: "add dark mode"}},
	})
	if got.Kind != KindAmbiguous {
		t.Errorf("bare 'switch' should ask for target, got kind=%d verb=%q", got.Kind, got.Verb)
	}
}

func TestRouter_SwitchDoesNotMatchVerbAsSlug(t *testing.T) {
	// Regression: a session happening to be slugged "switch" must not
	// match the "switch" keyword in every switch prompt.
	r := NewRouter()
	ctx := SessionContext{
		RecentSessions: []SessionRef{
			{ID: "01KQEP000001", Slug: "switch"},
			{ID: "01KQEP000002", Slug: "fix oauth"},
		},
	}
	got := r.Classify(context.Background(), "switch to oauth", ctx)
	if got.Kind != KindVerb || got.Args["target"] != "01KQEP000002" {
		t.Errorf("expected switch → oauth (01KQEP000002), got kind=%d target=%q rationale=%q",
			got.Kind, got.Args["target"], got.Rationale)
	}
}

func TestRouter_SwitchAmbiguous_MultipleMatches(t *testing.T) {
	r := NewRouter()
	ctx := SessionContext{
		RecentSessions: []SessionRef{
			{ID: "01KQEP000001", Slug: "dark mode toggle"},
			{ID: "01KQEP000002", Slug: "dark theme refactor"},
		},
	}
	got := r.Classify(context.Background(), "switch to the dark one", ctx)
	if got.Kind != KindAmbiguous {
		t.Errorf("expected ambiguous, got kind=%d verb=%q", got.Kind, got.Verb)
	}
}

func TestRouter_RationaleAlwaysPresent(t *testing.T) {
	r := NewRouter()
	got := r.Classify(context.Background(), "show me the spec", SessionContext{})
	if got.Rationale == "" {
		t.Error("verb classifications must have a non-empty rationale")
	}
}
