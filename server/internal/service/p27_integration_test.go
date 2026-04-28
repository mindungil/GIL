//go:build integration

package service

// P27 T7 — Compactor + cache-markers end-to-end integration tests.
//
// These tests prove that T3, T4, and T5 wiring survives composition:
//
//   T3: RunService.executeRun wires a Compactor from spec (not nil).
//       Proved by TestP27_CompactionFiresViaTool — goes through svc.Start.
//   T4: MarkCacheBreakpoints fires before Complete for "anthropic" providers.
//       Proved by TestP27_AnthropicCacheMarkersInRequest — goes through svc.Start
//       with a recording provider that impersonates "anthropic".
//   T5: per-model context window (from capacity table) drives the compaction
//       threshold when MaxContextTokens is not explicitly set.
//       Proved by TestP27_ContextWindowDrivesThreshold — uses AgentLoop directly
//       with MaxContextTokens=0 so the T5 lookup path fires.
//
// NOTE ON PACKAGE LOCATION
// ────────────────────────
// The task spec asked for tests/integration/p27_context_wiring_test.go.
// Go's internal-package rule (go/build §5.3) forbids importing
// github.com/mindungil/gil/server/internal/* from outside the server
// module, making a separate tests/integration module impossible without
// a full gRPC round-trip scaffold.  The existing test art in this
// package already exercises RunService at composition level — using it
// here avoids inventing parallel infrastructure while still proving the
// wiring is live.  Build tag //go:build integration gates these tests
// identically to server/cmd/gild/gateway_test.go.
//
// NOTE ON T4 BUG FIX
// ──────────────────
// Writing these tests revealed that runner.go's T4 gate used
// iterProvider.Name() == "anthropic" which never matched in production
// because RunService wraps every provider in NewRetry (Name() returns
// "anthropic+retry").  The gate was changed to strings.HasPrefix so the
// check survives the wrapper suffix.  That fix is in runner.go and is
// exercised by TestP27_AnthropicCacheMarkersInRequest.
//
// Run:
//
//	go test -tags integration ./server/internal/service/... -run TestP27 -v

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/compact"
	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/runner"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	"github.com/mindungil/gil/core/verify"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// ─── shared helpers ────────────────────────────────────────────────────────

// newRunSvcWithFactory mirrors newRunSvc but accepts a custom ProviderFactory
// so individual tests can inject recording or anthropic-named providers.
func newRunSvcWithFactory(t *testing.T, factory ProviderFactory) (*RunService, *session.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	repo := session.NewRepo(db)
	sessionsBase := filepath.Join(dir, "sessions")
	return NewRunService(repo, sessionsBase, factory), repo, sessionsBase
}

// makeFrozenSpecWithModel creates a frozen spec whose main model is
// (provName, modelID).  Shares the minimal goal / verification structure
// with makeFrozenSpec (which always uses "mock"/"mock-model").
func makeFrozenSpecWithModel(
	t *testing.T,
	sessionsBase, sessionID, workingDir,
	provName, modelID string,
	maxIter int32,
) {
	t.Helper()
	store := specstore.NewStore(filepath.Join(sessionsBase, sessionID))
	fs := &gilv1.FrozenSpec{
		SpecId:    "test-spec",
		SessionId: sessionID,
		Goal: &gilv1.Goal{
			OneLiner:               "integration probe",
			SuccessCriteriaNatural: []string{"probe.txt exists"},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"bash"}},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{
				{Name: "exists", Kind: gilv1.CheckKind_SHELL, Command: "test -f probe.txt"},
			},
		},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_NATIVE, Path: workingDir},
		Models: &gilv1.ModelConfig{
			Main: &gilv1.ModelChoice{Provider: provName, ModelId: modelID},
		},
		Risk:   &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL},
		Budget: &gilv1.Budget{MaxIterations: maxIter},
	}
	require.NoError(t, store.Save(fs))
	require.NoError(t, store.Freeze())
}

