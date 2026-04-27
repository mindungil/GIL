package stuck

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/event"
)

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func ev(typ string, data map[string]any) event.Event {
	b, _ := json.Marshal(data)
	return event.Event{Type: typ, Data: b}
}

func toolCall(name, input string) event.Event {
	return ev("tool_call", map[string]any{"name": name, "input": input})
}

func toolResult(name, content string, isError bool) event.Event {
	return ev("tool_result", map[string]any{"name": name, "content": content, "is_error": isError})
}

func providerResponse(toolCalls int) event.Event {
	return ev("provider_response", map[string]any{"tool_calls": toolCalls, "stop_reason": "tool_use"})
}

func hasPattern(sigs []Signal, p Pattern) bool {
	for _, s := range sigs {
		if s.Pattern == p {
			return true
		}
	}
	return false
}

// --------------------------------------------------------------------------
// PatternRepeatedActionObservation
// --------------------------------------------------------------------------

func TestDetector_RepeatedActionObservation_Fires(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 0; i < 4; i++ {
		events = append(events, toolCall("bash", "echo hi"))
		events = append(events, toolResult("bash", "hi", false))
	}
	sigs := d.Check(events)
	if !hasPattern(sigs, PatternRepeatedActionObservation) {
		t.Fatalf("expected RepeatedActionObservation, got %v", sigs)
	}
	for _, s := range sigs {
		if s.Pattern == PatternRepeatedActionObservation {
			if s.Count != 4 {
				t.Errorf("expected Count=4, got %d", s.Count)
			}
		}
	}
}

func TestDetector_RepeatedActionObservation_Threshold(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 0; i < 3; i++ {
		events = append(events, toolCall("bash", "echo hi"))
		events = append(events, toolResult("bash", "hi", false))
	}
	sigs := d.Check(events)
	if hasPattern(sigs, PatternRepeatedActionObservation) {
		t.Fatalf("expected no RepeatedActionObservation for 3 pairs, got %v", sigs)
	}
}

// --------------------------------------------------------------------------
// PatternRepeatedActionError
// --------------------------------------------------------------------------

func TestDetector_RepeatedActionError_Fires(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 0; i < 3; i++ {
		events = append(events, toolCall("bash", "bad command"))
		events = append(events, toolResult("bash", "error: not found", true))
	}
	sigs := d.Check(events)
	if !hasPattern(sigs, PatternRepeatedActionError) {
		t.Fatalf("expected RepeatedActionError, got %v", sigs)
	}
}

func TestDetector_RepeatedActionError_Threshold(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 0; i < 2; i++ {
		events = append(events, toolCall("bash", "bad command"))
		events = append(events, toolResult("bash", "error: not found", true))
	}
	sigs := d.Check(events)
	if hasPattern(sigs, PatternRepeatedActionError) {
		t.Fatalf("expected no RepeatedActionError for 2 pairs, got %v", sigs)
	}
}

// --------------------------------------------------------------------------
// PatternMonologue
// --------------------------------------------------------------------------

func TestDetector_Monologue_Fires(t *testing.T) {
	d := &Detector{}
	events := []event.Event{
		providerResponse(0),
		providerResponse(0),
		providerResponse(0),
	}
	sigs := d.Check(events)
	if !hasPattern(sigs, PatternMonologue) {
		t.Fatalf("expected Monologue, got %v", sigs)
	}
}

func TestDetector_Monologue_BrokenByToolResult(t *testing.T) {
	d := &Detector{}
	events := []event.Event{
		providerResponse(0),
		providerResponse(0),
		toolResult("bash", "ok", false), // resets counter
		providerResponse(0),
	}
	sigs := d.Check(events)
	if hasPattern(sigs, PatternMonologue) {
		t.Fatalf("expected no Monologue when broken by tool_result, got %v", sigs)
	}
}

// --------------------------------------------------------------------------
// PatternPingPong
// --------------------------------------------------------------------------

