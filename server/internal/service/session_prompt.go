package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/workspace"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// session_prompt.go is the M1 cut of the chat-architecture migration
// (docs/design/chat-architecture.md). It introduces SessionService.Prompt
// as the single chat-surface entry point: every natural-language input
// from cli REPL or TUI flows through here, the daemon runs an agent
// loop, and Parts stream back.
//
// V1 scope (this commit):
//   - auto-create session when PromptRequest.session_id is empty
//   - in-memory per-session message history (sync.Map keyed by id)
//   - one provider.Complete call per Prompt — no tool registry yet
//   - whole assistant response streams as one TextDelta + Metrics + DonePart
//
// What's deliberately missing here, deferred to subsequent M1 commits:
//   - tool registry (show_diff, apply_diff, freeze_spec, start_run, ...)
//   - multi-turn agent loop (consume tool_call → tool_result → re-call LLM)
//   - chunked text streaming (split the model's text into multiple TextDeltas)
//   - persistent chat history (currently lost on daemon restart)
//   - subagent / spec-build flows
//
// The InterviewService stays in place for now; M2 swaps the chat
// clients over to SessionService.Prompt; M3 deletes the interview
// engine entirely.

// chatHistory holds the running message log per session for the V1
// chat agent loop. Keyed by session ID. Lives only in process memory
// — daemon restart wipes the conversation. Persistent storage is a
// follow-up within M1.
type chatHistory struct {
	mu  sync.Mutex
	all map[string][]provider.Message
}

func newChatHistory() *chatHistory {
	return &chatHistory{all: make(map[string][]provider.Message)}
}

func (h *chatHistory) get(sid string) []provider.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	src := h.all[sid]
	out := make([]provider.Message, len(src))
	copy(out, src)
	return out
}

func (h *chatHistory) append(sid string, msg provider.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.all[sid] = append(h.all[sid], msg)
}

// WithProviderFactory wires the same ProviderFactory used by Run /
// InterviewService so SessionService.Prompt can resolve a provider
// for the agent loop. Chaining-style for symmetry with WithBudgetGetter.
func (s *SessionService) WithProviderFactory(f ProviderFactory) *SessionService {
	s.providerFactory = f
	return s
}

