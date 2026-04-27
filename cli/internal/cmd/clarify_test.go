package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mindungil/gil/core/event"
)

// writeFakeEvents writes the given events as JSONL into the standard
// session events dir resolved from defaultBase(). Returns the file
// path so the test can clean up. Used by clarify_test instead of
// spinning up gild.
func writeFakeEvents(t *testing.T, sessionID string, events []event.Event) string {
	t.Helper()
	dir := filepath.Join(defaultBase(), "sessions", sessionID, "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := event.NewPersister(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	for _, e := range events {
		if err := p.Write(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Sync(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "events.jsonl")
}

func TestLoadPendingClarifications_None(t *testing.T) {
	// Force defaultBase to a clean tempdir.
	tmp := t.TempDir()
	t.Setenv("GIL_HOME", tmp)
	pendings, err := loadPendingClarifications("does-not-exist")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(pendings) != 0 {
		t.Errorf("expected 0 pendings, got %d", len(pendings))
	}
}

func TestLoadPendingClarifications_OnePending(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("GIL_HOME", tmp)

	body, _ := json.Marshal(map[string]any{
		"ask_id":      "ask-001",
		"question":    "Deploy now?",
		"context":     "verifier passed",
		"suggestions": []string{"yes", "no"},
		"urgency":     "high",
	})
	writeFakeEvents(t, "sess1", []event.Event{
		{
			Timestamp: time.Now().UTC(),
			Source:    event.SourceAgent,
			Kind:      event.KindAction,
			Type:      "clarify_requested",
			Data:      body,
		},
	})

	pendings, err := loadPendingClarifications("sess1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(pendings) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pendings))
	}
	p := pendings[0]
	if p.AskID != "ask-001" {
		t.Errorf("askID: %q", p.AskID)
	}
	if p.Question != "Deploy now?" {
		t.Errorf("question: %q", p.Question)
	}
	if len(p.Suggestions) != 2 {
		t.Errorf("suggestions: %d", len(p.Suggestions))
	}
	if p.Urgency != "high" {
		t.Errorf("urgency: %q", p.Urgency)
	}
}

func TestLoadPendingClarifications_Answered(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("GIL_HOME", tmp)

	requested, _ := json.Marshal(map[string]any{"ask_id": "ask-002", "question": "Q?"})
	answered, _ := json.Marshal(map[string]any{"ask_id": "ask-002"})
	writeFakeEvents(t, "sess2", []event.Event{
		{Timestamp: time.Now().UTC(), Source: event.SourceAgent, Kind: event.KindAction, Type: "clarify_requested", Data: requested},
		{Timestamp: time.Now().UTC(), Source: event.SourceUser, Kind: event.KindObservation, Type: "clarify_answered", Data: answered},
	})

	pendings, err := loadPendingClarifications("sess2")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(pendings) != 0 {
		t.Errorf("expected answered ask to drop out, got %d pendings", len(pendings))
	}
}

func TestRenderPendingClarifications(t *testing.T) {
	var buf bytes.Buffer
	err := renderPendingClarifications(&buf, []pendingClarification{
		{AskID: "a1", Question: "Q1", Suggestions: []string{"x", "y"}, Urgency: "high"},
		{AskID: "a2", Question: "Q2", Urgency: ""},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ask=a1") || !strings.Contains(out, "ask=a2") {
		t.Errorf("missing askIDs: %q", out)
	}
	if !strings.Contains(out, "urgency=high") {
		t.Errorf("missing urgency: %q", out)
	}
	if !strings.Contains(out, "urgency=normal") {
		t.Errorf("missing default urgency: %q", out)
	}
	if !strings.Contains(out, "[1] x") {
		t.Errorf("suggestion render missing: %q", out)
	}
}

func TestRenderClarifyPrompt(t *testing.T) {
	var buf bytes.Buffer
	err := renderClarifyPrompt(&buf, pendingClarification{
		AskID:       "a1",
		Question:    "Q?",
		Context:     "ctx",
		Suggestions: []string{"yes", "no"},
		Urgency:     "high",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"a1", "Q?", "ctx", "[1] yes", "[2] no", "answer>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in: %q", want, out)
		}
	}
}

func TestPickSuggestionByPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[1] yes", "yes"},
		{"[2] no thanks", "no thanks"},
		{"plain", "plain"},
		{"  [3] foo  ", "foo"},
	}
	for _, c := range cases {
		got := pickSuggestionByPrefix(c.in)
		if got != c.want {
			t.Errorf("pickSuggestionByPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderPendingClarifications(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no pending") {
		t.Errorf("empty render: %q", buf.String())
	}
}
