package service

import (
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
// V1 deliberately does not yet describe tool calls — no tools are
// registered. The prompt will grow as M1 lands the tool registry.
const defaultChatSystemPrompt = `You are gil, an autonomous coding harness assistant.

The user types in natural language. There are no slash commands. You
respond conversationally. When the user describes a task, ask a few
focused clarifying questions to understand the goal, scope,
constraints, and what success looks like. Keep your tone direct and
low-ceremony.

Guidance:
- Don't enumerate commands or menus to the user.
- Don't describe yourself unless asked.
- If the user greets you, greet them back briefly and invite them to
  describe a task they'd like the harness to run.
- If the user asks about you ("what model are you", "어떤 모델이야"),
  answer plainly with the configured provider/model from the system
  context.
- Match the user's language (English or Korean).
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
	msgs := append(hist, provider.Message{
		Role:    provider.RoleUser,
		Content: userText,
	})

	// 4. Call the provider. V1 uses no tools — that lands in the next
	//    commit. Single-shot completion; whole text streams as one
	//    TextDelta. Latency is captured for the metrics part.
	t0 := time.Now()
	resp, err := prov.Complete(ctx, provider.Request{
		Model:       modelID,
		Messages:    msgs,
		System:      defaultChatSystemPrompt,
		MaxTokens:   2048,
		Temperature: 0.7,
	})
	latency := time.Since(t0)
	if err != nil {
		// Stream-level error: emit a Done with stop_reason="error" so
		// the chat surface renders something instead of just an EOF.
		_ = stream.Send(&gilv1.Part{
			Body: &gilv1.Part_Done{
				Done: &gilv1.DonePart{StopReason: "error", ErrorMessage: err.Error()},
			},
		})
		return status.Errorf(codes.Internal, "provider.Complete: %v", err)
	}

	// 5. Stream the response.
	if resp.Text != "" {
		if err := stream.Send(&gilv1.Part{
			Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{Content: resp.Text}},
		}); err != nil {
			return err
		}
	}

	// 6. Persist the user + assistant turn to in-memory history.
	s.chatHistory().append(sessionID, provider.Message{Role: provider.RoleUser, Content: userText})
	s.chatHistory().append(sessionID, provider.Message{Role: provider.RoleAssistant, Content: resp.Text})

	// 7. Metrics + Done.
	if err := stream.Send(&gilv1.Part{
		Body: &gilv1.Part_Metrics{Metrics: &gilv1.PromptMetrics{
			TokensIn:  resp.InputTokens,
			TokensOut: resp.OutputTokens,
			LatencyMs: latency.Milliseconds(),
		}},
	}); err != nil {
		return err
	}
	stopReason := resp.StopReason
	if stopReason == "" {
		stopReason = "end_turn"
	}
	if err := stream.Send(&gilv1.Part{
		Body: &gilv1.Part_Done{Done: &gilv1.DonePart{StopReason: stopReason}},
	}); err != nil {
		return err
	}
	return nil
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
