package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContextTokens_KnownAnthropic(t *testing.T) {
	require.Equal(t, int64(1_000_000), ContextTokens("claude-opus-4-7"))
	require.Equal(t, int64(1_000_000), ContextTokens("claude-opus-4-7[1m]"))
	require.Equal(t, int64(200_000), ContextTokens("claude-sonnet-4-6"))
	require.Equal(t, int64(200_000), ContextTokens("claude-haiku-4-5-20251001"))
}

func TestContextTokens_KnownOpenAI(t *testing.T) {
	require.Equal(t, int64(128_000), ContextTokens("gpt-4o"))
	require.Equal(t, int64(128_000), ContextTokens("gpt-4o-mini"))
	require.Equal(t, int64(400_000), ContextTokens("gpt-5"))
}

func TestContextTokens_KnownGoogle(t *testing.T) {
	require.Equal(t, int64(1_000_000), ContextTokens("gemini-2-pro"))
	require.Equal(t, int64(1_000_000), ContextTokens("gemini-1.5-flash"))
}

func TestContextTokens_KnownOllama(t *testing.T) {
	require.Equal(t, int64(8_192), ContextTokens("ollama:llama3:8b"))
	require.Equal(t, int64(32_768), ContextTokens("ollama:qwen3-coder:32b"))
}

func TestContextTokens_UnknownReturnsConservativeDefault(t *testing.T) {
	require.Equal(t, int64(200_000), ContextTokens("future-model-v9"))
	require.Equal(t, int64(200_000), ContextTokens(""))
}
