// conversation.go — LLM-driven multi-turn chat dispatcher.
//
// Phase 24 redesign. The earlier intake surface ran every user message
// through a regex/JSON classifier (classifier.go) and switch-routed on the
// returned Kind. That worked for clean wordings but had a structural bug:
// any input that fell through the regex AND the LLM JSON path landed on
// NEW_TASK with low confidence, which the chat REPL committed to. Real
// users protest ("아니 안녕ㄹ이라니까" — "no, I said HELLO"), and a 12+
// character protest is indistinguishable from a 12+ character task to a
// regex + length gate. The protest got committed as a goal. UX-wise: a
// disaster.
//
// The reference harnesses we studied (Cline, Codex, aider, opencode) all
// solve this the same way: keep a conversation history, send each user
// turn to the LLM with tool definitions, dispatch on tool_use blocks
// (not on text shape). Greetings stay greetings because the model doesn't
// emit a tool call for them; protests stay protests because the model
// understands "no, I meant X" in context. The harness commits to a real
// action only when the model invokes a tool.
//
// This file holds the conversation state + the per-turn dispatch loop.
// It is intentionally provider-agnostic: a Conversation only depends on
// core/provider, so chat tests can drive it with MockToolProvider.
package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mindungil/gil/core/provider"
)

// IntakeSystemPrompt is the system prompt for the chat intake LLM. It is
// kept tight (~300 tokens) so the round-trip stays under a cent on the
// smallest models (haiku, gpt-4o-mini, qwen-7b). The wording is
// deliberately mission-control — gil's voice is precision instrument,
// not chat companion.
//
// Exposed so tests and ops can inspect the exact contract gil ships with;
// changing it should always come with a CHANGELOG note.
const IntakeSystemPrompt = `You are the intake agent for gil, an autonomous coding harness.
Your job: have a brief, helpful conversation with the user to understand
what coding task they want gil to run, then commit by calling
start_interview when you have enough.

Tools available:
- start_interview(goal: string, workspace: string|null): begin a new
  task. Only call when the user has clearly described work to do AND
  given a workspace path (or you've asked and accepted no path).
- show_status(): list the user's existing sessions.
- resume_session(query: string): continue a previous session. query is
  a hint (date, name, ID prefix, or topic).
- explain(topic: string): answer a meta question about gil itself
  (e.g. "what is an interview?", "how does this work?").

Guidelines:
- Be brief. One short paragraph max per response. No emoji.
- For greetings ("hi", "안녕"), respond briefly and ask what they
  want to work on. DO NOT call start_interview.
- For clarifications/protests ("no, I meant X", "아니 안녕이라니까"),
  apologize briefly and re-ask. DO NOT call start_interview.
- For unclear input, ask ONE clarifying question. Don't lecture.
- For meta questions, call explain.
- For "show sessions" / "what's running", call show_status.
- For "continue", "resume", call resume_session.
- ONLY call start_interview when the user has stated a concrete task
  with at least a verb (add, fix, refactor) and ideally a target.

Tone: precise, calm, professional. Mission-control, not chatbot.
Never say "I'd be happy to" or use exclamation points.`

// Tool name constants — exposed so the chat dispatcher can switch on
// them without re-typing the strings (and so a typo gets caught at
// compile time, not at runtime when an LLM invokes the wrong name).
const (
	ToolStartInterview = "start_interview"
	ToolShowStatus     = "show_status"
	ToolResumeSession  = "resume_session"
	ToolExplain        = "explain"
)

