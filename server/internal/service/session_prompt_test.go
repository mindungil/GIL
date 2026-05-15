package service

import (
	"context"
	"strings"
	"testing"

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
