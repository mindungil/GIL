// Package tool — clarify is the safety-valve tool that lets a long-
// running agent pause the run and ask a human for input. The interview
// is supposed to extract requirements upfront; clarify is what the
// agent reaches for when an unforeseen blocker (ambiguous spec, missing
// credential, external service unexpectedly down) appears mid-run.
//
// Reference lift: cline's `ask_followup_question` tool shape — question
// + suggested answers + the agent-pauses-then-resumes flow where the
// server holds the iteration mid-air on a channel and feeds the human
// answer back as the tool_result. We add a urgency hint so the
// outbound notify channels (desktop bell / Slack webhook / stdout) can
// route the question at the right loudness without re-parsing it.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// ClarifyAsk is a single pending clarification request: what the agent
// asked + the optional context/suggestions/urgency the notify channels
// use. The server's AskClarifyCallback receives this and is responsible
// for emitting the clarify_requested event, dispatching the
// notification, blocking on its own per-session channel, and returning
// the user's free-form answer (or an error/empty string on timeout).
type ClarifyAsk struct {
	Question    string   // the question to ask the user
	Context     string   // optional: what the agent was doing
	Suggestions []string // optional 1-4 pre-baked answers
	Urgency     string   // "low" | "normal" | "high"
}

// ClarifyAnswer is what the server returns to the tool: the answer
// string itself plus a flag distinguishing "user answered" from "we
// timed out" so the tool can render a more useful tool_result body
// (the agent reacts differently to "user said X" vs "no response, you
// pick").
type ClarifyAnswer struct {
	Answer    string // free-form answer; empty when TimedOut
	TimedOut  bool
	Cancelled bool // session ended / context cancelled before answer arrived
}

// AskClarifyCallback is the server-side hook that pauses the runner.
// nil-safe: when the tool's Ask field is unset, Run returns an error
// result explaining the tool is not wired (matches the plan tool's
// "not configured" path).
type AskClarifyCallback func(ctx context.Context, sessionID string, ask ClarifyAsk) (ClarifyAnswer, error)

// Clarify is the agent-callable tool. SessionID is required so the
// server can route the answer back to the correct paused run; Ask is
// the callback the server wires (mirrors AskCallback for permissions).
type Clarify struct {
	SessionID string
	Ask       AskClarifyCallback
}

const clarifySchema = `{
  "type":"object",
  "properties":{
    "question":{
      "type":"string",
      "description":"The clarifying question. Be specific and concise."
    },
    "context":{
      "type":"string",
      "description":"Optional brief context — what was the agent doing? what does it need to know?"
    },
    "suggestions":{
      "type":"array",
      "items":{"type":"string"},
      "description":"Optional 1-4 suggested answers for the user to pick from."
    },
    "urgency":{
      "type":"string",
      "enum":["low","normal","high"],
      "description":"Hint to the notification channel — high triggers desktop bell + webhook, normal webhook only, low stdout/log only."
    }
  },
  "required":["question"]
}`

// Name implements tool.Tool.
func (c *Clarify) Name() string { return "clarify" }

// Description implements tool.Tool. The wording deliberately discourages
// trivial use — the interview is supposed to do the heavy lifting; this
// tool is for the rare unforeseen blocker.
func (c *Clarify) Description() string {
	return "Pause the run and ask the user a single clarifying question. ONLY use when truly necessary — the interview should have extracted requirements upfront. Examples: external service unexpectedly down, ambiguous user intent, missing credential. Tool returns the user's answer as a string. Do NOT use for trivial choices — make a best-effort decision instead."
}

// Schema implements tool.Tool.
func (c *Clarify) Schema() json.RawMessage { return json.RawMessage(clarifySchema) }

// Run validates the agent's input, then delegates to the wired Ask
// callback. The callback owns the per-session pending-channel, the
// clarify_requested event emission, and the outbound notification
// fan-out. Run is a thin shim that converts the JSON args into a
// ClarifyAsk and renders the ClarifyAnswer back into a tool_result.
func (c *Clarify) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	if c.Ask == nil || c.SessionID == "" {
		return Result{
			Content: "clarify tool not configured for this run (no callback wired); the agent should make a best-effort decision and continue.",
			IsError: true,
		}, nil
	}
	var args struct {
		Question    string   `json:"question"`
		Context     string   `json:"context"`
		Suggestions []string `json:"suggestions"`
		Urgency     string   `json:"urgency"`
	}
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return Result{}, fmt.Errorf("clarify unmarshal: %w", err)
		}
	}
	if args.Question == "" {
		return Result{Content: "clarify: 'question' is required", IsError: true}, nil
	}
	// Cap suggestions at 4 (per schema description): silently drop the
	// rest rather than erroring so a model that miscounts still gets
	// something useful through.
	if len(args.Suggestions) > 4 {
		args.Suggestions = args.Suggestions[:4]
	}
	urgency := normalizeUrgency(args.Urgency)

	answer, err := c.Ask(ctx, c.SessionID, ClarifyAsk{
		Question:    args.Question,
		Context:     args.Context,
		Suggestions: args.Suggestions,
		Urgency:     urgency,
	})
	if err != nil {
		return Result{Content: "clarify failed: " + err.Error(), IsError: true}, nil
	}
	return renderClarifyAnswer(answer), nil
}

// normalizeUrgency lower-cases and maps any unknown value to "normal".
// Empty string also maps to "normal" — the model is allowed to omit
// the field when the question doesn't fit a clear severity.
func normalizeUrgency(u string) string {
	switch u {
	case "low", "high":
		return u
	default:
		return "normal"
	}
}

// renderClarifyAnswer turns the server's ClarifyAnswer into the tool
// result the agent sees. We deliberately mark TimedOut/Cancelled as
// IsError so the agent's error-handling path triggers (it should
// proceed with a best-effort decision rather than re-asking).
func renderClarifyAnswer(a ClarifyAnswer) Result {
	if a.Cancelled {
		return Result{
			Content: "clarification was cancelled (session ended or context cancelled). The agent should make a best-effort decision and continue.",
			IsError: true,
		}
	}
	if a.TimedOut {
		return Result{
			Content: "clarification timed out — the agent should make a best-effort decision and continue.",
			IsError: true,
		}
	}
	if a.Answer == "" {
		return Result{
			Content: "user answered with an empty string; the agent should make a best-effort decision and continue.",
			IsError: false,
		}
	}
	return Result{Content: "user answered: " + a.Answer}
}
