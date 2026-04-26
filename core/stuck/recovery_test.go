package stuck

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jedutools/gil/core/checkpoint"
	"github.com/stretchr/testify/require"
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

// --------------------------------------------------------------------------
// AltToolOrderStrategy tests
// --------------------------------------------------------------------------

func TestAltToolOrderStrategy_RepeatedAction_ReturnsHint(t *testing.T) {
	s := AltToolOrderStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal: Signal{
			Pattern: PatternRepeatedActionObservation,
			Detail:  "tool 'bash' repeated identical action+observation 4 times",
			Count:   4,
		},
	})
	require.NoError(t, err)
	require.Equal(t, ActionAltToolOrder, dec.Action)
	require.Contains(t, dec.Explanation, "STUCK PATTERN DETECTED")
	require.Contains(t, dec.Explanation, "DIFFERENT tool")
	require.Contains(t, dec.Explanation, "4 times")
}

func TestAltToolOrderStrategy_RepeatedError_ReturnsHint(t *testing.T) {
	s := AltToolOrderStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal: Signal{Pattern: PatternRepeatedActionError, Count: 3},
	})
	require.NoError(t, err)
	require.Equal(t, ActionAltToolOrder, dec.Action)
}

func TestAltToolOrderStrategy_PingPong_ReturnsHint(t *testing.T) {
	s := AltToolOrderStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal: Signal{Pattern: PatternPingPong, Count: 6},
	})
	require.NoError(t, err)
	require.Equal(t, ActionAltToolOrder, dec.Action)
}

func TestAltToolOrderStrategy_Monologue_ReturnsErrNoFallback(t *testing.T) {
	s := AltToolOrderStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal: Signal{Pattern: PatternMonologue},
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestAltToolOrderStrategy_ContextWindow_ReturnsErrNoFallback(t *testing.T) {
	s := AltToolOrderStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal: Signal{Pattern: PatternContextWindowError},
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestAltToolOrderStrategy_HintContainsPatternAndDetail(t *testing.T) {
	s := AltToolOrderStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal: Signal{
			Pattern: PatternPingPong,
			Detail:  "alternating between 'read' and 'write' for 6 turns",
			Count:   6,
		},
	})
	require.NoError(t, err)
	require.Contains(t, dec.Explanation, "PingPong")
	require.Contains(t, dec.Explanation, "6 times")
	require.Contains(t, dec.Explanation, "alternating between")
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
		SubagentBranchStrategy{},
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

// --------------------------------------------------------------------------
// ResetSectionStrategy tests
// --------------------------------------------------------------------------

type fakeCheckpointReader struct {
	commits []checkpoint.CommitInfo
	err     error
}

func (f *fakeCheckpointReader) ListCommits(ctx context.Context) ([]checkpoint.CommitInfo, error) {
	return f.commits, f.err
}

func TestResetSectionStrategy_RollsBackToSecondNewest(t *testing.T) {
	cr := &fakeCheckpointReader{commits: []checkpoint.CommitInfo{
		{SHA: "newest_sha_____aaaa", Message: "iter 5"},
		{SHA: "second_sha_____bbbb", Message: "iter 4"},
		{SHA: "older_sha______cccc", Message: "iter 3"},
	}}
	s := ResetSectionStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal:     Signal{Pattern: PatternRepeatedActionError, Count: 3},
		Checkpoint: cr,
	})
	require.NoError(t, err)
	require.Equal(t, ActionResetSection, dec.Action)
	require.Equal(t, "second_sha_____bbbb", dec.RestoreSHA)
	require.Contains(t, dec.Explanation, "iter 4")
}

func TestResetSectionStrategy_RepeatedActionObservation_Works(t *testing.T) {
	cr := &fakeCheckpointReader{commits: []checkpoint.CommitInfo{
		{SHA: "sha_a", Message: "latest"},
		{SHA: "sha_b", Message: "prior"},
	}}
	s := ResetSectionStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal:     Signal{Pattern: PatternRepeatedActionObservation, Count: 4},
		Checkpoint: cr,
	})
	require.NoError(t, err)
	require.Equal(t, ActionResetSection, dec.Action)
	require.Equal(t, "sha_b", dec.RestoreSHA)
}

func TestResetSectionStrategy_NoCheckpoint_ReturnsErrNoFallback(t *testing.T) {
	s := ResetSectionStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal: Signal{Pattern: PatternRepeatedActionError},
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestResetSectionStrategy_LessThanTwoCommits_ReturnsErrNoFallback(t *testing.T) {
	cr := &fakeCheckpointReader{commits: []checkpoint.CommitInfo{
		{SHA: "only_one", Message: "iter 1"},
	}}
	s := ResetSectionStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:     Signal{Pattern: PatternRepeatedActionError},
		Checkpoint: cr,
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestResetSectionStrategy_ZeroCommits_ReturnsErrNoFallback(t *testing.T) {
	cr := &fakeCheckpointReader{commits: []checkpoint.CommitInfo{}}
	s := ResetSectionStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:     Signal{Pattern: PatternRepeatedActionError},
		Checkpoint: cr,
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestResetSectionStrategy_WrongPattern_ReturnsErrNoFallback(t *testing.T) {
	cr := &fakeCheckpointReader{commits: []checkpoint.CommitInfo{
		{SHA: "a"}, {SHA: "b"},
	}}
	s := ResetSectionStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:     Signal{Pattern: PatternMonologue},
		Checkpoint: cr,
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestResetSectionStrategy_PingPong_ReturnsErrNoFallback(t *testing.T) {
	cr := &fakeCheckpointReader{commits: []checkpoint.CommitInfo{
		{SHA: "a"}, {SHA: "b"},
	}}
	s := ResetSectionStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:     Signal{Pattern: PatternPingPong},
		Checkpoint: cr,
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestResetSectionStrategy_ExplanationContainsShortSHAAndPattern(t *testing.T) {
	longSHA := "abcdef1234567890abcdef1234567890abcdef12"
	cr := &fakeCheckpointReader{commits: []checkpoint.CommitInfo{
		{SHA: "newer", Message: "iter 2"},
		{SHA: longSHA, Message: "iter 1"},
	}}
	s := ResetSectionStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal:     Signal{Pattern: PatternRepeatedActionError},
		Checkpoint: cr,
	})
	require.NoError(t, err)
	require.Contains(t, dec.Explanation, longSHA[:12])
	require.Contains(t, dec.Explanation, "RepeatedActionError")
	require.Contains(t, dec.Explanation, "iter 1")
}

func TestResetSectionStrategy_ListCommitsError_ReturnsError(t *testing.T) {
	cr := &fakeCheckpointReader{err: errors.New("git offline")}
	s := ResetSectionStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:     Signal{Pattern: PatternRepeatedActionError},
		Checkpoint: cr,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "git offline")
}
