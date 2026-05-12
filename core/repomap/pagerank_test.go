package repomap_test

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/repomap"
)

func TestRank_HigherScoreForReferencedSymbols(t *testing.T) {
	// popular.go defines Foo, called from 5 other files
	// lonely.go defines Bar, called from 1 file
	syms := []*repomap.FileSymbols{
		{File: "popular.go", Defs: []repomap.Symbol{{Name: "Foo", Kind: "func", File: "popular.go"}}},
		{File: "lonely.go", Defs: []repomap.Symbol{{Name: "Bar", Kind: "func", File: "lonely.go"}}},
		{File: "a.go", Defs: []repomap.Symbol{{Name: "Aa", Kind: "func", File: "a.go"}}, Refs: []repomap.Reference{{Name: "Foo", File: "a.go"}}},
		{File: "b.go", Defs: []repomap.Symbol{{Name: "Bb", Kind: "func", File: "b.go"}}, Refs: []repomap.Reference{{Name: "Foo", File: "b.go"}}},
		{File: "c.go", Defs: []repomap.Symbol{{Name: "Cc", Kind: "func", File: "c.go"}}, Refs: []repomap.Reference{{Name: "Foo", File: "c.go"}}},
		{File: "d.go", Defs: []repomap.Symbol{{Name: "Dd", Kind: "func", File: "d.go"}}, Refs: []repomap.Reference{{Name: "Foo", File: "d.go"}}},
		{File: "e.go", Defs: []repomap.Symbol{{Name: "Ee", Kind: "func", File: "e.go"}}, Refs: []repomap.Reference{{Name: "Foo", File: "e.go"}, {Name: "Bar", File: "e.go"}}},
	}
	ranked := repomap.Rank(syms)
	require.NotEmpty(t, ranked)

	var fooScore, barScore float64
	for _, r := range ranked {
		if r.Symbol.Name == "Foo" {
			fooScore = r.Score
		}
		if r.Symbol.Name == "Bar" {
			barScore = r.Score
		}
	}
	require.Greater(t, fooScore, barScore, "Foo (5 refs) should outrank Bar (1 ref)")
}

func TestRank_SortedDescending(t *testing.T) {
	syms := []*repomap.FileSymbols{
		{File: "a.go", Defs: []repomap.Symbol{{Name: "X", Kind: "func", File: "a.go"}}},
		{File: "b.go", Defs: []repomap.Symbol{{Name: "Y", Kind: "func", File: "b.go"}}},
		{File: "c.go", Defs: []repomap.Symbol{{Name: "Z", Kind: "func", File: "c.go"}}},
	}
	ranked := repomap.Rank(syms)
	for i := 1; i < len(ranked); i++ {
		require.GreaterOrEqual(t, ranked[i-1].Score, ranked[i].Score)
	}
}

func TestRank_StableForSameInput(t *testing.T) {
	syms := []*repomap.FileSymbols{
		{File: "a.go", Defs: []repomap.Symbol{{Name: "X", Kind: "func", File: "a.go"}}},
		{File: "b.go", Defs: []repomap.Symbol{{Name: "Y", Kind: "func", File: "b.go"}}, Refs: []repomap.Reference{{Name: "X", File: "b.go"}}},
	}
	r1 := repomap.Rank(syms)
	r2 := repomap.Rank(syms)
	require.Equal(t, r1, r2)
}

func TestRank_EmptyInput(t *testing.T) {
	require.Nil(t, repomap.Rank(nil))
	require.Nil(t, repomap.Rank([]*repomap.FileSymbols{}))
}

func TestRank_NoRefs_AllScoresEqual(t *testing.T) {
	syms := []*repomap.FileSymbols{
		{File: "a.go", Defs: []repomap.Symbol{{Name: "X", Kind: "func", File: "a.go"}}},
		{File: "b.go", Defs: []repomap.Symbol{{Name: "Y", Kind: "func", File: "b.go"}}},
	}
	ranked := repomap.Rank(syms)
	require.Len(t, ranked, 2)
	require.InDelta(t, ranked[0].Score, ranked[1].Score, 1e-9)
}

func TestRank_InDegreeCount(t *testing.T) {
	syms := []*repomap.FileSymbols{
		{File: "tgt.go", Defs: []repomap.Symbol{{Name: "T", Kind: "func", File: "tgt.go"}}},
		{File: "a.go", Defs: []repomap.Symbol{{Name: "A", Kind: "func", File: "a.go"}}, Refs: []repomap.Reference{{Name: "T", File: "a.go"}}},
		{File: "b.go", Defs: []repomap.Symbol{{Name: "B", Kind: "func", File: "b.go"}}, Refs: []repomap.Reference{{Name: "T", File: "b.go"}, {Name: "T", File: "b.go"}}},
	}
	ranked := repomap.Rank(syms)
	var tDeg int
	for _, r := range ranked {
		if r.Symbol.Name == "T" {
			tDeg = r.InDegree
		}
	}
	// a.go has 1 def + 1 ref to T → 1 edge; b.go has 1 def + 2 refs to T → 2 edges
	require.Equal(t, 3, tDeg)
}

func TestRank_SelfReferenceIgnored(t *testing.T) {
	syms := []*repomap.FileSymbols{
		{
			File: "a.go",
			Defs: []repomap.Symbol{{Name: "A", Kind: "func", File: "a.go"}},
			Refs: []repomap.Reference{{Name: "A", File: "a.go"}}, // A in a.go references A in a.go → self-edge dropped
		},
	}
	ranked := repomap.Rank(syms)
	require.Len(t, ranked, 1)
	require.Equal(t, 0, ranked[0].InDegree)
}

func TestRank_RealisticProject(t *testing.T) {
	// Run repomap.WalkProject on the testdata/project fixture, then Rank
	syms, _, err := repomap.WalkProject(context.Background(), "testdata/project", repomap.WalkOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, syms)
	ranked := repomap.Rank(syms)
	require.NotEmpty(t, ranked)
	// Sanity: scores are positive finite numbers
	for _, r := range ranked {
		require.Greater(t, r.Score, 0.0)
		require.False(t, math.IsNaN(r.Score) || math.IsInf(r.Score, 0))
	}
}