// DefaultTools returns the four tool definitions the intake LLM is
// taught to call. Returned as a fresh slice each time so callers can
// mutate (e.g. drop resume_session when the user has zero sessions)
// without polluting other call sites.
//
// Schemas use JSON Schema draft-07 (the dialect Anthropic accepts) and
// only the "properties" + "required" fields the provider adapter needs.
// We keep them minimal — the system prompt does the heavy lifting on
// "when to call".
func DefaultTools() []provider.ToolDef {
	return []provider.ToolDef{
		{
			Name: ToolStartInterview,
			Description: "Begin a new coding task interview. Call ONLY when the user has stated " +
				"concrete work to do (verb + target). Pass the user's goal as a one-liner; pass " +
				"workspace if they mentioned a path, otherwise omit.",
			Schema: json.RawMessage(`{
				"goal": {"type": "string", "description": "Short one-liner describing the task"},
				"workspace": {"type": "string", "description": "Filesystem path the task targets (optional)"}
			}`),
		},
		{
			Name:        ToolShowStatus,
			Description: "List the user's existing sessions. Call when they ask 'what's running' / 'show sessions' / 'status'.",
			Schema:      json.RawMessage(`{}`),
		},
		{
			Name: ToolResumeSession,
			Description: "Continue a previous session. Call when the user says 'continue', 'resume', " +
				"'pick up yesterday's task', etc. Pass any hint they gave (date, ID prefix, topic).",
			Schema: json.RawMessage(`{
				"query": {"type": "string", "description": "Hint for which session to resume (optional)"}
			}`),
		},
		{
			Name: ToolExplain,
			Description: "Answer a meta question about gil itself ('what is an interview?', 'how does this work?'). " +
				"Pass the topic as the user phrased it.",
			Schema: json.RawMessage(`{
				"topic": {"type": "string", "description": "What the user is asking about"}
			}`),
		},
	}
}

// historyBudget is the max number of turns Conversation keeps. System
// prompt + tool defs are constant and excluded from this count. We
// trim the OLDEST turns first to preserve the recency the LLM actually
// reasons over. Ten was picked because the chat is intake-only — most
// real intake conversations close on 1–4 turns; ten leaves enough head-
// room for clarification ping-pong without ballooning context cost.
const historyBudget = 10

// Conversation holds the running chat state for a single intake session.
// It is NOT goroutine-safe — the chat REPL drives it serially from a
// single reader. Concurrent use would require external synchronization.
//
// History excludes the system prompt (carried in System) and the tool
// definitions (carried in Tools). On each Send call we append the user
// turn, ship the request, then append the assistant reply (text +
// tool_calls) so subsequent turns get the right context.
type Conversation struct {
	// History is the running list of user/assistant turns. Cap at
	// historyBudget (oldest dropped first). Initial state is empty —
	// the system prompt below is what teaches the model gil's role.
	History []provider.Message
	// Tools are the function definitions the LLM may invoke. Default
	// is DefaultTools(); callers may shrink the list (e.g. drop
	// resume_session for users with zero sessions) before construction.
	Tools []provider.ToolDef
	// System is the constant intake system prompt. Provider adapters
	// pin it at the head of the request so it is cached on Anthropic.
	System string
}

// NewConversation returns a fresh Conversation with the default intake
// system prompt and tool set. The caller may mutate Tools afterwards
// (the slice is owned by the Conversation, not shared) if they want to
// trim the surface for a particular run.
func NewConversation() *Conversation {
	return &Conversation{
		History: nil,
		Tools:   DefaultTools(),
		System:  IntakeSystemPrompt,
	}
}

// Turn is the result of one round-trip with the intake LLM. The chat
// REPL renders AssistantText (when non-empty) and dispatches each
// ToolCall in order. ToolCalls is empty for pure-text turns (greetings,
// clarifications), which is the load-bearing property: NO tool call
// means NO action is taken. This is the core of the redesign.
type Turn struct {
	// AssistantText is what the model said in plain text. The chat
	// REPL prints this verbatim to the user (after agentLine wrapping).
	AssistantText string
	// ToolCalls are the actions the model committed to. The chat REPL
	// dispatches each in order. Length 0 is the "just talk" case.
	ToolCalls []provider.ToolCall
}

