package intent

import (
	"context"
	"testing"

	"github.com/mindungil/gil/core/provider"
)

// TestClassify_RegexFastPath asserts the heuristic layer catches the
// obvious shapes without any provider call. These are the inputs we expect
// to dominate real chat sessions, so the regex layer must be solid.
//
// Each case uses hasSessions=true so STATUS/RESUME aren't demoted to
// NEW_TASK by the count-aware short-circuit. The demotion path has its
// own test below.
func TestClassify_RegexFastPath(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantKind  Kind
		wantGoal  string
		wantWS    string
		wantSesID string
	}{
		{name: "empty", msg: "", wantKind: KindUnknown},
		{name: "blank-spaces", msg: "   ", wantKind: KindUnknown},

		{name: "status-bare", msg: "status", wantKind: KindStatus},
		{name: "status-show", msg: "show me sessions", wantKind: KindStatus},
		{name: "status-list", msg: "list sessions", wantKind: KindStatus},
		{name: "status-running", msg: "what's running?", wantKind: KindStatus},

		{name: "resume-continue", msg: "continue", wantKind: KindResume},
		{name: "resume-yesterday", msg: "continue yesterday's task", wantKind: KindResume},
		{name: "resume-pickup", msg: "pick up the OAuth work", wantKind: KindResume},

		{name: "help-bare", msg: "help", wantKind: KindHelp},
		{name: "help-what-can", msg: "what can you do?", wantKind: KindHelp},

		{name: "explain-gil", msg: "what is gil?", wantKind: KindExplain},
		{name: "explain-interview", msg: "explain what an interview means", wantKind: KindExplain},

		{name: "new-task-add", msg: "I want to add dark mode to my app", wantKind: KindNewTask, wantGoal: "I want to add dark mode to my app"},
		{name: "new-task-with-path", msg: "Add hello.txt at ~/dogfood", wantKind: KindNewTask, wantGoal: "Add hello.txt at ~/dogfood", wantWS: "~/dogfood"},
		{name: "new-task-abs-path", msg: "fix the build at /home/me/proj", wantKind: KindNewTask, wantGoal: "fix the build at /home/me/proj", wantWS: "/home/me/proj"},
		{name: "new-task-trailing-comma", msg: "tweak ~/foo, please", wantKind: KindNewTask, wantWS: "~/foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it, err := Classify(context.Background(), nil, "", tc.msg, true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if it.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", it.Kind, tc.wantKind)
			}
			if tc.wantGoal != "" && it.GoalText != tc.wantGoal {
				t.Errorf("goal = %q, want %q", it.GoalText, tc.wantGoal)
			}
			if tc.wantWS != "" && it.Workspace != tc.wantWS {
				t.Errorf("workspace = %q, want %q", it.Workspace, tc.wantWS)
			}
			if tc.wantSesID != "" && it.SessionID != tc.wantSesID {
				t.Errorf("session_id = %q, want %q", it.SessionID, tc.wantSesID)
			}
		})
	}
}

// TestClassify_NoSessionsDemotion checks that STATUS/RESUME re-route to
// NEW_TASK when the user has no existing sessions. The scenario: a new
// user types "show me what's running" before they've started anything;
// we should treat their words as their first task description rather
// than an empty list dead-end.
func TestClassify_NoSessionsDemotion(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"status", "status"},
		{"resume", "continue yesterday"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it, err := Classify(context.Background(), nil, "", tc.msg, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if it.Kind != KindNewTask {
				t.Errorf("expected demotion to NEW_TASK, got %q", it.Kind)
			}
			if it.GoalText == "" {
				t.Error("expected goal text to carry the user's original message")
			}
		})
	}
}

// TestClassify_LLMFallback validates the LLM path. We use the existing
// Mock provider to script a JSON response, so this test costs nothing
// and stays deterministic.
//
// The first case tests the happy path (clean JSON). The second tests
// fence-stripping (model wrapped the JSON in ```json fences). The third
// tests the parse-failure fallback (garbled text → NEW_TASK with the
// original message preserved).
func TestClassify_LLMFallback(t *testing.T) {
	cases := []struct {
		name      string
		response  string
		userMsg   string
		wantKind  Kind
		wantGoal  string
	}{
		{
			name:     "clean-json",
			response: `{"kind":"NEW_TASK","confidence":0.9,"goal":"refactor the parser","workspace":"~/work","session_id":""}`,
			userMsg:  "I'd like you to refactor the parser somewhere around ~/work",
			wantKind: KindNewTask,
			wantGoal: "refactor the parser",
		},
		{
			name: "fenced-json",
			response: "```json\n" +
				`{"kind":"RESUME","confidence":0.7,"goal":"","workspace":"","session_id":"01HXY"}` +
				"\n```",
			userMsg:  "let's keep going on 01HXY",
			wantKind: KindResume,
		},
		{
			name:     "garbled",
			response: `Sure! I think the user wants to ...`,
			userMsg:  "I'd like a code review",
			wantKind: KindNewTask, // fallback path
			wantGoal: "I'd like a code review",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Use a phrasing that the regex layer won't catch so we
			// actually exercise the LLM path.
			mock := provider.NewMock([]string{tc.response})
			it, err := Classify(context.Background(), mock, "haiku", tc.userMsg, true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if it.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", it.Kind, tc.wantKind)
			}
			if tc.wantGoal != "" && it.GoalText != tc.wantGoal {
				t.Errorf("goal = %q, want %q", it.GoalText, tc.wantGoal)
			}
		})
	}
}

// TestClassify_LLMUnavailable is the offline scenario: the user has no
// configured provider, so prov is nil. Ambiguous input must still
// produce a NEW_TASK so the chat REPL has something to work with.
func TestClassify_LLMUnavailable(t *testing.T) {
	it, err := Classify(context.Background(), nil, "", "do the thing", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if it.Kind != KindNewTask {
		t.Errorf("kind = %q, want NEW_TASK", it.Kind)
	}
	if it.GoalText != "do the thing" {
		t.Errorf("goal = %q, want passthrough", it.GoalText)
	}
}

// TestExtractWorkspace tests the path-finding helper independently.
// Goal: catch obvious accidental matches (like "use update" thinking
// "update" looks like a path).
func TestExtractWorkspace(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"work in ~/myapp", "~/myapp"},
		{"build at /home/me/proj", "/home/me/proj"},
		{"the relative ./bin matters", "bin"}, // filepath.Clean strips ./
		{"no path here", ""},
		{"mention myapp without slash", ""},
		{"trailing punct ~/foo,", "~/foo"},
	}
	for _, tc := range cases {
		got := extractWorkspace(tc.msg)
		if got != tc.want {
			t.Errorf("extractWorkspace(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

// TestExtractSessionID validates the ULID-prefix detection.
func TestExtractSessionID(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"continue 01HXY1", "01HXY1"},
		{"resume session abcdef", "abcdef"},
		{"continue", ""},  // no ID
		{"a", ""},         // too short
		// "going" contains "o" (excluded from Crockford ULID) so it
		// won't match; "OAuth" has "O" excluded; "bug" is too short;
		// "fix" too short. The whole string yields no match.
		{"keep going on the OAuth bug fix", ""},
		{"continue session 01HXYZAB1234", "01HXYZAB1234"},
	}
	for _, tc := range cases {
		got := extractSessionID(tc.msg)
		if got != tc.want {
			t.Errorf("extractSessionID(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}