func TestDetector_PingPong_Fires(t *testing.T) {
	d := &Detector{}
	// ABABAB — 6 tool calls, strictly alternating
	events := []event.Event{
		toolCall("read", "/a"),
		toolCall("write", "/b"),
		toolCall("read", "/a"),
		toolCall("write", "/b"),
		toolCall("read", "/a"),
		toolCall("write", "/b"),
	}
	sigs := d.Check(events)
	if !hasPattern(sigs, PatternPingPong) {
		t.Fatalf("expected PingPong, got %v", sigs)
	}
}

func TestDetector_PingPong_NotConsistentAlternation(t *testing.T) {
	d := &Detector{}
	// A,B,A,A,B,A — breaks alternation at index 3
	events := []event.Event{
		toolCall("read", "/a"),
		toolCall("write", "/b"),
		toolCall("read", "/a"),
		toolCall("read", "/a"),  // same as previous, not alternating
		toolCall("write", "/b"),
		toolCall("read", "/a"),
	}
	sigs := d.Check(events)
	if hasPattern(sigs, PatternPingPong) {
		t.Fatalf("expected no PingPong for non-consistent alternation, got %v", sigs)
	}
}

// --------------------------------------------------------------------------
// PatternContextWindowError
// --------------------------------------------------------------------------

func TestDetector_ContextWindow_Fires(t *testing.T) {
	d := &Detector{}
	events := []event.Event{
		ev("run_error", map[string]any{"err": "context window exceeded"}),
		ev("run_error", map[string]any{"err": "context window exceeded"}),
	}
	sigs := d.Check(events)
	if !hasPattern(sigs, PatternContextWindowError) {
		t.Fatalf("expected ContextWindowError, got %v", sigs)
	}
}

func TestDetector_ContextWindow_Threshold(t *testing.T) {
	d := &Detector{}
	events := []event.Event{
		ev("run_error", map[string]any{"err": "context window exceeded"}),
	}
	sigs := d.Check(events)
	if hasPattern(sigs, PatternContextWindowError) {
		t.Fatalf("expected no ContextWindowError for count=1, got %v", sigs)
	}
}

// --------------------------------------------------------------------------
// Healthy run — no signals
// --------------------------------------------------------------------------

func TestDetector_NoSignals_OnHealthyRun(t *testing.T) {
	d := &Detector{}
	events := []event.Event{
		ev("iteration_start", map[string]any{"iter": 1}),
		ev("provider_request", map[string]any{"iteration": 1, "messages": 3}),
		ev("provider_response", map[string]any{"iteration": 1, "stop_reason": "tool_use", "tool_calls": 1}),
		toolCall("bash", "ls -la"),
		toolResult("bash", "total 8\ndrwxr-xr-x ...", false),
		ev("verify_run", nil),
		ev("verify_result", map[string]any{"name": "test", "passed": true, "exit_code": 0}),
		ev("iteration_start", map[string]any{"iter": 2}),
		ev("provider_request", map[string]any{"iteration": 2, "messages": 5}),
		ev("provider_response", map[string]any{"iteration": 2, "stop_reason": "end_turn", "tool_calls": 0}),
	}
	sigs := d.Check(events)
	if len(sigs) != 0 {
		t.Fatalf("expected no signals on healthy run, got %v", sigs)
	}
}

// --------------------------------------------------------------------------
// Window truncation
// --------------------------------------------------------------------------

func TestDetector_Window_Truncation(t *testing.T) {
	d := &Detector{Window: 20}

	// First 80 events: 4 identical pairs that would fire RepeatedActionObservation
	// but they fall outside the window.
	var events []event.Event
	for i := 0; i < 4; i++ {
		events = append(events, toolCall("bash", "outside"))
		events = append(events, toolResult("bash", "out", false))
	}
	// Pad to push those 8 events outside the window of 20.
	for i := 0; i < 72; i++ {
		events = append(events, ev("iteration_start", map[string]any{"iter": i}))
	}
	// Total: 80 events; window=20 → only last 20 (all iteration_start) are inspected.
	sigs := d.Check(events)
	if hasPattern(sigs, PatternRepeatedActionObservation) {
		t.Fatalf("expected no signal: identical pairs should be outside the window, got %v", sigs)
	}
}

// --------------------------------------------------------------------------
// Pattern.String()
// --------------------------------------------------------------------------

