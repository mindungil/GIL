package stuck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/checkpoint"
	"github.com/mindungil/gil/core/provider"
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

// (SubagentBranchStrategy is no longer a stub — it has real tests below.)

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

// --------------------------------------------------------------------------
// AdversaryConsultStrategy tests
// --------------------------------------------------------------------------

// fakeProvider returns the configured response for use in adversary tests.
type fakeProvider struct {
	resp provider.Response
	err  error
	seen []provider.Request
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	f.seen = append(f.seen, req)
	return f.resp, f.err
}

func TestAdversaryConsultStrategy_LLMSuggestsStep(t *testing.T) {
	fp := &fakeProvider{resp: provider.Response{Text: "Run git status before retrying.\nThis grounds the next decision."}}
	s := AdversaryConsultStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternRepeatedActionError, Detail: "x", Count: 3},
		Provider:       fp,
		CurrentModel:   "main",
		RecentMessages: []provider.Message{{Role: provider.RoleUser, Content: "do the thing"}},
	})
	require.NoError(t, err)
	require.Equal(t, ActionAdversaryConsult, dec.Action)
	require.Contains(t, dec.Explanation, "Run git status before retrying.")
	require.NotContains(t, dec.Explanation, "This grounds the next decision.", "should take only first line")
	// Sanity: provider was called with adversary system prompt + structured user message
	require.Len(t, fp.seen, 1)
	require.Contains(t, fp.seen[0].System, "adversarial reviewer")
	require.Contains(t, fp.seen[0].Messages[0].Content, "STUCK PATTERN DETECTED")
}

func TestAdversaryConsultStrategy_AdversaryModelOverride(t *testing.T) {
	fp := &fakeProvider{resp: provider.Response{Text: "do the next thing"}}
	s := AdversaryConsultStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternRepeatedActionError, Count: 2},
		Provider:       fp,
		CurrentModel:   "main-model",
		AdversaryModel: "adversary-model",
	})
	require.NoError(t, err)
	require.Equal(t, "adversary-model", fp.seen[0].Model)
}

func TestAdversaryConsultStrategy_AdversaryModelEmptyFallsBackToCurrent(t *testing.T) {
	fp := &fakeProvider{resp: provider.Response{Text: "x"}}
	s := AdversaryConsultStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:       Signal{Pattern: PatternRepeatedActionError, Count: 2},
		Provider:     fp,
		CurrentModel: "main-model",
		// AdversaryModel: ""
	})
	require.NoError(t, err)
	require.Equal(t, "main-model", fp.seen[0].Model)
}

func TestAdversaryConsultStrategy_NoProvider_ErrNoFallback(t *testing.T) {
	s := AdversaryConsultStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:       Signal{Pattern: PatternRepeatedActionError, Count: 2},
		Provider:     nil,
		CurrentModel: "m",
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestAdversaryConsultStrategy_NoModel_ErrNoFallback(t *testing.T) {
	fp := &fakeProvider{resp: provider.Response{Text: "x"}}
	s := AdversaryConsultStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:   Signal{Pattern: PatternRepeatedActionError, Count: 2},
		Provider: fp,
		// no model fields
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestAdversaryConsultStrategy_ContextOverflow_ErrNoFallback(t *testing.T) {
	fp := &fakeProvider{resp: provider.Response{Text: "x"}}
	s := AdversaryConsultStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:       Signal{Pattern: PatternContextWindowError, Count: 2},
		Provider:     fp,
		CurrentModel: "m",
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestAdversaryConsultStrategy_LLMError_PropagatesWrapped(t *testing.T) {
	fp := &fakeProvider{err: errors.New("network down")}
	s := AdversaryConsultStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:       Signal{Pattern: PatternRepeatedActionError, Count: 2},
		Provider:     fp,
		CurrentModel: "m",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "adversary_consult")
	require.Contains(t, err.Error(), "network down")
}

func TestAdversaryConsultStrategy_EmptyResponse_ErrNoFallback(t *testing.T) {
	fp := &fakeProvider{resp: provider.Response{Text: "   \n   "}}
	s := AdversaryConsultStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:       Signal{Pattern: PatternRepeatedActionError, Count: 2},
		Provider:     fp,
		CurrentModel: "m",
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestBuildAdversaryConsultPrompt_TruncatesLongMessages(t *testing.T) {
	long := strings.Repeat("x", 500)
	out := buildAdversaryConsultPrompt(
		Signal{Pattern: PatternRepeatedActionError, Detail: "d", Count: 2},
		[]provider.Message{{Role: provider.RoleUser, Content: long}},
	)
	// Each message capped at 200 chars + ellipsis
	require.Contains(t, out, strings.Repeat("x", 200)+"…")
	require.NotContains(t, out, strings.Repeat("x", 201))
}

func TestBuildAdversaryConsultPrompt_CapsRecentTo10(t *testing.T) {
	msgs := make([]provider.Message, 20)
	for i := range msgs {
		msgs[i] = provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("msg-%d", i)}
	}
	out := buildAdversaryConsultPrompt(Signal{Pattern: PatternRepeatedActionError, Count: 1}, msgs)
	// Should contain msg-19 (last) but NOT msg-9 (older than the last 10)
	require.Contains(t, out, "msg-19")
	require.Contains(t, out, "msg-10")
	require.NotContains(t, out, "msg-9")
}

// --------------------------------------------------------------------------
// SubagentBranchStrategy tests
// --------------------------------------------------------------------------

type fakeSubagentRunner struct {
	summary     string
	err         error
	seenSubgoal string
	seenTools   []string
}

func (f *fakeSubagentRunner) RunSubagent(ctx context.Context, subgoal string, allowedTools []string, maxIters int) (string, error) {
	f.seenSubgoal = subgoal
	f.seenTools = append([]string(nil), allowedTools...)
	return f.summary, f.err
}

func TestSubagentBranchStrategy_ReturnsSubagentSummary(t *testing.T) {
	fr := &fakeSubagentRunner{summary: "the bash command is failing because the file path is wrong"}
	s := SubagentBranchStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternRepeatedActionError, Count: 3},
		SubagentRunner: fr,
	})
	require.NoError(t, err)
	require.Equal(t, ActionSubagentBranch, dec.Action)
	require.Contains(t, dec.Explanation, "the bash command is failing")
	require.Contains(t, fr.seenTools, "read_file")
	require.NotContains(t, fr.seenTools, "bash")
	require.NotContains(t, fr.seenTools, "write_file")
}