// Send appends userMsg to History, calls the provider, and returns the
// resulting Turn. The assistant reply is also appended to History so
// follow-up turns see the right context.
//
// On provider error the user message is rolled back from History so a
// retry isn't double-counted. The chat REPL surfaces a friendly error
// and loops to re-prompt — Conversation stays usable.
//
// model is provider-specific (haiku for anthropic, gpt-4o-mini for
// openai, etc.). The caller picks based on credstore; this package
// stays credstore-free.
func (c *Conversation) Send(ctx context.Context, prov provider.Provider, model, userMsg string) (Turn, error) {
	if prov == nil {
		return Turn{}, fmt.Errorf("intent.Conversation.Send: provider is nil — chat must fall back to regex-only mode")
	}
	trimmed := strings.TrimSpace(userMsg)
	if trimmed == "" {
		// No-op send — the REPL shouldn't even reach this, but be
		// defensive: don't burn a token on an empty user turn.
		return Turn{}, nil
	}

	// Append user turn first so the LLM sees it in Messages. We snap-
	// shot the pre-call length so we can roll back on error.
	preLen := len(c.History)
	c.History = append(c.History, provider.Message{
		Role:    provider.RoleUser,
		Content: trimmed,
	})

	resp, err := prov.Complete(ctx, provider.Request{
		Model:       model,
		System:      c.System,
		Messages:    c.History,
		Tools:       c.Tools,
		MaxTokens:   512,
		Temperature: 0.2, // small but non-zero so brief replies vary slightly
	})
	if err != nil {
		// Roll back the user turn so the next attempt isn't seeing
		// a duplicate.
		c.History = c.History[:preLen]
		return Turn{}, fmt.Errorf("intent.Conversation.Send: %w", err)
	}

	// Record assistant reply (text + any tool calls) so context stays
	// coherent across turns. The provider adapter encodes tool_use
	// blocks correctly when this message is sent back next round.
	c.History = append(c.History, provider.Message{
		Role:      provider.RoleAssistant,
		Content:   resp.Text,
		ToolCalls: resp.ToolCalls,
	})

	// Trim oldest turns past the budget. We keep an EVEN number so
	// user/assistant pairs stay aligned — Anthropic rejects a leading
	// assistant turn ("first message must be user").
	c.trimHistory()

	return Turn{
		AssistantText: strings.TrimSpace(resp.Text),
		ToolCalls:     resp.ToolCalls,
	}, nil
}

// trimHistory drops the oldest turns when History exceeds historyBudget.
// We trim in pairs so the surviving prefix still starts with a user turn
// (Anthropic requires this). When the budget is odd the extra slot goes
// to recency — we never trim the most recent assistant turn.
func (c *Conversation) trimHistory() {
	if len(c.History) <= historyBudget {
		return
	}
	excess := len(c.History) - historyBudget
	// Round excess UP to the next even number so we drop user/assistant
	// pairs, never half a pair. (excess+1) & ^1 = next-even-up.
	if excess%2 == 1 {
		excess++
	}
	if excess > len(c.History) {
		excess = len(c.History)
	}
	c.History = c.History[excess:]
}

// StartInterviewArgs is the structured form of a start_interview tool
// call. The chat REPL parses ToolCall.Input into this so it can drive
// the existing interview flow without re-implementing JSON handling at
// every call site.
type StartInterviewArgs struct {
	Goal      string `json:"goal"`
	Workspace string `json:"workspace"`
}

// ResumeSessionArgs is the structured form of a resume_session call.
type ResumeSessionArgs struct {
	Query string `json:"query"`
}

// ExplainArgs is the structured form of an explain call.
type ExplainArgs struct {
	Topic string `json:"topic"`
}

// ParseStartInterview unmarshals a start_interview tool call's input.
// Returns zero-value args + error if the JSON is malformed; the chat
// REPL should treat that as "ask user to repeat" rather than crash.
func ParseStartInterview(tc provider.ToolCall) (StartInterviewArgs, error) {
	var a StartInterviewArgs
	if len(tc.Input) == 0 {
		return a, nil
	}
	if err := json.Unmarshal(tc.Input, &a); err != nil {
		return a, fmt.Errorf("parse start_interview input: %w", err)
	}
	a.Goal = strings.TrimSpace(a.Goal)
	a.Workspace = strings.TrimSpace(a.Workspace)
	return a, nil
}

// ParseResumeSession unmarshals a resume_session tool call's input.
func ParseResumeSession(tc provider.ToolCall) (ResumeSessionArgs, error) {
	var a ResumeSessionArgs
	if len(tc.Input) == 0 {
		return a, nil
	}
	if err := json.Unmarshal(tc.Input, &a); err != nil {
		return a, fmt.Errorf("parse resume_session input: %w", err)
	}
	a.Query = strings.TrimSpace(a.Query)
	return a, nil
}

// ParseExplain unmarshals an explain tool call's input.
func ParseExplain(tc provider.ToolCall) (ExplainArgs, error) {
	var a ExplainArgs
	if len(tc.Input) == 0 {
		return a, nil
	}
	if err := json.Unmarshal(tc.Input, &a); err != nil {
		return a, fmt.Errorf("parse explain input: %w", err)
	}
	a.Topic = strings.TrimSpace(a.Topic)
	return a, nil
}
