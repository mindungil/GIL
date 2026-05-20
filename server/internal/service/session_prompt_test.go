package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/grpc/metadata"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// fakePromptStream implements grpc.ServerStreamingServer[gilv1.Part]
// (= gilv1.SessionService_PromptServer). It buffers all sent Parts
// and returns a background context.
type fakePromptStream struct {
	ctx   context.Context
	Parts []*gilv1.Part
}

func (f *fakePromptStream) Send(p *gilv1.Part) error {
	f.Parts = append(f.Parts, p)
	return nil
}
func (f *fakePromptStream) Context() context.Context { return f.ctx }

// grpc.ServerStream stubs required to satisfy the interface.
func (f *fakePromptStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakePromptStream) SendHeader(metadata.MD) error { return nil }
func (f *fakePromptStream) SetTrailer(metadata.MD)       {}
func (f *fakePromptStream) RecvMsg(any) error            { return nil }
func (f *fakePromptStream) SendMsg(any) error            { return nil }

// newTestSessionServiceWithMockTurns builds a *SessionService whose
// providerFactory drains turns on successive Complete calls, plus a
// session ID rooted at a temp working directory. Analogous to
// newRunSvc in run_test.go.
func newTestSessionServiceWithMockTurns(t *testing.T, turns []provider.MockTurn) (*SessionService, string) {
	t.Helper()
	repo := newTestRepo(t)
	wd := t.TempDir()
	sess, err := repo.Create(t.Context(), session.CreateInput{
		WorkingDir: wd,
		GoalHint:   "test",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	factory := func(name string) (provider.Provider, string, error) {
		return provider.NewMockToolProvider(turns), "mock-model", nil
	}
	svc := NewSessionService(repo, nil).WithProviderFactory(factory)
	return svc, sess.ID
}

// newTestSessionServiceWithSharedProvider is like
// newTestSessionServiceWithMockTurns but returns the SAME provider
// instance on every factory call. This matters for multi-turn tests
// where the chat path runs `s.providerFactory()` once per Prompt()
// invocation: the default factory creates a fresh MockToolProvider per
// call and resets the scripted queue. Tests that need turn-count state
// to survive across user turns (e.g., P67i escalation, where pattern
// fire counts accumulate across Prompt() calls) pass a shared
// MockToolProvider via this helper.
func newTestSessionServiceWithSharedProvider(t *testing.T, p provider.Provider) (*SessionService, string) {
	t.Helper()
	repo := newTestRepo(t)
	wd := t.TempDir()
	sess, err := repo.Create(t.Context(), session.CreateInput{
		WorkingDir: wd,
		GoalHint:   "test",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	factory := func(name string) (provider.Provider, string, error) {
		return p, "mock-model", nil
	}
	svc := NewSessionService(repo, nil).WithProviderFactory(factory)
	return svc, sess.ID
}

// promptReq constructs a PromptRequest targeting an existing session with
// a single text part.
func promptReq(sid, text string) *gilv1.PromptRequest {
	return &gilv1.PromptRequest{
		SessionId: sid,
		Parts:     []*gilv1.PromptPart{{Body: &gilv1.PromptPart_Text{Text: text}}},
	}
}

// TestPrompt_WriteWithoutVerify_RetriesThenErrors uses a stub provider that
// emits a write_file tool call once and then signals end_turn with no further
// tool calls. The system should inject a verify reminder, loop again, and when
// the stub model still doesn't verify, emit a verify_missing error termination.
func TestPrompt_WriteWithoutVerify_RetriesThenErrors(t *testing.T) {
	turns := []provider.MockTurn{
		// Turn 1: write_file (no verify).
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "write_file",
			Input: []byte(`{"path":"main.go","content":"package main\n"}`)}},
			StopReason: "tool_use"},
		// Turn 2: model thinks it's done (no tool calls) — triggers gate.
		{Text: "done", StopReason: "end_turn"},
		// Turn 3: after first verify-reminder injection, model still doesn't verify.
		{Text: "still done", StopReason: "end_turn"},
		// Turn 4: after second verify-reminder injection, model still doesn't verify.
		{Text: "really done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	err := svc.Prompt(promptReq(sid, "make a main.go"), stream)
	if err == nil {
		t.Fatalf("expected error termination after exhausting verify retries; got nil")
	}
	// Confirm the system injected a verify reminder at least once.
	injected := 0
	for _, p := range stream.Parts {
		if td := p.GetText(); td != nil && strings.Contains(td.GetContent(), "verify") {
			injected++
		}
	}
	if injected == 0 {
		t.Fatalf("no verify-reminder text seen in stream; got %d parts", len(stream.Parts))
	}
}

// TestPrompt_WriteThenVerify_TurnEndsCleanly: write_file then verify then
// end_turn → no injection, no error.
//
// The verify command uses "go version" (not a weak inspect-only command;
// always exits 0 on this machine) so the tool returns IsError=false.
func TestPrompt_WriteThenVerify_TurnEndsCleanly(t *testing.T) {
	turns := []provider.MockTurn{
		// Turn 1: write_file.
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "write_file",
			Input: []byte(`{"path":"main.go","content":"package main\n"}`)}},
			StopReason: "tool_use"},
		// Turn 2: verify (go version always exits 0 and is not a weak command).
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "verify",
			Input: []byte(`{"description":"check go toolchain","command":"go version"}`)}},
			StopReason: "tool_use"},
		// Turn 3: model is done.
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	if err := svc.Prompt(promptReq(sid, "make a main.go and verify"), stream); err != nil {
		t.Fatalf("clean run errored: %v", err)
	}
	// Confirm no verify-reminder was injected.
	for _, p := range stream.Parts {
		if td := p.GetText(); td != nil && strings.Contains(td.GetContent(), "[system]") {
			t.Fatalf("unexpected system injection in clean run: %q", td.GetContent())
		}
	}
}

