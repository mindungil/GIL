package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestClarify_NotWired(t *testing.T) {
	c := &Clarify{}
	r, err := c.Run(context.Background(), json.RawMessage(`{"question":"why?"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.IsError {
		t.Fatalf("expected error result when callback unwired, got %+v", r)
	}
	if !strings.Contains(r.Content, "not configured") {
		t.Errorf("missing 'not configured' in: %q", r.Content)
	}
}

func TestClarify_MissingQuestion(t *testing.T) {
	c := &Clarify{
		SessionID: "s1",
		Ask: func(_ context.Context, _ string, _ ClarifyAsk) (ClarifyAnswer, error) {
			t.Fatal("callback should not be called when question missing")
			return ClarifyAnswer{}, nil
		},
	}
	r, _ := c.Run(context.Background(), json.RawMessage(`{}`))
	if !r.IsError || !strings.Contains(r.Content, "question") {
		t.Errorf("expected question-required error, got %+v", r)
	}
}

func TestClarify_PausesAndResumesWithAnswer(t *testing.T) {
	// Channel-based pause/resume sim: the callback blocks on a chan
	// the test fills from a goroutine, mirroring how the server
	// pendingClarification works.
	resume := make(chan string, 1)
	c := &Clarify{
		SessionID: "s1",
		Ask: func(ctx context.Context, sid string, ask ClarifyAsk) (ClarifyAnswer, error) {
			if sid != "s1" {
				t.Errorf("session id mismatch: %s", sid)
			}
			if ask.Question != "are we done?" {
				t.Errorf("question mismatch: %s", ask.Question)
			}
			if ask.Urgency != "high" {
				t.Errorf("urgency mismatch: %s", ask.Urgency)
			}
			if len(ask.Suggestions) != 2 {
				t.Errorf("suggestions count mismatch: %d", len(ask.Suggestions))
			}
			select {
			case ans := <-resume:
				return ClarifyAnswer{Answer: ans}, nil
			case <-ctx.Done():
				return ClarifyAnswer{Cancelled: true}, nil
			case <-time.After(2 * time.Second):
				return ClarifyAnswer{TimedOut: true}, nil
			}
		},
	}
	go func() {
		// Inject the answer asynchronously, like the AnswerClarification RPC does.
		time.Sleep(30 * time.Millisecond)
		resume <- "yes, deploy"
	}()
	r, err := c.Run(context.Background(), json.RawMessage(`{
        "question":"are we done?",
        "context":"verifier passed; user did not pre-approve auto-deploy",
        "suggestions":["yes, deploy","no, hold"],
        "urgency":"high"
    }`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.IsError {
		t.Fatalf("unexpected error: %s", r.Content)
	}
	if !strings.Contains(r.Content, "yes, deploy") {
		t.Errorf("answer not in result content: %q", r.Content)
	}
}

func TestClarify_Timeout(t *testing.T) {
	c := &Clarify{
		SessionID: "s1",
		Ask: func(ctx context.Context, _ string, _ ClarifyAsk) (ClarifyAnswer, error) {
			return ClarifyAnswer{TimedOut: true}, nil
		},
	}
	r, _ := c.Run(context.Background(), json.RawMessage(`{"question":"q?"}`))
	if !r.IsError || !strings.Contains(r.Content, "timed out") {
		t.Errorf("expected timeout error, got %+v", r)
	}
}

func TestClarify_Cancelled(t *testing.T) {
	c := &Clarify{
		SessionID: "s1",
		Ask: func(ctx context.Context, _ string, _ ClarifyAsk) (ClarifyAnswer, error) {
			return ClarifyAnswer{Cancelled: true}, nil
		},
	}
	r, _ := c.Run(context.Background(), json.RawMessage(`{"question":"q?"}`))
	if !r.IsError || !strings.Contains(r.Content, "cancelled") {
		t.Errorf("expected cancelled error, got %+v", r)
	}
}

func TestClarify_CallbackError(t *testing.T) {
	c := &Clarify{
		SessionID: "s1",
		Ask: func(ctx context.Context, _ string, _ ClarifyAsk) (ClarifyAnswer, error) {
			return ClarifyAnswer{}, errors.New("rpc broken")
		},
	}
	r, _ := c.Run(context.Background(), json.RawMessage(`{"question":"q?"}`))
	if !r.IsError || !strings.Contains(r.Content, "rpc broken") {
		t.Errorf("expected callback err propagated, got %+v", r)
	}
}

func TestClarify_TruncatesExcessSuggestions(t *testing.T) {
	var seen ClarifyAsk
	c := &Clarify{
		SessionID: "s1",
		Ask: func(_ context.Context, _ string, ask ClarifyAsk) (ClarifyAnswer, error) {
			seen = ask
			return ClarifyAnswer{Answer: "ok"}, nil
		},
	}
	_, _ = c.Run(context.Background(), json.RawMessage(`{
        "question":"pick",
        "suggestions":["a","b","c","d","e","f"]
    }`))
	if len(seen.Suggestions) != 4 {
		t.Errorf("expected suggestions capped at 4, got %d", len(seen.Suggestions))
	}
}

func TestClarify_NormalizesUrgency(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "normal"},
		{"low", "low"},
		{"normal", "normal"},
		{"high", "high"},
		{"banana", "normal"}, // unknown → normal
	}
	for _, tc := range cases {
		var seen string
		c := &Clarify{
			SessionID: "s1",
			Ask: func(_ context.Context, _ string, ask ClarifyAsk) (ClarifyAnswer, error) {
				seen = ask.Urgency
				return ClarifyAnswer{Answer: "ok"}, nil
			},
		}
		body := `{"question":"q","urgency":"` + tc.in + `"}`
		_, _ = c.Run(context.Background(), json.RawMessage(body))
		if seen != tc.want {
			t.Errorf("urgency %q → got %q, want %q", tc.in, seen, tc.want)
		}
	}
}

func TestClarify_EmptyAnswerIsNotError(t *testing.T) {
	c := &Clarify{
		SessionID: "s1",
		Ask: func(_ context.Context, _ string, _ ClarifyAsk) (ClarifyAnswer, error) {
			return ClarifyAnswer{Answer: ""}, nil
		},
	}
	r, _ := c.Run(context.Background(), json.RawMessage(`{"question":"q?"}`))
	if r.IsError {
		t.Errorf("empty answer should not be tool error: %+v", r)
	}
	if !strings.Contains(r.Content, "empty string") {
		t.Errorf("expected hint about empty answer: %q", r.Content)
	}
}
