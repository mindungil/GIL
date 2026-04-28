package intent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
)

// TestConversation_GreetingNoToolCall is the headline regression test
// for Phase 24 redesign: a bare greeting must NOT produce any tool
// call. The earlier classifier-based dispatcher would (eventually) fall
// through to NEW_TASK on any 12+ character input that didn't match
// the greeting regex; the new LLM-driven path leaves "is this a task?"
// to the model itself.
func TestConversation_GreetingNoToolCall(t *testing.T) {
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "Standing by. What would you like to work on?", StopReason: "end_turn"},
	})
	c := NewConversation()

	turn, err := c.Send(context.Background(), prov, "haiku", "안녕")
	require.NoError(t, err)
	require.Empty(t, turn.ToolCalls, "greeting must not trigger any tool call")
	require.Contains(t, turn.AssistantText, "Standing by")
	require.Len(t, c.History, 2, "user + assistant turn recorded")
}

// TestConversation_ProtestNoToolCall replays the exact bug from the
// user's session: "안녕" → "뭐??" → "아니 안녕ㄹ이라니까". The first two
// turns are normal greetings; the third is a protest ("no, I told you
// it's hello"). NONE of these may produce a start_interview call.
func TestConversation_ProtestNoToolCall(t *testing.T) {
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "Standing by. Mission?", StopReason: "end_turn"},
		{Text: "Need more detail. What task?", StopReason: "end_turn"},
		{Text: "Apologies — that was a greeting. Standing by for your task.", StopReason: "end_turn"},
	})
	c := NewConversation()
	for _, msg := range []string{"안녕", "뭐??", "아니 안녕ㄹ이라니까"} {
		turn, err := c.Send(context.Background(), prov, "haiku", msg)
		require.NoError(t, err, "msg %q", msg)
		require.Empty(t, turn.ToolCalls, "msg %q must not trigger tool call", msg)
	}
	require.Len(t, c.History, 6, "three user + three assistant turns recorded")
}

// TestConversation_TaskTriggersStartInterview verifies the happy path:
// a clear task description with a workspace yields a start_interview
// tool call with goal+workspace populated.
func TestConversation_TaskTriggersStartInterview(t *testing.T) {
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{
			Text: "Briefing your task.",
			ToolCalls: []provider.ToolCall{
				{
					ID:    "tu_1",
					Name:  ToolStartInterview,
					Input: json.RawMessage(`{"goal":"fix the lint warnings in handlers/auth.go","workspace":"/home/me/proj"}`),
				},
			},
			StopReason: "tool_use",
		},
	})
	c := NewConversation()

	turn, err := c.Send(context.Background(), prov, "haiku", "fix the lint warnings in handlers/auth.go at /home/me/proj")
	require.NoError(t, err)
	require.Len(t, turn.ToolCalls, 1)
	require.Equal(t, ToolStartInterview, turn.ToolCalls[0].Name)

	args, err := ParseStartInterview(turn.ToolCalls[0])
	require.NoError(t, err)
	require.Equal(t, "fix the lint warnings in handlers/auth.go", args.Goal)
	require.Equal(t, "/home/me/proj", args.Workspace)
}

// TestConversation_StatusToolCall — "show me my sessions" must invoke
// show_status (a no-arg tool).
func TestConversation_StatusToolCall(t *testing.T) {
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{
			Text: "",
			ToolCalls: []provider.ToolCall{
				{ID: "tu_1", Name: ToolShowStatus, Input: json.RawMessage(`{}`)},
			},
			StopReason: "tool_use",
		},
	})
	c := NewConversation()

	turn, err := c.Send(context.Background(), prov, "haiku", "show me my sessions")
	require.NoError(t, err)
	require.Len(t, turn.ToolCalls, 1)
	require.Equal(t, ToolShowStatus, turn.ToolCalls[0].Name)
}

// TestConversation_ResumeToolCall — "continue yesterday's task" yields
// resume_session with the raw query the user gave.
func TestConversation_ResumeToolCall(t *testing.T) {
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{
			Text: "",
			ToolCalls: []provider.ToolCall{
				{ID: "tu_1", Name: ToolResumeSession, Input: json.RawMessage(`{"query":"yesterday's OAuth task"}`)},
			},
			StopReason: "tool_use",
		},
	})
	c := NewConversation()

	turn, err := c.Send(context.Background(), prov, "haiku", "continue yesterday's OAuth task")
	require.NoError(t, err)
	require.Len(t, turn.ToolCalls, 1)
	require.Equal(t, ToolResumeSession, turn.ToolCalls[0].Name)

	args, err := ParseResumeSession(turn.ToolCalls[0])
	require.NoError(t, err)
	require.Equal(t, "yesterday's OAuth task", args.Query)
}

