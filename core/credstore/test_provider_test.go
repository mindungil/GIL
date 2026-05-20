package credstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/provider"
)

// fakeProvider is a stand-in for provider.Provider used by the
// TestProvider tests. It records the request it received so we can
// assert the wizard wired the right model + a tiny token budget, and it
// returns a canned response (or a canned error) without touching a real
// API.
type fakeProvider struct {
	name      string
	gotReq    provider.Request
	respText  string
	respIn    int64
	respOut   int64
	failWith  error
	completed int
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	f.gotReq = req
	f.completed++
	if f.failWith != nil {
		return provider.Response{}, f.failWith
	}
	return provider.Response{
		Text:         f.respText,
		InputTokens:  f.respIn,
		OutputTokens: f.respOut,
		StopReason:   "end_turn",
	}, nil
}

func (f *fakeProvider) StreamComplete(ctx context.Context, req provider.Request, onText func(string)) (provider.Response, error) {
	return f.Complete(ctx, req)
}

// TestTestProvider_HappyPath verifies the wizard's smoke check returns a
// populated TestResult when the underlying provider responds normally.
// We use a fake builder so the test is hermetic.
func TestTestProvider_HappyPath(t *testing.T) {
	fp := &fakeProvider{name: "anthropic", respText: "ok", respIn: 7, respOut: 1}
	restore := SetTestProviderBuilder(func(_ ProviderName, _ Credential) (provider.Provider, string, error) {
		return fp, "claude-haiku-4-5", nil
	})
	t.Cleanup(func() { SetTestProviderBuilder(restore) })

	res, err := TestProvider(context.Background(), Anthropic, Credential{Type: CredAPI, APIKey: "sk-ant-x"})
	if err != nil {
		t.Fatalf("TestProvider: %v", err)
	}
	if !res.OK {
		t.Errorf("expected OK=true, got %+v", res)
	}
	if res.ReplyText != "ok" {
		t.Errorf("ReplyText: got %q want %q", res.ReplyText, "ok")
	}
	if res.Model != "claude-haiku-4-5" {
		t.Errorf("Model: got %q", res.Model)
	}
	if res.InputTokens != 7 || res.OutputTokens != 1 {
		t.Errorf("usage tokens: got in=%d out=%d", res.InputTokens, res.OutputTokens)
	}
	// Sanity: the wizard MUST cap MaxTokens to keep the smoke test cheap.
	if fp.gotReq.MaxTokens == 0 || fp.gotReq.MaxTokens > 16 {
		t.Errorf("MaxTokens should be small (<=16), got %d", fp.gotReq.MaxTokens)
	}
	if fp.completed != 1 {
		t.Errorf("expected 1 Complete call, got %d", fp.completed)
	}
}

// TestTestProvider_BubblesError checks that a provider failure surfaces
// verbatim — the wizard's UI layer is responsible for translating "401
// unauthorized" into a friendly hint, not credstore.
func TestTestProvider_BubblesError(t *testing.T) {
	fp := &fakeProvider{name: "anthropic", failWith: errors.New("401 unauthorized")}
	restore := SetTestProviderBuilder(func(_ ProviderName, _ Credential) (provider.Provider, string, error) {
		return fp, "claude-haiku-4-5", nil
	})
	t.Cleanup(func() { SetTestProviderBuilder(restore) })

	res, err := TestProvider(context.Background(), Anthropic, Credential{Type: CredAPI, APIKey: "bad"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got %q", err.Error())
	}
	if res.OK {
		t.Errorf("expected OK=false on error, got %+v", res)
	}
}

// TestNewProviderForCredential_VLLMRequiresModel asserts the wizard
// cannot move past the "test connection" step for vllm without a model
// — vllm has no canonical default, so we fail closed rather than guess.
func TestNewProviderForCredential_VLLMRequiresModel(t *testing.T) {
	_, _, err := NewProviderForCredential(VLLM, Credential{Type: CredAPI, BaseURL: "http://localhost:8000/v1"})
	if err == nil {
		t.Fatal("expected error for vllm without Model, got nil")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Errorf("expected error to mention model, got %q", err.Error())
	}
}

// TestNewProviderForCredential_DefaultsApplied — when the user picked a
// known provider but the wizard didn't capture a Model (e.g. older
// auth.json predating the field), we fall back to a sane default per
// provider so the smoke test still works.
func TestNewProviderForCredential_DefaultsApplied(t *testing.T) {
	tests := []struct {
		name     ProviderName
		cred     Credential
		wantModel string
	}{
		{Anthropic, Credential{Type: CredAPI, APIKey: "x"}, "claude-haiku-4-5"},
		{OpenAI, Credential{Type: CredAPI, APIKey: "x"}, "gpt-4o-mini"},
		{OpenRouter, Credential{Type: CredAPI, APIKey: "x"}, "anthropic/claude-haiku-4-5"},
	}
	for _, tc := range tests {
		t.Run(string(tc.name), func(t *testing.T) {
			_, model, err := NewProviderForCredential(tc.name, tc.cred)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if model != tc.wantModel {
				t.Errorf("default model: got %q want %q", model, tc.wantModel)
			}
		})
	}
}
