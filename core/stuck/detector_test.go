package stuck

import (
	"encoding/json"
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
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Errorf("Pattern(%d).String() = %q, want %q", c.p, got, c.want)
		}
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
