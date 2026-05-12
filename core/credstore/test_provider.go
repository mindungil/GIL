package credstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mindungil/gil/core/provider"
)

// TestResult is the outcome of a single small completion against a
// provider, returned by TestProvider. Tokens are 0 when the provider
// did not report usage; ReplyText is the model's verbatim output (often
// just "ok") so the wizard can echo it back to the user as proof of life.
type TestResult struct {
	OK           bool
	Provider     string
	Model        string
	ReplyText    string
	InputTokens  int64
	OutputTokens int64
	Latency      time.Duration
}

// TestProviderBuilder is the pluggable seam used by tests to bypass real
// network calls. The wizard wires the production builder
// (NewProviderForCredential) by default; auth_test.go overrides it with a
// fake that returns a canned response, so we can exercise the wizard's
// "test connection" branch without touching api.anthropic.com.
//
// The builder is global because the wizard is invoked deep in the cobra
// command tree and threading a constructor down would require touching a
// dozen call-sites for a single override point. The override lives in the
// test process's address space only — production binaries always use the
// default.
type TestProviderBuilder func(name ProviderName, cred Credential) (provider.Provider, string, error)

var defaultBuilder TestProviderBuilder = NewProviderForCredential

// SetTestProviderBuilder swaps the builder used by TestProvider. Tests use
// it to inject a mock; pass nil to restore the default. Returns the
// previous builder so the caller can reinstate it in a defer/Cleanup.
func SetTestProviderBuilder(b TestProviderBuilder) TestProviderBuilder {
	prev := defaultBuilder
	if b == nil {
		defaultBuilder = NewProviderForCredential
	} else {
		defaultBuilder = b
	}
	return prev
}

// NewProviderForCredential constructs a real provider.Provider for the
// given credential. Returns the provider, the resolved model id, and any
// error. The model id falls back to a sane default per provider when
// cred.Model is empty so the wizard's "test connection" step works even
// if the user picked "(skip)" on the model step.
//
// This is the mirror of cli/internal/cmd.buildProvider but rooted in
// credstore so the testing path doesn't depend on the CLI package.
func NewProviderForCredential(name ProviderName, cred Credential) (provider.Provider, string, error) {
	switch name {
	case Anthropic:
		if cred.APIKey == "" {
			return nil, "", errors.New("anthropic: api key is empty")
		}
		model := cred.Model
		if model == "" {
			model = "claude-haiku-4-5"
		}
		return provider.NewAnthropic(cred.APIKey), model, nil

	case OpenAI:
		if cred.APIKey == "" {
			return nil, "", errors.New("openai: api key is empty")
		}
		base := cred.BaseURL
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		model := cred.Model
		if model == "" {
			model = "gpt-4o-mini"
		}
		return provider.NewOpenAI(cred.APIKey, base), model, nil

	case OpenRouter:
		if cred.APIKey == "" {
			return nil, "", errors.New("openrouter: api key is empty")
		}
		base := cred.BaseURL
		if base == "" {
			base = "https://openrouter.ai/api/v1"
		}
		model := cred.Model
		if model == "" {
			model = "anthropic/claude-haiku-4-5"
		}
		return provider.NewOpenAI(cred.APIKey, base), model, nil

	case VLLM:
		if cred.BaseURL == "" {
			return nil, "", errors.New("vllm: base url required")
		}
		if cred.Model == "" {
			return nil, "", errors.New("vllm: model required (set via `gil auth login vllm` wizard)")
		}
		// vLLM keys are usually a stub like "local"; an empty key is also
		// fine (some self-hosted endpoints don't enforce auth).
		return provider.NewOpenAI(cred.APIKey, cred.BaseURL), cred.Model, nil
	}
	return nil, "", fmt.Errorf("unknown provider %q", name)
}

// TestProvider sends a minimal completion to the provider implied by name+cred
// and returns the result. The completion is deliberately tiny — "say 'ok'"
// with max_tokens=8 — so the cost is negligible (well under $0.0001 on the
// haiku/gpt-4o-mini class of models) and the latency is bounded.
//
// On success: TestResult.OK is true, ReplyText carries the model's response
// (so the wizard can show "got: ok"), and InputTokens/OutputTokens carry
// any usage the provider reported.
//
// On failure: returns the error verbatim so the wizard's caller can decide
// whether to abort, retry, or continue. We do NOT translate provider-specific
// error codes here — the wizard's UI layer is the right place to humanise
// "401 unauthorized" into "check your API key", because that copy is part
// of the user-facing experience and belongs near the prompt that produced it.
//
// Reference lift: aider/onboarding.py's "try a chat completion to see if the
// key works" pattern; goose/configure's confirm-with-1-token check.
func TestProvider(ctx context.Context, name ProviderName, cred Credential) (TestResult, error) {
	prov, model, err := defaultBuilder(name, cred)
	if err != nil {
		return TestResult{Provider: string(name)}, err
	}
	start := time.Now()
	req := provider.Request{
		Model:     model,
		System:    "Reply with only the word ok. No punctuation, no extra words.",
		MaxTokens: 8,
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "ping"},
		},
	}
	resp, err := prov.Complete(ctx, req)
	latency := time.Since(start)
	if err != nil {
		return TestResult{
			Provider: string(name),
			Model:    model,
			Latency:  latency,
		}, err
	}
	return TestResult{
		OK:           true,
		Provider:     string(name),
		Model:        model,
		ReplyText:    strings.TrimSpace(resp.Text),
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		Latency:      latency,
	}, nil
}
