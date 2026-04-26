package cost

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// approxEq compares cents-level USD values; price arithmetic over millions
// of tokens accumulates float dust that breaks strict equality.
func approxEq(t *testing.T, want, got float64) {
	t.Helper()
	require.InDelta(t, want, got, 0.0001, "want=%.6f got=%.6f", want, got)
}

func TestCalculator_Estimate_KnownModelsRoundTrip(t *testing.T) {
	c := NewCalculator()

	cases := []struct {
		model string
		usage Usage
		want  float64
	}{
		// 1M in + 1M out for opus-4-7 → $15 + $75 = $90
		{"claude-opus-4-7", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 90.00},
		// 100K in + 50K out for sonnet-4-6 → 0.1 * 3 + 0.05 * 15 = $1.05
		{"claude-sonnet-4-6", Usage{InputTokens: 100_000, OutputTokens: 50_000}, 1.05},
		// haiku-4-5: 1M cached read at 0.10 → $0.10
		{"claude-haiku-4-5", Usage{CachedReadTokens: 1_000_000}, 0.10},
		// gpt-4o-mini tiny call → 1K in + 1K out = 0.00015 + 0.00060 = 0.00075
		{"gpt-4o-mini", Usage{InputTokens: 1000, OutputTokens: 1000}, 0.00075},
	}

	for _, tc := range cases {
		usd, ok := c.Estimate(tc.model, tc.usage)
		require.True(t, ok, "model %q should be known", tc.model)
		approxEq(t, tc.want, usd)
	}
}

func TestCalculator_Estimate_UnknownModel(t *testing.T) {
	c := NewCalculator()
	usd, ok := c.Estimate("imaginary-model-9000", Usage{InputTokens: 1, OutputTokens: 1})
	require.False(t, ok)
	require.Equal(t, 0.0, usd)
}

func TestCalculator_Estimate_ZeroUsage(t *testing.T) {
	c := NewCalculator()
	usd, ok := c.Estimate("claude-opus-4-7", Usage{})
	require.True(t, ok)
	require.Equal(t, 0.0, usd)
}

func TestCalculator_Estimate_CacheFallsBackToInput(t *testing.T) {
	// Synthetic catalog: cached_read omitted → falls back to input_per_m.
	c := &Calculator{Catalog: Catalog{
		"toy-model": ModelPrice{InputPerM: 10, OutputPerM: 20},
	}}
	usd, ok := c.Estimate("toy-model", Usage{CachedReadTokens: 1_000_000})
	require.True(t, ok)
	approxEq(t, 10.00, usd)

	usd, ok = c.Estimate("toy-model", Usage{CacheWriteTokens: 1_000_000})
	require.True(t, ok)
	approxEq(t, 10.00, usd)
}

func TestCalculator_NilSafe(t *testing.T) {
	var c *Calculator
	usd, ok := c.Estimate("claude-opus-4-7", Usage{InputTokens: 1})
	require.False(t, ok)
	require.Equal(t, 0.0, usd)

	c2 := &Calculator{}
	usd, ok = c2.Estimate("claude-opus-4-7", Usage{InputTokens: 1})
	require.False(t, ok)
	require.Equal(t, 0.0, usd)
}

func TestCalculator_Estimate_Finite(t *testing.T) {
	c := NewCalculator()
	usd, ok := c.Estimate("claude-opus-4-7", Usage{InputTokens: 12_000_000, OutputTokens: 3_000_000})
	require.True(t, ok)
	require.False(t, math.IsNaN(usd))
	require.False(t, math.IsInf(usd, 0))
	require.Greater(t, usd, 0.0)
}