// defaultChatSystemPrompt is the V1 system prompt for the "default"
// agent. It frames gil as an autonomous coding harness assistant
// without enumerating slash commands (there are none — see
// docs/design/chat-architecture.md §1).
//
// The prompt instructs the agent to call tools (show_diff /
// show_spec / show_status / list_sessions / request_compact) when
// the user asks about workspace state, rather than describing what
// it would do.
const defaultChatSystemPrompt = `You are gil, an autonomous coding harness assistant.

The user types in natural language. There are no slash commands.
Respond conversationally and call tools when they map to what the
user is asking for — don't describe what you would do, just do it.

You can actually read, edit, and execute code in the user's working
directory. When the user describes a coding task, do the work — don't
just talk about it. Use grep / glob to find relevant files, read_file
to inspect them, write_file to make edits, and run_bash to compile,
test, and verify.

Tools — workspace state (read-only):
- show_diff: see changes vs the last checkpoint.
- show_spec: see the frozen spec, if any.
- show_status: terse session status (phase, iter, cost).
- list_sessions: recent sessions (use to recall past work).
- request_compact: ask the runner to compact context next turn.

Tools — code I/O (scoped to the session working dir):
- read_file: read a file's contents.
- write_file: overwrite/create a file (atomic). Use for new files or
  full rewrites.
- edit_file: replace an exact text snippet in a file. Prefer this over
  write_file for small edits to large files — it's cheaper on tokens.
- apply_patch: apply a multi-file, multi-hunk patch atomically. Format:
  '*** Begin Patch' / '*** End Patch' envelope with '*** Add File: <p>',
  '*** Delete File: <p>', or '*** Update File: <p>' (followed by '@@'
  hunks of space/-/+ lines). All hunks must match exactly once or NO
  file is touched. Prefer over edit_file when you have multiple edits
  in the same call — saves round-trips and keeps changes coherent.
- run_bash: run a shell command (default 30s, max 60s).
- grep: regex search across the tree (uses ripgrep when present).
- glob: list files matching a pattern (** supported for recursion).
- todowrite: persist a session todo list (statuses pending /
  in_progress / completed). Use when a task has 3+ steps and no
  verification gate. For verification-gated work prefer plan_steps.
- plan_steps: declare a verification-gated plan. Each step has a
  description and an acceptance_check command. Statuses (pending /
  verified / failed) are SYSTEM-MANAGED — only the verify tool can
  transition a step to verified or failed by running its
  acceptance_check.
- verify: run a verification command. When called with step_id, it
  transitions the matching plan_step on success/failure. After every
  code-changing tool call (write_file, edit_file, apply_patch) you
  MUST run verify before progressing or declaring the work done.
- webfetch: GET an http(s) URL, capped at 256 KB / 15s. Use for docs,
  issue links, public web content.

Tools — session lifecycle (call when the user wants autonomous run):
- freeze_spec: persist a frozen spec (goal + optional
  constraints/verification/budget/autonomy) onto the session. Required
  before start_run. Call ONCE per session; a frozen spec is immutable.
  Pass only the slots you've extracted from conversation —
  goal.one_liner is the only hard requirement, everything else is
  optional. Don't ask the user to re-state things you already know.
- start_run: kick the autonomous run loop on a frozen spec. Detached;
  use show_status / list_sessions to observe progress. Refuses
  unfrozen sessions.
- apply_diff: confirm what the agent's edits landed this turn. In chat
  mode edits write directly to the working directory; this is for the
  "apply it / 적용해" moment after show_diff.

Tools — subagent delegation (call to split work in parallel):
- spawn_agent: create a child agent running on a sliced copy of this
  session's frozen spec. Pass a short label (lowercase) and a task
  string the child receives as its first user message. Optional
  agent_type (default / explore / plan) and spec_override (narrows
  workspace / tools / max_iterations). Subject to V1 caps: max 8
  active children per root, depth 1 only (children cannot spawn
  further). Returns the child's agent_id + label.
- wait_agent: block until a spawned child reaches terminal state
  (done / failed / stopped / budget_exceeded). Identify by agent_id
  (from spawn_agent) OR label. Default 600s timeout.
- agent_status: non-blocking list of this session's children with
  their current status / iter / tokens / cost.

Workflow guidance:
- For non-trivial coding tasks: declare a plan_steps plan first (each
  step with an acceptance_check command), then for each step: do the
  edit (write_file/edit_file/apply_patch) and IMMEDIATELY call verify
  with the step_id. Do not move on to the next step until the current
  step is verified (or you've decided to revise the plan because the
  acceptance_check itself was wrong). Do not tell the user the work is
  done until the final step is verified.
- For trivial tasks (one-line edits, exploration): plan_steps is
  overhead; just do the edit and call verify once at the end.
- Show the user a short summary at the end with what changed and the
  final verify result.
- For an ambiguous task: ask 1-2 focused clarifying questions
  (goal, scope, success criteria) before doing destructive work. For
  obvious tasks just proceed.
- For a question about workspace state: call the matching read-only
  tool (show_diff, list_sessions, …) instead of describing what you'd
  show.
- Don't enumerate available commands to the user.
- If asked "what model are you" / "어떤 모델이야", answer plainly with
  the configured provider and model from the system context line below.
- Match the user's language (English or Korean).

System context: provider=%s model=%s session=%s
`