func TestSubagentBranchStrategy_NilRunner_ErrNoFallback(t *testing.T) {
	s := SubagentBranchStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal: Signal{Pattern: PatternRepeatedActionError, Count: 3},
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestSubagentBranchStrategy_WrongPattern_ErrNoFallback(t *testing.T) {
	fr := &fakeSubagentRunner{summary: "x"}
	s := SubagentBranchStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternMonologue},
		SubagentRunner: fr,
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestSubagentBranchStrategy_EmptySummary_ErrNoFallback(t *testing.T) {
	fr := &fakeSubagentRunner{summary: ""}
	s := SubagentBranchStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternRepeatedActionError, Count: 3},
		SubagentRunner: fr,
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestSubagentBranchStrategy_RunnerError_PropagatesWrapped(t *testing.T) {
	fr := &fakeSubagentRunner{err: errors.New("boom")}
	s := SubagentBranchStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternRepeatedActionError, Count: 3},
		SubagentRunner: fr,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "subagent_branch")
	require.Contains(t, err.Error(), "boom")
}

func TestSubagentBranchStrategy_PingPong_Fires(t *testing.T) {
	fr := &fakeSubagentRunner{summary: "switch to a different approach"}
	s := SubagentBranchStrategy{}
	dec, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternPingPong, Count: 4},
		SubagentRunner: fr,
	})
	require.NoError(t, err)
	require.Equal(t, ActionSubagentBranch, dec.Action)
	require.Contains(t, dec.Explanation, "SUBAGENT FINDING")
}

func TestSubagentBranchStrategy_ContextWindow_ErrNoFallback(t *testing.T) {
	fr := &fakeSubagentRunner{summary: "something"}
	s := SubagentBranchStrategy{}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternContextWindowError},
		SubagentRunner: fr,
	})
	require.ErrorIs(t, err, ErrNoFallback)
}

func TestSubagentBranchStrategy_DefaultMaxIter(t *testing.T) {
	var capturedMaxIters int
	fr := &captureMaxIterRunner{summary: "finding", captureMaxIters: &capturedMaxIters}
	s := SubagentBranchStrategy{} // MaxIterations=0 → default 5
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternRepeatedActionError, Count: 3},
		SubagentRunner: fr,
	})
	require.NoError(t, err)
	require.Equal(t, 5, capturedMaxIters)
}

func TestSubagentBranchStrategy_CustomMaxIter(t *testing.T) {
	var capturedMaxIters int
	fr := &captureMaxIterRunner{summary: "finding", captureMaxIters: &capturedMaxIters}
	s := SubagentBranchStrategy{MaxIterations: 3}
	_, err := s.Apply(context.Background(), ApplyRequest{
		Signal:         Signal{Pattern: PatternRepeatedActionError, Count: 3},
		SubagentRunner: fr,
	})
	require.NoError(t, err)
	require.Equal(t, 3, capturedMaxIters)
}

type captureMaxIterRunner struct {
	summary         string
	captureMaxIters *int
}

func (c *captureMaxIterRunner) RunSubagent(_ context.Context, _ string, _ []string, maxIters int) (string, error) {
	*c.captureMaxIters = maxIters
	return c.summary, nil
}