func TestPattern_String(t *testing.T) {
	cases := []struct {
		p    Pattern
		want string
	}{
		{PatternUnknown, "Unknown"},
		{PatternRepeatedActionObservation, "RepeatedActionObservation"},
		{PatternRepeatedActionError, "RepeatedActionError"},
		{PatternMonologue, "Monologue"},
		{PatternPingPong, "PingPong"},
		{PatternContextWindowError, "ContextWindowError"},
		{PatternNoProgress, "NoProgress"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Errorf("Pattern(%d).String() = %q, want %q", c.p, got, c.want)
		}
	}
}

// --------------------------------------------------------------------------
// PatternNoProgress
// --------------------------------------------------------------------------

// iterStart is a small helper for NoProgress tests that emits an
// iteration_start event with the given iter number.
func iterStart(iter int) event.Event {
	return ev("iteration_start", map[string]any{"iter": iter})
}

// verifyRun emits a verify_run event followed by N verify_result events
// where the first `passing` results have passed=true and the rest are
// failed. The detector reads the boundary as: verify_run starts a verify
// span, and any verify_result before the next non-verify event counts.
func verifyRun(passing, total int) []event.Event {
	out := []event.Event{ev("verify_run", nil)}
	for i := 0; i < total; i++ {
		out = append(out, ev("verify_result", map[string]any{
			"name":   "check" + itoa(i),
			"passed": i < passing,
		}))
	}
	return out
}

// TestDetector_NoProgress_Fires_NoFilesModified covers the canonical Run 8
// scenario: 4 iterations, identical verifier passing count, zero successful
// edits — the pattern fires.
func TestDetector_NoProgress_Fires_NoFilesModified(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 1; i <= 4; i++ {
		events = append(events, iterStart(i))
		// Vary the actions: read_file with different paths each iter so
		// existing patterns (RepeatedAction*, PingPong) don't fire.
		events = append(events, toolCall("read_file", `{"path":"a`+itoa(i)+`.txt"}`))
		events = append(events, toolResult("read_file", "ok", false))
		events = append(events, verifyRun(0, 2)...)
	}
	sigs := d.Check(events)
	if !hasPattern(sigs, PatternNoProgress) {
		t.Fatalf("expected NoProgress, got %v", sigs)
	}
}

// TestDetector_NoProgress_Fires_FileChurn covers the second trigger arm:
// same file edited K times across the window with no verifier improvement.
func TestDetector_NoProgress_Fires_FileChurn(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 1; i <= 4; i++ {
		events = append(events, iterStart(i))
		events = append(events, toolCall("write_file", `{"path":"main.go","content":"v`+itoa(i)+`"}`))
		events = append(events, toolResult("write_file", "wrote main.go", false))
		events = append(events, verifyRun(0, 2)...)
	}
	sigs := d.Check(events)
	if !hasPattern(sigs, PatternNoProgress) {
		t.Fatalf("expected NoProgress (churn), got %v", sigs)
	}
	for _, s := range sigs {
		if s.Pattern == PatternNoProgress {
			if !strings.Contains(s.Detail, "main.go") {
				t.Errorf("expected detail to mention main.go, got %q", s.Detail)
			}
			if s.Count != 4 {
				t.Errorf("expected Count=4, got %d", s.Count)
			}
		}
	}
}

// TestDetector_NoProgress_DoesNotFire_VerifyImproving asserts that strict
// improvement in passing-check count suppresses the signal even when the
// other conditions would match.
func TestDetector_NoProgress_DoesNotFire_VerifyImproving(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 1; i <= 4; i++ {
		events = append(events, iterStart(i))
		events = append(events, toolCall("write_file", `{"path":"x.go","content":"v"}`))
		events = append(events, toolResult("write_file", "wrote x.go", false))
		// Improving by one each iter: 0,1,2,3 out of 5
		events = append(events, verifyRun(i-1, 5)...)
	}
	sigs := d.Check(events)
	if hasPattern(sigs, PatternNoProgress) {
		t.Fatalf("expected NO NoProgress when verify is improving, got %v", sigs)
	}
}