// loadSessionEvents reads the persisted events.jsonl for a session and
// returns all events, asserting the file exists.
func loadSessionEvents(t *testing.T, sessionsBase, sessionID string) []event.Event {
	t.Helper()
	eventsPath := filepath.Join(sessionsBase, sessionID, "events", "events.jsonl")
	require.FileExists(t, eventsPath)
	loaded, err := event.LoadAll(eventsPath)
	require.NoError(t, err)
	return loaded
}

// eventTypes extracts the Type field from each event for use in error messages.
func eventTypes(events []event.Event) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types
}

// requestRecordingProvider wraps a delegate provider, recording every
// provider.Request received by Complete.  Name() returns the configured
// name so the runner's provider-type guards (e.g., the T4 Anthropic check)
// evaluate correctly.
//
// Named requestRecordingProvider (not recordingProvider) to avoid a
// collision with the type of the same name in interview_test.go.
type requestRecordingProvider struct {
	mu       sync.Mutex
	name     string
	delegate provider.Provider
	requests []provider.Request
}

func newRequestRecordingProvider(name string, delegate provider.Provider) *requestRecordingProvider {
	return &requestRecordingProvider{name: name, delegate: delegate}
}

func (r *requestRecordingProvider) Name() string { return r.name }

func (r *requestRecordingProvider) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	// Deep-copy Messages before storing so later loop mutations don't
	// corrupt the recorded snapshot.
	msgs := make([]provider.Message, len(req.Messages))
	copy(msgs, req.Messages)
	snap := provider.Request{
		Model:              req.Model,
		System:             req.System,
		SystemCacheControl: req.SystemCacheControl,
		MaxTokens:          req.MaxTokens,
		Temperature:        req.Temperature,
		Messages:           msgs,
		Tools:              req.Tools,
	}
	r.mu.Lock()
	r.requests = append(r.requests, snap)
	r.mu.Unlock()
	return r.delegate.Complete(ctx, req)
}

// lastRequest returns the most recent recorded request, or a zero-value
// Request if no calls have been made yet.
func (r *requestRecordingProvider) lastRequest() provider.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		return provider.Request{}
	}
	return r.requests[len(r.requests)-1]
}

// ─── Test 1: T3 — Compactor wired (fires via compact_now tool) ───────────