// TestPrompt_WriteVerifyWriteNoVerify_RetriesThenErrors covers the
// regression where a successful verify mid-turn shouldn't satisfy a
// subsequent write's verify obligation.
func TestPrompt_WriteVerifyWriteNoVerify_RetriesThenErrors(t *testing.T) {
	turns := []provider.MockTurn{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "write_file",
			Input: []byte(`{"path":"a.go","content":"package main\n"}`)}}},
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "verify",
			Input: []byte(`{"description":"build","command":"go version"}`)}}},
		{ToolCalls: []provider.ToolCall{{ID: "c3", Name: "write_file",
			Input: []byte(`{"path":"b.go","content":"package main\nvar B = 1\n"}`)}}},
		{Text: "done"},
		{Text: "still done"},
		{Text: "really done"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	err := svc.Prompt(promptReq(sid, "write a then verify then write b"), stream)
	if err == nil {
		t.Fatalf("expected verify_missing error after unverified second write; got nil")
	}
}

// TestPrompt_LoopCapHitWithUnverifiedWrite_BackstopFires covers the
// eval-loop iter6 regression: agent emits 8+ turns of tool calls
// without ever stopping at a no-tool-calls boundary, so the C1 gate
// inside the for loop never fires. The post-loop backstop must catch
// this and emit verify_missing.
//
// Sequence: 8 turns each emitting one write_file, never a verify.
// maxAgentTurns=30 (P63 lift). The for loop exits after turn 29
// (0-indexed) without entering the no-tool-calls branch. Backstop
// must fire. Use 32 turns to ensure cap is hit.
func TestPrompt_LoopCapHitWithUnverifiedWrite_BackstopFires(t *testing.T) {
	var turns []provider.MockTurn
	for i := 0; i < 32; i++ {
		turns = append(turns, provider.MockTurn{
			ToolCalls: []provider.ToolCall{{
				ID:    "c" + fmt.Sprintf("%d", i),
				Name:  "write_file",
				Input: []byte(fmt.Sprintf(`{"path":"f%d.go","content":"package x\n"}`, i)),
			}},
		})
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	err := svc.Prompt(promptReq(sid, "write 9 files no verify"), stream)
	if err == nil {
		t.Fatalf("expected verify_missing error from post-loop backstop; got nil")
	}
	if !strings.Contains(err.Error(), "agent turn cap reached") {
		t.Fatalf("expected backstop error message, got: %v", err)
	}
	// Done part should carry verify_missing as stop reason.
	sawDone := false
	for _, p := range stream.Parts {
		if d := p.GetDone(); d != nil {
			sawDone = true
			if d.GetStopReason() != "verify_missing" {
				t.Fatalf("expected stop_reason verify_missing, got %q", d.GetStopReason())
			}
		}
	}
	if !sawDone {
		t.Fatalf("no Done part in stream")
	}
}

// iter18a: dispatchTool sanitizes tool result Content so non-UTF-8
// bytes (e.g. from read_file on a file truncated mid-multibyte, or
// run_bash on a binary) don't crash the gRPC stream with "marshaling:
// string field contains invalid UTF-8". Replacement char preserves
// the agent's ability to reason about surrounding context.
func TestDispatchTool_SanitizesInvalidUTF8Content(t *testing.T) {
	// Build a fake tool that returns a partial multibyte sequence — the
	// exact shape L18's gil.log had after display-layer truncation
	// inserted "…" (U+2026) mid-character.
	ft := &fakeContentTool{
		out: provider.ToolResult{
			// "abc" + lone-continuation 0xea + "def" — 0xea is the
			// first byte of a 3-byte sequence but no continuation
			// bytes follow, so the string is invalid UTF-8.
			Content: "abc\xeadef",
		},
	}
	reg := &chatToolRegistry{tools: []chatTool{ft}}
	res, err := dispatchTool(context.Background(), reg, "sid",
		provider.ToolCall{ID: "tc1", Name: "fakecontent", Input: []byte("{}")})
	if err != nil {
		t.Fatalf("dispatchTool: %v", err)
	}
	if !utf8.ValidString(res.Content) {
		t.Fatalf("dispatchTool must produce valid UTF-8; got %q", res.Content)
	}
	// Original byte 0xea should be replaced by U+FFFD; "abc" and "def"
	// must survive intact.
	if !strings.Contains(res.Content, "abc") || !strings.Contains(res.Content, "def") {
		t.Fatalf("surrounding text must survive sanitization; got %q", res.Content)
	}
}

type fakeContentTool struct{ out provider.ToolResult }

func (f *fakeContentTool) name() string            { return "fakecontent" }
func (f *fakeContentTool) description() string     { return "" }
func (f *fakeContentTool) schema() json.RawMessage { return json.RawMessage(`{}`) }
func (f *fakeContentTool) run(_ context.Context, _ string, _ json.RawMessage) (provider.ToolResult, error) {
	return f.out, nil
}

// P50: when the agent calls verify and it fails, then hits the turn
// cap, the verify_missing message includes a tail of the verify
// output so the user can re-prompt with concrete context. Without
// P50 the user only saw "you never verified" which mis-describes
// the situation.
func TestPrompt_LoopCapHitWithFailedVerify_BackstopIncludesLastOutput(t *testing.T) {
	// Sequence: write, verify (fails), write, verify (fails), … until cap.
	// P63: maxAgentTurns=30, so need ≥16 (write+verify) pairs = 32 turns.
	var turns []provider.MockTurn
	for i := 0; i < 16; i++ {
		turns = append(turns,
			provider.MockTurn{
				ToolCalls: []provider.ToolCall{{
					ID: fmt.Sprintf("w%d", i), Name: "write_file",
					Input: []byte(fmt.Sprintf(`{"path":"f%d.go","content":"package x\n"}`, i)),
				}},
			},
			provider.MockTurn{
				ToolCalls: []provider.ToolCall{{
					ID: fmt.Sprintf("v%d", i), Name: "verify",
					Input: []byte(`{"command":"false","description":"deliberate fail"}`),
				}},
			},
		)
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	err := svc.Prompt(promptReq(sid, "write and verify-fail loop"), stream)
	if err == nil {
		t.Fatalf("expected verify_missing error; got nil")
	}
	if !strings.Contains(err.Error(), "Last verify output") {
		t.Fatalf("expected message to include 'Last verify output' tail; got: %v", err)
	}
}