// TestDetector_NoProgress_Abstains_NoVerifyEvents asserts that the
// detector keeps quiet when no verify_run has fired at all in the window.
// This is the early-iter safety: we cannot claim "no progress" without a
// progress signal to compare.
func TestDetector_NoProgress_Abstains_NoVerifyEvents(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 1; i <= 4; i++ {
		events = append(events, iterStart(i))
		// Vary the actions and DO modify a file each iter; no verify_run.
		events = append(events, toolCall("write_file", `{"path":"y.go","content":"v"}`))
		events = append(events, toolResult("write_file", "wrote y.go", false))
	}
	sigs := d.Check(events)
	if hasPattern(sigs, PatternNoProgress) {
		t.Fatalf("expected NO NoProgress when no verify events but files ARE modified, got %v", sigs)
	}
}

// TestDetector_NoProgress_Fires_NoVerifyAndNoEdits — Phase 22.B fallback.
// Self-dogfood Run 10 showed agents seldom call verify per-iter, so the
// original "abstain when no verify_run" rule was too strict. New rule:
// if K iters have NO successful edits/writes AND no verify_run, that's an
// even stronger stuck signal (agent is iterating but neither verifying
// nor modifying anything).
func TestDetector_NoProgress_Fires_NoVerifyAndNoEdits(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 1; i <= 4; i++ {
		events = append(events, iterStart(i))
		// Read-only tool calls — no edits, no verify.
		events = append(events, toolCall("read_file", `{"path":"a.go"}`))
		events = append(events, toolResult("read_file", "<content>", false))
		events = append(events, toolCall("bash", `{"command":"ls"}`))
		events = append(events, toolResult("bash", "out", false))
	}
	sigs := d.Check(events)
	if !hasPattern(sigs, PatternNoProgress) {
		t.Fatalf("expected NoProgress fire when no verify AND no edits over 4 iters, got %v", sigs)
	}
	// Detail should mention the verify-independent path.
	for _, s := range sigs {
		if s.Pattern == PatternNoProgress {
			if s.Detail == "" || !contains(s.Detail, "no verify run") {
				t.Errorf("expected detail to mention verify-independent path, got %q", s.Detail)
			}
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || (len(s) > len(sub) && (s[:len(sub)] == sub || contains(s[1:], sub))))
}

// TestDetector_NoProgress_BelowThreshold asserts 3 iters (< default 4)
// does not fire even when all other conditions hold.
func TestDetector_NoProgress_BelowThreshold(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 1; i <= 3; i++ {
		events = append(events, iterStart(i))
		events = append(events, toolCall("write_file", `{"path":"z.go","content":"v"}`))
		events = append(events, toolResult("write_file", "wrote z.go", false))
		events = append(events, verifyRun(0, 2)...)
	}
	sigs := d.Check(events)
	if hasPattern(sigs, PatternNoProgress) {
		t.Fatalf("expected NO NoProgress for 3 iters (below threshold), got %v", sigs)
	}
}

// TestDetector_NoProgress_CustomThreshold verifies the configurable knob.
func TestDetector_NoProgress_CustomThreshold(t *testing.T) {
	// With Threshold=6, four iters should NOT fire, but six should.
	d := &Detector{NoProgressThreshold: 6, Window: 200}
	var events []event.Event
	for i := 1; i <= 4; i++ {
		events = append(events, iterStart(i))
		events = append(events, toolCall("read_file", `{"path":"a`+itoa(i)+`.txt"}`))
		events = append(events, toolResult("read_file", "ok", false))
		events = append(events, verifyRun(0, 2)...)
	}
	if hasPattern(d.Check(events), PatternNoProgress) {
		t.Fatalf("expected no fire with Threshold=6 and 4 iters")
	}
	for i := 5; i <= 6; i++ {
		events = append(events, iterStart(i))
		events = append(events, toolCall("read_file", `{"path":"a`+itoa(i)+`.txt"}`))
		events = append(events, toolResult("read_file", "ok", false))
		events = append(events, verifyRun(0, 2)...)
	}
	if !hasPattern(d.Check(events), PatternNoProgress) {
		t.Fatalf("expected NoProgress with Threshold=6 and 6 iters, got %v", d.Check(events))
	}
}

// TestDetector_NoProgress_Run8Reproduction reproduces the self-dogfood
// Run 8 scenario: agent makes varied tool calls (read_file → bash →
// permission-denied delete → another bash → ...) across 12 iterations,
// verifier never improves (impossible task). Ensures NoProgress fires
// well before iteration 12.
func TestDetector_NoProgress_Run8Reproduction(t *testing.T) {
	d := &Detector{Window: 500}
	// Build a window with 12 iters of varied futile actions. Every iter
	// emits a verify_run with the same passing count (0/3) to simulate
	// "agent thinks it's done after each turn but verifier never passes
	// any check".
	varied := []func(int) []event.Event{
		func(i int) []event.Event {
			return []event.Event{
				toolCall("read_file", `{"path":"file`+itoa(i)+`.txt"}`),
				toolResult("read_file", "contents", false),
			}
		},
		func(i int) []event.Event {
			return []event.Event{
				toolCall("bash", `{"command":"ls -la /tmp/`+itoa(i)+`"}`),
				toolResult("bash", "ok", false),
			}
		},
		func(i int) []event.Event {
			return []event.Event{
				toolCall("bash", `{"command":"rm /etc/passwd-`+itoa(i)+`"}`),
				toolResult("bash", "permission denied", true),
			}
		},
		func(i int) []event.Event {
			return []event.Event{
				toolCall("bash", `{"command":"go build ./internal/`+itoa(i)+`"}`),
				toolResult("bash", "build failed", true),
			}
		},
	}

	var events []event.Event
	for i := 1; i <= 12; i++ {
		events = append(events, iterStart(i))
		events = append(events, varied[i%len(varied)](i)...)
		// verify never improves — 0 out of 3 passing every iter
		events = append(events, verifyRun(0, 3)...)
	}

	sigs := d.Check(events)
	if !hasPattern(sigs, PatternNoProgress) {
		t.Fatalf("expected NoProgress on Run 8 reproduction, got %v", sigs)
	}
	// Sanity: existing patterns should NOT fire (varied actions, no churn,
	// no monologue, no ping-pong).
	for _, s := range sigs {
		switch s.Pattern {
		case PatternRepeatedActionObservation, PatternPingPong:
			t.Errorf("Run 8 should not trigger %s (varied actions), got: %v", s.Pattern, s)
		}
	}
}

// TestDetector_NoProgress_FailedEditsDontCount asserts that an edit which
// returned is_error=true does not contribute to the files-modified set.
// The agent attempting and failing the same edit looks like RepeatedActionError;
// NoProgress should focus on "real" successful changes.
func TestDetector_NoProgress_FailedEditsDontCount(t *testing.T) {
	d := &Detector{}
	var events []event.Event
	for i := 1; i <= 4; i++ {
		events = append(events, iterStart(i))
		// Failed edit each iter — different paths so RepeatedActionError doesn't fire.
		events = append(events, toolCall("write_file", `{"path":"f`+itoa(i)+`.go","content":"v"}`))
		events = append(events, toolResult("write_file", "permission denied", true))
		events = append(events, verifyRun(0, 2)...)
	}
	sigs := d.Check(events)
	// All edits failed → files set is empty → NoProgress fires (the
	// "varied futile actions" case).
	if !hasPattern(sigs, PatternNoProgress) {
		t.Fatalf("expected NoProgress with failed edits (empty files set), got %v", sigs)
	}
}

// --------------------------------------------------------------------------
// Malformed data safety
// --------------------------------------------------------------------------

func TestDetector_MalformedData_NoPanic(t *testing.T) {
	d := &Detector{}
	events := []event.Event{
		{Type: "tool_call", Data: []byte("not json")},
		{Type: "tool_result", Data: []byte("{broken")},
		{Type: "provider_response", Data: nil},
		{Type: "run_error", Data: []byte(`{"err": 42}`)}, // wrong type for err
	}
	// Should not panic and should produce no signals.
	_ = d.Check(events)
}
