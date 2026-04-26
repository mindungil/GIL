package repomap

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func mkRanked(n int) []ScoredSymbol {
	out := make([]ScoredSymbol, n)
	for i := 0; i < n; i++ {
		out[i] = ScoredSymbol{
			Symbol: Symbol{
				Name: fmt.Sprintf("Sym%d", i),
				Kind: "func",
				File: fmt.Sprintf("file%d.go", i%5),
				Line: i + 1,
			},
			Score: float64(n - i),
		}
	}
	return out
}

func TestEstimateTokens_FourCharsPerToken(t *testing.T) {
	require.Equal(t, 0, EstimateTokens(""))
	require.Equal(t, 1, EstimateTokens("abcd"))
	require.Equal(t, 2, EstimateTokens("abcdefgh"))
}

func TestRender_OutputContainsAllSymbolsAndFiles(t *testing.T) {
	ranked := mkRanked(3)
	out := Render(ranked, 3)
	for _, r := range ranked {
		require.Contains(t, out, r.Symbol.Name)
	}
	require.Contains(t, out, "## file0.go")
}

func TestRender_FileOrderIsFirstAppearance(t *testing.T) {
	// file2 appears first in ranked, then file1, then file2 again
	ranked := []ScoredSymbol{
		{Symbol: Symbol{Name: "A", Kind: "func", File: "file2.go", Line: 1}},
		{Symbol: Symbol{Name: "B", Kind: "func", File: "file1.go", Line: 2}},
		{Symbol: Symbol{Name: "C", Kind: "func", File: "file2.go", Line: 3}},
	}
	out := Render(ranked, 3)
	idx2 := strings.Index(out, "## file2.go")
	idx1 := strings.Index(out, "## file1.go")
	require.Less(t, idx2, idx1, "file2 (first seen) should appear before file1")
}

func TestRender_PrefixBeyondLen_TreatedAsLen(t *testing.T) {
	ranked := mkRanked(2)
	out := Render(ranked, 100)
	require.Contains(t, out, "Sym0")
	require.Contains(t, out, "Sym1")
}

func TestRender_ZeroOrNegative_ReturnsEmpty(t *testing.T) {
	ranked := mkRanked(2)
	require.Equal(t, "", Render(ranked, 0))
	require.Equal(t, "", Render(ranked, -1))
	require.Equal(t, "", Render(nil, 5))
}

func TestRender_RangeLines(t *testing.T) {
	ranked := []ScoredSymbol{
		{Symbol: Symbol{Name: "A", Kind: "struct", File: "x.go", Line: 10, EndLine: 25}},
		{Symbol: Symbol{Name: "B", Kind: "func", File: "x.go", Line: 30}},
	}
	out := Render(ranked, 2)
	require.Contains(t, out, "(line 10-25)")
	require.Contains(t, out, "(line 30)")
}

func TestFit_FitsWithinBudget(t *testing.T) {
	ranked := mkRanked(50)
	out := Fit(ranked, 100)
	require.LessOrEqual(t, EstimateTokens(out), 100)
	require.NotEmpty(t, out)
}

func TestFit_LargerBudget_IncludesMoreSymbols(t *testing.T) {
	ranked := mkRanked(50)
	small := Fit(ranked, 50)
	large := Fit(ranked, 500)
	require.LessOrEqual(t, len(small), len(large))
	// Count "- " bullets to compare symbol counts
	smallCount := strings.Count(small, "\n- ")
	largeCount := strings.Count(large, "\n- ")
	require.Less(t, smallCount, largeCount)
}

func TestFit_AlwaysIncludesHighestRankedFirst(t *testing.T) {
	ranked := mkRanked(20)
	out := Fit(ranked, 10000) // generous budget
	// Sym0 (highest) must appear; check before Sym19
	s0 := strings.Index(out, "Sym0 ")
	s19 := strings.Index(out, "Sym19 ")
	require.Greater(t, s0, -1)
	if s19 > -1 {
		require.Less(t, s0, s19)
	}
}

func TestFit_TinyBudget_ReturnsEmpty(t *testing.T) {
	ranked := mkRanked(5)
	// 1 token = 4 chars, way too small for any rendered output
	out := Fit(ranked, 1)
	require.Equal(t, "", out)
}

func TestFit_EmptyRanked_ReturnsEmpty(t *testing.T) {
	require.Equal(t, "", Fit(nil, 1000))
	require.Equal(t, "", Fit([]ScoredSymbol{}, 1000))
}

func TestFit_ZeroOrNegativeBudget_ReturnsEmpty(t *testing.T) {
	ranked := mkRanked(5)
	require.Equal(t, "", Fit(ranked, 0))
	require.Equal(t, "", Fit(ranked, -100))
}

func TestFit_Monotonicity_NoMoreThanRanked(t *testing.T) {
	ranked := mkRanked(10)
	out := Fit(ranked, 1_000_000) // huge budget
	bullets := strings.Count(out, "\n- ")
	require.LessOrEqual(t, bullets, 10)
}

func TestFit_RealisticProject(t *testing.T) {
	syms, _, err := WalkProject(context.Background(), "testdata/project", WalkOptions{})
	require.NoError(t, err)
	ranked := Rank(syms)
	out := Fit(ranked, 500)
	require.NotEmpty(t, out)
	require.LessOrEqual(t, EstimateTokens(out), 500)
	// Should contain at least one ## file heading
	require.Contains(t, out, "## ")
}
