package provider

// modelContextTokens is the seed table of per-model context window
// capacities. Update as new models ship; unknown models fall back to a
// conservative 200k default. The default is chosen so that the
// compaction trigger fires no later than the most common model class
// (Sonnet/Haiku/GPT-4 family at 128-200k).
var modelContextTokens = map[string]int64{
	// Anthropic
	"claude-opus-4-7":           1_000_000,
	"claude-opus-4-7[1m]":       1_000_000,
	"claude-sonnet-4-6":         200_000,
	"claude-haiku-4-5-20251001": 200_000,

	// OpenAI
	"gpt-4o":      128_000,
	"gpt-4o-mini": 128_000,
	"gpt-5":       400_000,

	// Google
	"gemini-2-pro":     1_000_000,
	"gemini-1.5-flash": 1_000_000,

	// Local / Ollama (per-model varies; seed values from common configs)
	"ollama:llama3:8b":         8_192,
	"ollama:qwen3-coder:32b":   32_768,
}

// ContextTokens returns the maximum context window in tokens for the
// given model identifier. Unknown models receive a conservative 200k
// default so the compaction trigger still fires reasonably.
func ContextTokens(model string) int64 {
	if v, ok := modelContextTokens[model]; ok {
		return v
	}
	return 200_000
}