// Prompt is the streaming agent-loop RPC. See file header for the
// V1 scope. Errors that should terminate the stream return a gRPC
// status; recoverable errors (provider blip on a single turn) emit
// a DonePart with stop_reason="error" and let the caller decide.
func (s *SessionService) Prompt(req *gilv1.PromptRequest, stream gilv1.SessionService_PromptServer) error {
	ctx := stream.Context()

	// 1. Resolve / auto-create the session.
	sessionID := req.GetSessionId()
	if sessionID == "" {
		hint := firstTextPart(req.GetParts())
		hint = truncateGoalHint(hint, 80)
		sess, err := s.repo.Create(ctx, session.CreateInput{GoalHint: hint})
		if err != nil {
			return status.Errorf(codes.Internal, "auto-create session: %v", err)
		}
		sessionID = sess.ID
		if err := stream.Send(&gilv1.Part{
			Body: &gilv1.Part_SessionAllocated{
				SessionAllocated: &gilv1.SessionAllocatedPart{SessionId: sessionID},
			},
		}); err != nil {
			return err
		}
	} else {
		// Verify the session exists; surface NotFound if it doesn't.
		if _, err := s.repo.Get(ctx, sessionID); err != nil {
			if errors.Is(err, session.ErrNotFound) {
				return status.Errorf(codes.NotFound, "session %q not found", sessionID)
			}
			return status.Errorf(codes.Internal, "session lookup: %v", err)
		}
	}

	// 2. Resolve provider + model. PromptRequest.Model overrides; empty
	//    falls through to the workspace config layer (global + project).
	if s.providerFactory == nil {
		return status.Error(codes.FailedPrecondition,
			"chat agent loop requires a provider factory; gild was started without one")
	}
	provName := req.GetModel().GetProvider()
	modelID := req.GetModel().GetModelId()
	if provName == "" || modelID == "" {
		if cfgProv, cfgModel := resolveWorkspaceLLM(); cfgProv != "" || cfgModel != "" {
			if provName == "" {
				provName = cfgProv
			}
			if modelID == "" {
				modelID = cfgModel
			}
		}
	}
	prov, factoryModel, ferr := s.providerFactory(provName)
	if ferr != nil {
		return status.Errorf(codes.InvalidArgument, "provider: %v", ferr)
	}
	if modelID == "" {
		modelID = factoryModel
	}

	// 3. Build messages: prior history + new user message.
	hist := s.chatHistory().get(sessionID)
	userText := firstTextPart(req.GetParts())
	if userText == "" {
		return status.Error(codes.InvalidArgument, "prompt requires a non-empty text part")
	}
	// Persist the user turn upfront so the history reflects the
	// real conversation even if the agent loop terminates early
	// (provider error, max iterations).
	s.chatHistory().append(sessionID, provider.Message{Role: provider.RoleUser, Content: userText})
	msgs := append(hist, provider.Message{
		Role:    provider.RoleUser,
		Content: userText,
	})

	// 4. Resolve the agent profile (system prompt + tool whitelist).
	//    PromptRequest.agent picks; empty → "default".
	agent, agentErr := resolveAgent(req.GetAgent())
	if agentErr != nil {
		return status.Errorf(codes.InvalidArgument, "%v", agentErr)
	}

	// Reset the turn-scoped diff tracker so show_diff only ever returns
	// what *this* invocation of the agent did, not history. Wired tools
	// (write_file, edit_file, apply_patch, run_bash) populate it as
	// they fire.
	if s.diffTracker != nil {
		s.diffTracker.reset(sessionID)
	}
	// 5. Build the tool registry for this turn, filtered by the
	//    agent's tool whitelist (empty whitelist = full registry).
	registry := s.buildChatToolRegistry(s.runService()).filterByName(agent.Tools)
	toolDefs := registry.defs()

	// 6. Multi-turn agent loop. Each iteration calls the LLM; if it
	//    emits tool_calls, we dispatch them, append the results, and
	//    re-call. The loop terminates when the LLM returns no tool
	//    calls (StopReason="end_turn") or we hit the iteration cap.
	const maxAgentTurns = 8
	systemPrompt := fmt.Sprintf(agent.SystemPrompt, provName, modelID, sessionID)
	var totalTokensIn, totalTokensOut int64
	var totalLatency time.Duration

	for turn := 0; turn < maxAgentTurns; turn++ {
		t0 := time.Now()
		resp, err := prov.Complete(ctx, provider.Request{
			Model:       modelID,
			Messages:    msgs,
			System:      systemPrompt,
			Tools:       toolDefs,
			MaxTokens:   2048,
			Temperature: 0.7,
		})
		totalLatency += time.Since(t0)
		if err != nil {
			_ = stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Done{
					Done: &gilv1.DonePart{StopReason: "error", ErrorMessage: err.Error()},
				},
			})
			return status.Errorf(codes.Internal, "provider.Complete: %v", err)
		}
		totalTokensIn += resp.InputTokens
		totalTokensOut += resp.OutputTokens

		// Stream any text the LLM emitted on this turn.
		if resp.Text != "" {
			if err := stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{Content: resp.Text}},
			}); err != nil {
				return err
			}
		}

		// If no tool calls, the LLM is done.
		if len(resp.ToolCalls) == 0 {
			s.chatHistory().append(sessionID,
				provider.Message{Role: provider.RoleAssistant, Content: resp.Text})
			break
		}

		// Append the assistant turn (with tool calls) to messages so
		// the LLM sees the call→result correlation on the next turn.
		assistantTurn := provider.Message{
			Role:      provider.RoleAssistant,
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		}
		msgs = append(msgs, assistantTurn)
		s.chatHistory().append(sessionID, assistantTurn)

		// Dispatch each tool call, stream Parts, collect results.
		toolResults := make([]provider.ToolResult, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			if err := stream.Send(&gilv1.Part{
				Body: &gilv1.Part_ToolCall{ToolCall: &gilv1.ToolCallPart{
					Id:        call.ID,
					Name:      call.Name,
					InputJson: string(call.Input),
				}},
			}); err != nil {
				return err
			}
			result, runErr := dispatchTool(ctx, registry, sessionID, call)
			if runErr != nil {
				result = provider.ToolResult{
					ToolUseID: call.ID,
					Content:   "tool dispatch failed: " + runErr.Error(),
					IsError:   true,
				}
			}
			toolResults = append(toolResults, result)
			if err := stream.Send(&gilv1.Part{
				Body: &gilv1.Part_ToolResult{ToolResult: &gilv1.ToolResultPart{
					CallId:  call.ID,
					Content: result.Content,
					IsError: result.IsError,
				}},
			}); err != nil {
				return err
			}
		}

		// Feed the tool results back as a synthetic user turn (per
		// Anthropic's tool_result block convention) so the LLM can
		// see them on the next iteration.
		toolFeedback := provider.Message{
			Role:        provider.RoleUser,
			ToolResults: toolResults,
		}
		msgs = append(msgs, toolFeedback)
		s.chatHistory().append(sessionID, toolFeedback)
	}

	// 6. Metrics + Done.
	if err := stream.Send(&gilv1.Part{
		Body: &gilv1.Part_Metrics{Metrics: &gilv1.PromptMetrics{
			TokensIn:  totalTokensIn,
			TokensOut: totalTokensOut,
			LatencyMs: totalLatency.Milliseconds(),
		}},
	}); err != nil {
		return err
	}
	if err := stream.Send(&gilv1.Part{
		Body: &gilv1.Part_Done{Done: &gilv1.DonePart{StopReason: "end_turn"}},
	}); err != nil {
		return err
	}
	return nil
}

