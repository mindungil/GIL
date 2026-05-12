package provider

// providerCharsPerToken is a coarse heuristic that improves on a
// single global 4-chars-per-token rule by acknowledging that different
// providers' tokenizers encode text at different densities. Values
// reflect rough averages across code-heavy mission content.
//
// Phase 27.5 will replace this for OpenAI with tiktoken-go (offline,
// accurate). Phase 28 will integrate Anthropic and Google count_tokens
// API calls with response caching.
var providerCharsPerToken = map[string]float64{
	"anthropic": 3.5,
	"openai":    4.0,
	"google":    3.8,
	"ollama":    4.5,
}

const defaultCharsPerToken = 4.0

// EstimateTokens returns a coarse token-count estimate for the given
// string under the given provider's tokenizer characteristics. This is
// a heuristic — accurate to ~85% — and exists so the compaction
// trigger fires at a reasonable threshold across providers without
// pulling in heavy tokenizer dependencies in V1.
func EstimateTokens(providerID, s string) int {
	if s == "" {
		return 0
	}
	cpt, ok := providerCharsPerToken[providerID]
	if !ok {
		cpt = defaultCharsPerToken
	}
	return int(float64(len(s))/cpt + 0.5)
}