// TestP27_CompactionFiresViaTool verifies that the Compactor wired by
// RunService.executeRun (T3) is not nil and actually fires when the agent
// calls compact_now.  The forced-compaction path in runner.go fires
// regardless of the token count — so we don't need enormous messages.
//
// The compactor skips its internal LLM summary call when the conversation
// has fewer than MinMiddle=8 middle messages, which is always the case in
// this short 3-turn run.  We still get compact_start + compact_done events.
func TestP27_CompactionFiresViaTool(t *testing.T) {
	workDir := t.TempDir()

	mockTurns := []provider.MockTurn{
		{
			// Turn 1: call compact_now → forces compaction at start of next iter.
			Text: "Compacting context.",
			ToolCalls: []provider.ToolCall{
				{ID: "c1", Name: "compact_now", Input: json.RawMessage(`{"reason":"p27-t7-probe"}`)},
			},
			StopReason: "tool_use",
		},
		{
			// Turn 2: write probe.txt to satisfy the verifier.
			Text: "Writing probe.txt.",
			ToolCalls: []provider.ToolCall{
				{ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"probe.txt","content":"ok\n"}`)},
			},
			StopReason: "tool_use",
		},
		{Text: "Done.", StopReason: "end_turn"},
	}

	factory := func(_ string) (provider.Provider, string, error) {
		return provider.NewMockToolProvider(mockTurns), "mock-model", nil
	}

	svc, repo, sessionsBase := newRunSvcWithFactory(t, factory)
	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	// makeFrozenSpec (existing helper) uses provider="mock", model="mock-model".
	makeFrozenSpec(t, sessionsBase, s.ID, workDir)

	resp, err := svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "mock"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Status)

	events := loadSessionEvents(t, sessionsBase, s.ID)

	var sawCompact bool
	for _, e := range events {
		if strings.Contains(strings.ToLower(e.Type), "compact") {
			sawCompact = true
			break
		}
	}
	require.True(t, sawCompact,
		"expected at least one compact_* event proving T3 wiring; got event types: %v",
		eventTypes(events),
	)
}

// ─── Test 2: T5 — per-model context window drives threshold ───────────────

// TestP27_ContextWindowDrivesThreshold verifies that the runner's
// compaction trigger uses provider.ContextTokens(model) — the T5 per-model
// capacity table — when MaxContextTokens is not explicitly set on the loop.
//
// We use AgentLoop directly (not via RunService) because RunService does
// not expose MaxContextTokens and the test needs it to be 0 (unset) to
// exercise the T5 code path.  The loop is otherwise equivalent to what
// RunService builds: it has a Compactor wired in.
//
// Model: "ollama:llama3:8b" → ContextTokens = 8192 → threshold ≈ 7782.
// Each mock turn returns ~3000 chars ≈ 750 tokens (4 chars/token for
// "mock-tool" provider).  After 11 turns the conversation history
// accumulates ~8250 tokens, crossing the threshold.
//
// The compactor uses a separate Mock provider (summaryProv) so it doesn't
// exhaust the main turn queue.  The short summary text ensures saved_tokens
// is recorded.
func TestP27_ContextWindowDrivesThreshold(t *testing.T) {
	// ~3000 chars per turn response → ~750 tokens with 4 chars/token default.
	bigText := strings.Repeat("context-inflation ", 167) // ≈ 3006 chars

	// 12 turns that write unique files (to avoid verifier side-effects).
	// No verifier is needed — we only care about compaction events.
	// The loop will run MaxIterations times; we give it enough turns.
	const numBigTurns = 12
	var mainTurns []provider.MockTurn
	for i := 0; i < numBigTurns; i++ {
		mainTurns = append(mainTurns, provider.MockTurn{
			Text: bigText,
			ToolCalls: []provider.ToolCall{{
				ID:    fmt.Sprintf("n%d", i),
				Name:  "write_file",
				Input: json.RawMessage(fmt.Sprintf(`{"path":"f%d.txt","content":"x\n"}`, i)),
			}},
			StopReason: "tool_use",
		})
	}
	mainTurns = append(mainTurns, provider.MockTurn{Text: "Done.", StopReason: "end_turn"})

	mainProv := provider.NewMockToolProvider(mainTurns)

	// Compactor gets its own provider so summary calls don't race with main turns.
	summaryProv := provider.NewMock([]string{"## Summary\n- context snapshot"})
	comp := &compact.Compactor{
		Provider:  summaryProv,
		Model:     "ollama:llama3:8b",
		HeadKeep:  1,
		TailKeep:  2,
		MinMiddle: 2, // low so compaction actually runs (not skipped) once triggered
		History:   &compact.History{},
	}

	// Minimal FrozenSpec — no verification so the loop runs to MaxIterations.
	spec := &gilv1.FrozenSpec{
		Budget:       &gilv1.Budget{MaxIterations: int32(numBigTurns + 1)},
		Verification: &gilv1.Verification{},
	}
	ver := verify.NewRunner(t.TempDir())

	evStream := event.NewStream()
	sub := evStream.Subscribe(512)

	// Collect events in background.
	var collected []event.Event
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for e := range sub.Events() {
			collected = append(collected, e)
		}
	}()

	loop := &runner.AgentLoop{
		Spec:  spec,
		// MaxContextTokens = 0 → T5 path: derive from provider.ContextTokens(model)
		// Model must be in the capacity table so ContextTokens != 200_000.
		Provider:         mainProv,
		Model:            "ollama:llama3:8b", // 8192 token window
		Compactor:        comp,
		Verifier:         ver,
		Events:           evStream,
	}

	_, err := loop.Run(context.Background())
	require.NoError(t, err)
	sub.Close()
	<-collectorDone

	var sawCompact bool
	for _, e := range collected {
		if e.Type == "compact_start" || e.Type == "compact_done" || e.Type == "compact_error" {
			sawCompact = true
			break
		}
	}

	types := make([]string, len(collected))
	for i, e := range collected {
		types[i] = e.Type
	}
	require.True(t, sawCompact,
		"expected auto-compaction via T5 per-model window (ollama:llama3:8b = 8192); events: %v",
		types,
	)
}

// ─── Test 3: T4 — cache markers wired ─────────────────────────────────────

// TestP27_AnthropicCacheMarkersInRequest verifies that T4's
// compact.MarkCacheBreakpoints fires for Anthropic providers: at least
// one of the last-3 messages in the final provider request should carry
// CacheControl=true.
//
// A requestRecordingProvider that returns Name()="anthropic" is injected so
// the runner's HasPrefix("anthropic") guard fires without a real API key.
// It delegates completions to a MockToolProvider for scripted turns.
//
// IMPORTANT: the "anthropic+retry" detection fix (runner.go T4 gate changed
// from == to strings.HasPrefix) is what makes this test pass.  Before the
// fix, the recording provider's name "anthropic" would have matched the old
// == check too — but the real provider in production is wrapped by
// NewRetry which returns Name()="anthropic+retry", so the fix is real.
// We verify that the RETRY-wrapped recording provider (as the runner sees
// it after RunService applies NewRetry) still produces marked messages.
func TestP27_AnthropicCacheMarkersInRequest(t *testing.T) {
	workDir := t.TempDir()

	// 5 turns: 4 write_file calls (to build message history) + end_turn.
	mockTurns := []provider.MockTurn{
		{Text: "Writing f1.", ToolCalls: []provider.ToolCall{{ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"f1.txt","content":"x"}`)}}, StopReason: "tool_use"},
		{Text: "Writing f2.", ToolCalls: []provider.ToolCall{{ID: "w2", Name: "write_file", Input: json.RawMessage(`{"path":"f2.txt","content":"x"}`)}}, StopReason: "tool_use"},
		{Text: "Writing f3.", ToolCalls: []provider.ToolCall{{ID: "w3", Name: "write_file", Input: json.RawMessage(`{"path":"f3.txt","content":"x"}`)}}, StopReason: "tool_use"},
		{Text: "Writing probe.", ToolCalls: []provider.ToolCall{{ID: "w4", Name: "write_file", Input: json.RawMessage(`{"path":"probe.txt","content":"ok\n"}`)}}, StopReason: "tool_use"},
		{Text: "Done.", StopReason: "end_turn"},
	}

	delegate := provider.NewMockToolProvider(mockTurns)
	// rec.Name() == "anthropic" → runner wraps in NewRetry → "anthropic+retry"
	// → strings.HasPrefix check in runner.go fires → MarkCacheBreakpoints runs.
	rec := newRequestRecordingProvider("anthropic", delegate)

	factory := func(_ string) (provider.Provider, string, error) {
		return rec, "claude-sonnet-4-6", nil
	}

	svc, repo, sessionsBase := newRunSvcWithFactory(t, factory)
	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	// provider="anthropic" so NewCompactorFromSpec finds provsByName["anthropic"].
	makeFrozenSpecWithModel(t, sessionsBase, s.ID, workDir, "anthropic", "claude-sonnet-4-6", 6)

	resp, err := svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "anthropic"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Status)

	// Inspect ALL recorded requests, not just the last one.
	// The last request may be the milestone-gate call (which doesn't go
	// through the cache-marker path); the main-loop iteration requests DO.
	// We assert that at least one request has CacheControl=true on its
	// last-3 messages, proving T4 fired at least once.
	rec.mu.Lock()
	allRequests := make([]provider.Request, len(rec.requests))
	copy(allRequests, rec.requests)
	rec.mu.Unlock()

	require.NotEmpty(t, allRequests,
		"recording provider received no requests — check factory wiring")

	var foundMarked bool
	for _, req := range allRequests {
		n := len(req.Messages)
		if n == 0 {
			continue
		}
		start := n - 3
		if start < 0 {
			start = 0
		}
		for i := start; i < n; i++ {
			if req.Messages[i].CacheControl {
				foundMarked = true
				break
			}
		}
		if foundMarked {
			break
		}
	}
	require.True(t, foundMarked,
		"expected at least one provider request with CacheControl=true on last-3 messages "+
			"(T4 wiring via strings.HasPrefix fix in runner.go); "+
			"total requests: %d. This means MarkCacheBreakpoints never fired.",
		len(allRequests),
	)
}

// Prevents "imported and not used" for time if only some tests compile.
var _ = time.Second