// dispatchTool looks up a tool by name and invokes it with the
// LLM-provided input. Returns a typed ToolResult ready to feed back
// to the LLM. Unknown tool names produce an IsError result so the
// LLM can self-correct without crashing the stream.
func dispatchTool(ctx context.Context, registry *chatToolRegistry, sessionID string, call provider.ToolCall) (provider.ToolResult, error) {
	tool, ok := registry.lookup(call.Name)
	if !ok {
		return provider.ToolResult{
			ToolUseID: call.ID,
			Content:   "unknown tool: " + call.Name,
			IsError:   true,
		}, nil
	}
	r, err := tool.run(ctx, sessionID, call.Input)
	r.ToolUseID = call.ID
	return r, err
}

// chatHistory lazily allocates the message log map. Stored on the
// service struct via a method-level singleton instead of constructor
// wiring so existing test setups (NewSessionService(repo, nil)) keep
// compiling without churn.
func (s *SessionService) chatHistory() *chatHistory {
	s.chatHistMu.Lock()
	defer s.chatHistMu.Unlock()
	if s.chatHist == nil {
		s.chatHist = newChatHistory()
	}
	return s.chatHist
}

// firstTextPart pulls the text body off the first PromptPart that
// has one. V1 only supports text, so this is the canonical reader;
// future image/file parts will need their own extractors.
func firstTextPart(parts []*gilv1.PromptPart) string {
	for _, p := range parts {
		if t := p.GetText(); t != "" {
			return t
		}
	}
	return ""
}

// truncateGoalHint clips a free-form prompt for use as the auto-
// created session's goal_hint column. Same shape as the cli REPL's
// truncateHint helper but lives here so the daemon's session row
// stays compact regardless of whether the request came from cli or
// tui.
func truncateGoalHint(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// resolveWorkspaceLLM reads the layered workspace config (global +
// the daemon's cwd as a stand-in for project-local) and returns the
// provider/model pair. Best-effort — any error returns ("", "") and
// the caller falls back to the providerFactory's defaults.
func resolveWorkspaceLLM() (string, string) {
	layout, err := paths.FromEnv()
	if err != nil {
		return "", ""
	}
	cfg, err := workspace.Resolve(layout.ConfigFile(), "")
	if err != nil {
		return "", ""
	}
	return cfg.Provider, cfg.Model
}

// _ stops the linter complaining about the unused fmt import for
// follow-up commits that will reach for fmt for tool-call rendering.
var _ = fmt.Sprintf
