package stuck

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------
// ModelEscalateStrategy tests
// --------------------------------------------------------------------------

func TestModelEscalate_PicksNextInChain(t *testing.T) {
	s := ModelEscalateStrategy{}
	req := ApplyRequest{
		Signal:       Signal{Pattern: PatternMonologue},
		CurrentModel: "a",
		ModelChain:   []string{"a", "b", "c"},
	}
	d, err := s.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Action != ActionSwitchModel {
		t.Errorf("expected ActionSwitchModel, got %v", d.Action)
	}
	if d.NewModel != "b" {
		t.Errorf("expected NewModel=b, got %q", d.NewModel)
	}
}

func TestModelEscalate_PicksNextInChain_Middle(t *testing.T) {
	s := ModelEscalateStrategy{}
	req := ApplyRequest{
		Signal:       Signal{Pattern: PatternRepeatedActionError},
		CurrentModel: "b",
		ModelChain:   []string{"a", "b", "c"},
	}
	d, err := s.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Action != ActionSwitchModel {
		t.Errorf("expected ActionSwitchModel, got %v", d.Action)
	}
	if d.NewModel != "c" {
		t.Errorf("expected NewModel=c, got %q", d.NewModel)
	}
}

func TestModelEscalate_LastInChain_ReturnsErrNoFallback(t *testing.T) {
	s := ModelEscalateStrategy{}
	req := ApplyRequest{
		CurrentModel: "c",
		ModelChain:   []string{"a", "b", "c"},
	}
	_, err := s.Apply(context.Background(), req)
	if !errors.Is(err, ErrNoFallback) {
		t.Errorf("expected ErrNoFallback, got %v", err)
	}
}

func TestModelEscalate_CurrentNotInChain_ReturnsErrNoFallback(t *testing.T) {
	s := ModelEscalateStrategy{}
	req := ApplyRequest{
		CurrentModel: "x",
		ModelChain:   []string{"a", "b"},
	}
	_, err := s.Apply(context.Background(), req)
	if !errors.Is(err, ErrNoFallback) {
		t.Errorf("expected ErrNoFallback, got %v", err)
	}
}

func TestModelEscalate_EmptyChain_ReturnsErrNoFallback(t *testing.T) {
	s := ModelEscalateStrategy{}
	req := ApplyRequest{
		CurrentModel: "a",
		ModelChain:   nil,
	}
	_, err := s.Apply(context.Background(), req)
	if !errors.Is(err, ErrNoFallback) {
		t.Errorf("expected ErrNoFallback, got %v", err)
	}
}

func TestModelEscalate_ExplanationIncludesPattern(t *testing.T) {
	s := ModelEscalateStrategy{}
	req := ApplyRequest{
		Signal:       Signal{Pattern: PatternPingPong},
		CurrentModel: "a",
		ModelChain:   []string{"a", "b"},
	}
	d, err := s.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pattern := PatternPingPong.String()
	if !strings.Contains(d.Explanation, pattern) {
		t.Errorf("expected Explanation to contain %q, got %q", pattern, d.Explanation)
	}
}

// --------------------------------------------------------------------------
// Stub strategies smoke test
// --------------------------------------------------------------------------

func TestStubs_ReturnErrNoFallback(t *testing.T) {
	ctx := context.Background()
	req := ApplyRequest{
		CurrentModel: "a",
		ModelChain:   []string{"a", "b"},
	}

	stubs := []Strategy{
		AltToolOrderStrategy{},
		SubagentBranchStrategy{},
		ResetSectionStrategy{},
		AdversaryConsultStrategy{},
	}

	for _, s := range stubs {
		t.Run(s.Name(), func(t *testing.T) {
			_, err := s.Apply(ctx, req)
			if !errors.Is(err, ErrNoFallback) {
				t.Errorf("expected ErrNoFallback from %s, got %v", s.Name(), err)
			}
		})
	}
}