// TestConversation_ExplainToolCall — "what's an interview?" yields
// explain with the topic.
func TestConversation_ExplainToolCall(t *testing.T) {
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{
			Text: "",
			ToolCalls: []provider.ToolCall{
				{ID: "tu_1", Name: ToolExplain, Input: json.RawMessage(`{"topic":"what's an interview"}`)},
			},
			StopReason: "tool_use",
		},
	})
	c := NewConversation()

	turn, err := c.Send(context.Background(), prov, "haiku", "what's an interview?")
	require.NoError(t, err)
	require.Len(t, turn.ToolCalls, 1)

	args, err := ParseExplain(turn.ToolCalls[0])
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(args.Topic), "interview")
}

// TestConversation_ProviderError — when the LLM call fails, Send
// returns the error AND rolls back the user turn so a retry isn't
// double-counted in History.
func TestConversation_ProviderError(t *testing.T) {
	prov := &errorProv{err: errors.New("network down")}
	c := NewConversation()

	_, err := c.Send(context.Background(), prov, "haiku", "hi")
	require.Error(t, err)
	require.Empty(t, c.History, "user turn rolled back on provider error")

	// A retry with a working provider must succeed and only record
	// the single retry turn (not the original + retry).
	prov2 := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "Standing by.", StopReason: "end_turn"},
	})
	turn, err := c.Send(context.Background(), prov2, "haiku", "hi")
	require.NoError(t, err)
	require.Empty(t, turn.ToolCalls)
	require.Len(t, c.History, 2, "exactly one user + one assistant turn after retry")
}

// TestConversation_NilProvider — Send with a nil provider returns an
// error instead of panicking. The chat REPL is expected to gate this
// case before calling Send (offline mode), but defensive coding here
// catches a class of mistakes.
func TestConversation_NilProvider(t *testing.T) {
	c := NewConversation()
	_, err := c.Send(context.Background(), nil, "haiku", "hi")
	require.Error(t, err)
	require.Empty(t, c.History, "no turn recorded on nil-provider error")
}

// TestConversation_EmptyMessage — empty user input is a no-op (no
// LLM call, no history mutation).
func TestConversation_EmptyMessage(t *testing.T) {
	prov := provider.NewMockToolProvider(nil)
	c := NewConversation()
	turn, err := c.Send(context.Background(), prov, "haiku", "   ")
	require.NoError(t, err)
	require.Empty(t, turn.ToolCalls)
	require.Empty(t, turn.AssistantText)
	require.Empty(t, c.History)
}

// TestConversation_HistoryBudget — once history exceeds the budget we
// drop oldest pairs. The surviving prefix must still start on a user
// turn (Anthropic API requirement).
func TestConversation_HistoryBudget(t *testing.T) {
	// Script enough turns to overflow historyBudget (=10).
	turns := make([]provider.MockTurn, historyBudget+5)
	for i := range turns {
		turns[i] = provider.MockTurn{Text: "ok", StopReason: "end_turn"}
	}
	prov := provider.NewMockToolProvider(turns)
	c := NewConversation()

	for i := 0; i < len(turns); i++ {
		_, err := c.Send(context.Background(), prov, "haiku", "msg")
		require.NoError(t, err)
	}

	require.LessOrEqual(t, len(c.History), historyBudget+1,
		"history must be trimmed to roughly the budget (we leave the most recent pair intact)")
	require.Equal(t, provider.RoleUser, c.History[0].Role,
		"surviving prefix must start with a user turn")
}

// TestDefaultTools sanity-checks the four tool definitions. Schemas
// must be valid JSON; names must match the package constants.
func TestDefaultTools(t *testing.T) {
	tools := DefaultTools()
	require.Len(t, tools, 4)

	wantNames := map[string]bool{
		ToolStartInterview: false,
		ToolShowStatus:     false,
		ToolResumeSession:  false,
		ToolExplain:        false,
	}
	for _, td := range tools {
		_, present := wantNames[td.Name]
		require.True(t, present, "unexpected tool name %q", td.Name)
		wantNames[td.Name] = true

		// Schema must be valid JSON.
		var probe interface{}
		require.NoErrorf(t, json.Unmarshal(td.Schema, &probe), "tool %q has invalid JSON schema", td.Name)
		require.NotEmpty(t, td.Description, "tool %q missing description", td.Name)
	}
	for name, seen := range wantNames {
		require.Truef(t, seen, "tool %q missing from DefaultTools()", name)
	}
}

// TestParseStartInterview_BadJSON — malformed input from the LLM does
// not crash; we surface an error so the REPL can ask again.
func TestParseStartInterview_BadJSON(t *testing.T) {
	_, err := ParseStartInterview(provider.ToolCall{
		Name:  ToolStartInterview,
		Input: json.RawMessage(`not json`),
	})
	require.Error(t, err)
}

// errorProv is a Provider that always errors. Used to test the rollback
// path in Send without dragging in network mocks.
type errorProv struct{ err error }

func (e *errorProv) Name() string { return "errorprov" }
func (e *errorProv) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	return provider.Response{}, e.err
}
