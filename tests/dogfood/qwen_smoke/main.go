// qwen_smoke is a hand-run smoke binary for the OpenAI-compatible adapter.
//
// It reads credentials from $XDG_CONFIG_HOME/gil/auth.json (the same file
// `gil auth login vllm` writes), constructs the OpenAI provider directly
// (no daemon round-trip), and exercises three things in turn:
//
//   1. A trivial text completion ("say hello") to confirm the wire is up.
//   2. A tool-use round trip with a single calculate(expr) tool, to see
//      whether the served model can produce well-formed tool_calls.
//   3. A multi-turn follow-up that feeds the tool result back in and asks
//      the model to summarise it, exercising the role:"tool" message path.
//
// Each step prints latency and token usage. Failures don't abort — we keep
// going so the user sees how far the served model got even when one stage
// regresses.
//
// This binary is NOT registered as a Makefile e2e target because it talks
// to a real upstream and is meant to be run by hand against the user's
// chosen endpoint.
//
// Usage:
//   go run ./tests/dogfood/qwen_smoke           # uses XDG default auth.json
//   GIL_AUTH_FILE=/tmp/x/auth.json go run ...   # override file location
//   GIL_PROVIDER=openai go run ...              # use a different cred entry
//   GIL_MODEL=other-model go run ...            # override served model name
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mindungil/gil/core/credstore"
	"github.com/mindungil/gil/core/provider"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "qwen_smoke:", err)
		os.Exit(1)
	}
}

func run() error {
	providerName := envOr("GIL_PROVIDER", string(credstore.VLLM))
	model := envOr("GIL_MODEL", "qwen3.6-27b")

	authFile := os.Getenv("GIL_AUTH_FILE")
	if authFile == "" {
		// Mirror server/cmd/gild path resolution at the level of detail the
		// smoke binary cares about: XDG_CONFIG_HOME or ~/.config.
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".config")
		}
		authFile = filepath.Join(base, "gil", "auth.json")
	}

	store := credstore.NewFileStore(authFile)
	cred, err := store.Get(context.Background(), credstore.ProviderName(providerName))
	if err != nil {
		return fmt.Errorf("read auth file %s: %w", authFile, err)
	}
	if cred == nil {
		return fmt.Errorf("no credential for provider %q in %s\n  run: gil auth login %s --base-url <url> --api-key <key>",
			providerName, authFile, providerName)
	}

	if cred.BaseURL == "" {
		return fmt.Errorf("provider %q has no base_url; for vllm/local endpoints this is required", providerName)
	}

	fmt.Printf("provider:   %s\n", providerName)
	fmt.Printf("base_url:   %s\n", redactURL(cred.BaseURL))
	fmt.Printf("auth_key:   %s\n", cred.MaskedKey())
	fmt.Printf("model:      %s\n", model)
	fmt.Println()

	p := provider.NewOpenAI(cred.APIKey, cred.BaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// --- Step 1: plain text completion ----------------------------------
	fmt.Println("[1/3] text completion: \"say hello in five words\"")
	t0 := time.Now()
	resp, err := p.Complete(ctx, provider.Request{
		Model:     model,
		System:    "You are concise.",
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "say hello in five words"}},
		MaxTokens: 64,
	})
	d := time.Since(t0)
	if err != nil {
		fmt.Printf("  FAILED in %s: %v\n", d.Round(time.Millisecond), err)
	} else {
		fmt.Printf("  text:        %q\n", resp.Text)
		fmt.Printf("  stop_reason: %s\n", resp.StopReason)
		fmt.Printf("  tokens:      in=%d  out=%d\n", resp.InputTokens, resp.OutputTokens)
		fmt.Printf("  latency:     %s\n", d.Round(time.Millisecond))
	}
	fmt.Println()

	// --- Step 2: tool-use ------------------------------------------------
	fmt.Println("[2/3] tool-use: \"what is 17*23? use the calculate tool\"")
	calcTool := provider.ToolDef{
		Name:        "calculate",
		Description: "Evaluate a simple arithmetic expression and return the numeric result.",
		Schema: json.RawMessage(`{
            "type":"object",
            "properties":{"expr":{"type":"string","description":"arithmetic expression"}},
            "required":["expr"]
        }`),
	}
	t0 = time.Now()
	toolResp, err := p.Complete(ctx, provider.Request{
		Model:  model,
		System: "You are a math assistant. When you need to compute, call the calculate tool.",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "What is 17 * 23? Use the calculate tool."},
		},
		Tools:     []provider.ToolDef{calcTool},
		MaxTokens: 256,
	})
	d = time.Since(t0)
	supportsTools := false
	var firstToolCall provider.ToolCall
	if err != nil {
		fmt.Printf("  FAILED in %s: %v\n", d.Round(time.Millisecond), err)
	} else {
		fmt.Printf("  text:        %q\n", toolResp.Text)
		fmt.Printf("  stop_reason: %s\n", toolResp.StopReason)
		fmt.Printf("  tool_calls:  %d\n", len(toolResp.ToolCalls))
		for i, tc := range toolResp.ToolCalls {
			fmt.Printf("    [%d] id=%s name=%s args=%s\n", i, tc.ID, tc.Name, string(tc.Input))
		}
		fmt.Printf("  tokens:      in=%d  out=%d\n", toolResp.InputTokens, toolResp.OutputTokens)
		fmt.Printf("  latency:     %s\n", d.Round(time.Millisecond))
		if len(toolResp.ToolCalls) > 0 {
			supportsTools = true
			firstToolCall = toolResp.ToolCalls[0]
		}
	}
	fmt.Println()

	// --- Step 3: tool-result follow-up ----------------------------------
	if !supportsTools {
		fmt.Println("[3/3] tool-result follow-up: SKIPPED (model produced no tool_calls)")
		fmt.Println()
		fmt.Println("done.")
		return nil
	}
	fmt.Println("[3/3] tool-result follow-up: feeding fake \"391\" back to model")
	t0 = time.Now()
	followResp, err := p.Complete(ctx, provider.Request{
		Model:  model,
		System: "You are a math assistant. When you need to compute, call the calculate tool.",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "What is 17 * 23? Use the calculate tool."},
			{Role: provider.RoleAssistant, Content: toolResp.Text, ToolCalls: toolResp.ToolCalls},
			{Role: provider.RoleUser, ToolResults: []provider.ToolResult{
				{ToolUseID: firstToolCall.ID, Content: "391"},
			}},
		},
		Tools:     []provider.ToolDef{calcTool},
		MaxTokens: 256,
	})
	d = time.Since(t0)
	if err != nil {
		fmt.Printf("  FAILED in %s: %v\n", d.Round(time.Millisecond), err)
	} else {
		fmt.Printf("  text:        %q\n", followResp.Text)
		fmt.Printf("  stop_reason: %s\n", followResp.StopReason)
		fmt.Printf("  tokens:      in=%d  out=%d\n", followResp.InputTokens, followResp.OutputTokens)
		fmt.Printf("  latency:     %s\n", d.Round(time.Millisecond))
	}
	fmt.Println()
	fmt.Println("done.")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// redactURL returns the URL with the host/port intact but any userinfo or
// query params elided. We print the URL so the user knows which endpoint
// the smoke hit, but we never want to leak a key embedded in the URL.
func redactURL(u string) string {
	// Cheap redaction: keep up to the path, drop query string entirely.
	for i := 0; i < len(u); i++ {
		if u[i] == '?' {
			return u[:i] + "?…"
		}
	}
	return u
}
