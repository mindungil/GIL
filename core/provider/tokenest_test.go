package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimateTokens_PerProviderDiffer(t *testing.T) {
	s := "Lorem ipsum dolor sit amet, consectetur adipiscing elit."
	a := EstimateTokens("anthropic", s)
	o := EstimateTokens("openai", s)
	g := EstimateTokens("google", s)
	l := EstimateTokens("ollama", s)
	require.NotEqual(t, a, o, "anthropic ≠ openai estimates")
	require.NotEqual(t, o, g, "openai ≠ google estimates")
	require.NotEqual(t, g, l, "google ≠ ollama estimates")
}

func TestEstimateTokens_AnthropicDenser(t *testing.T) {
	s := "func foo() { return bar(baz, qux) }"
	a := EstimateTokens("anthropic", s)
	o := EstimateTokens("openai", s)
	require.Greater(t, a, o, "anthropic estimate > openai (denser code tokens)")
}

func TestEstimateTokens_UnknownProviderUsesDefault(t *testing.T) {
	s := "hello world hello world hello world hello world"
	e := EstimateTokens("unknown", s)
	require.Equal(t, EstimateTokens("openai", s), e)
}

func TestEstimateTokens_EmptyIsZero(t *testing.T) {
	require.Equal(t, 0, EstimateTokens("openai", ""))
}
