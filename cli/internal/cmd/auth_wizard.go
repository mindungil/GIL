package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/core/cliutil"
	"github.com/mindungil/gil/core/credstore"
)

// providerInfo is the static metadata the wizard renders for each
// well-known provider: a human label, a one-line tagline shown next to
// the picker, and (for API-key providers) a "where to get a key" URL.
//
// Reference lift:
//   - opencode's providers.ts maps provider IDs to display names + hints
//     ("recommended", "ChatGPT Plus/Pro or API key").
//   - cline/cli's ModelPicker.providerModels maps provider → default model.
//   - aider/onboarding's "we'll point you at openrouter.ai/keys" pattern.
type providerInfo struct {
	name      credstore.ProviderName
	label     string
	tagline   string
	keyURL    string // empty for vllm (self-hosted has no canonical signup page)
	keyPrefix string // sample prefix shown in the prompt; empty when arbitrary
}

// providerCatalog is the source of truth for the wizard's first-step
// picker. Adding a new provider here automatically extends `gil auth
// login` (no extra wiring needed) — that's the "harness universality"
// rule: provider list lives in one place, not threaded through the
// command tree.
var providerCatalog = []providerInfo{
	{
		name:      credstore.Anthropic,
		label:     "Anthropic",
		tagline:   "claude-opus / sonnet / haiku  ·  best tool use",
		keyURL:    "https://console.anthropic.com/settings/keys",
		keyPrefix: "sk-ant-…",
	},
	{
		name:      credstore.OpenAI,
		label:     "OpenAI",
		tagline:   "gpt-4o / gpt-4o-mini / o1",
		keyURL:    "https://platform.openai.com/api-keys",
		keyPrefix: "sk-…",
	},
	{
		name:      credstore.OpenRouter,
		label:     "OpenRouter",
		tagline:   "proxy for 100+ models (anthropic, llama, qwen, gemini, …)",
		keyURL:    "https://openrouter.ai/keys",
		keyPrefix: "sk-or-v1-…",
	},
	{
		name:    credstore.VLLM,
		label:   "vLLM (self-hosted, OpenAI-compatible)",
		tagline: "your endpoint  ·  any open-weights model",
	},
}

// modelChoice is one row in a provider's model picker. ID is what we
// store; Label is what the user sees; Hint is a short cost/strength tag
// rendered after the label in dim text.
type modelChoice struct {
	ID    string
	Label string
	Hint  string
}

// recommendedModels maps a provider to a curated short-list shown in the
// wizard's model-picker step. The first entry is the default. We
// deliberately keep these short (3–5) — a 100-row picker for OpenRouter
// would be unusable; users who want anything off this list pick "Other"
// and type the model id.
//
// Reference lift: cline/cli's ModelPicker.tsx shows a similar curated set
// per provider with a fallback "Type custom" option.
func recommendedModels(name credstore.ProviderName) []modelChoice {
	switch name {
	case credstore.Anthropic:
		return []modelChoice{
			{"claude-haiku-4-5", "claude-haiku-4-5", "cheap, fast — default"},
			{"claude-sonnet-4-6", "claude-sonnet-4-6", "strong, balanced"},
			{"claude-opus-4-7", "claude-opus-4-7", "max strength, slow + pricey"},
		}
	case credstore.OpenAI:
		return []modelChoice{
			{"gpt-4o-mini", "gpt-4o-mini", "cheap, fast — default"},
			{"gpt-4o", "gpt-4o", "balanced"},
			{"o1-mini", "o1-mini", "reasoning"},
		}
	case credstore.OpenRouter:
		return []modelChoice{
			{"anthropic/claude-haiku-4-5", "anthropic/claude-haiku-4-5", "cheap, fast — default"},
			{"anthropic/claude-sonnet-4-6", "anthropic/claude-sonnet-4-6", "strong, balanced"},
			{"meta-llama/llama-3.3-70b-instruct", "meta-llama/llama-3.3-70b-instruct", "open"},
			{"google/gemini-2.5-flash", "google/gemini-2.5-flash", "cheap"},
			{"qwen/qwen3-32b", "qwen/qwen3-32b", "open + capable"},
		}
	}
	return nil
}

// runLoginWizard is the multi-step interactive setup flow that replaces
// the old one-line "Enter API key" prompt. It mirrors opencode's
// providers.ts (provider picker → key prompt → save), aider's onboarding
// (offer to test the key once entered), and goose's configure (each step
// confirmed before moving on).
//
// Inputs from cmd flags:
//   - args[0] (optional): provider id — if provided, skip step 1.
//   - --api-key: pre-supplied key, skip step 2's prompt.
//   - --base-url: pre-supplied base URL, skip the vllm sub-step.
//   - --model: pre-supplied model id, skip step 3.
//   - --no-test: skip the smoke-test step (used by tests + scripted setup).
//
// When all four flags are set, the wizard is fully non-interactive — that's
// the existing scriptable contract `gil auth login --api-key X anthropic`
// preserves so dogfood-installer scripts don't break.
func runLoginWizard(cmd *cobra.Command, args []string, apiKey, baseURL, model string, noTest bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	reader := bufio.NewReader(in)
	g := uistyle.NewGlyphs(asciiMode)
	p := uistyle.NewPalette(false)

	// Decide up-front whether we have a TTY; the wizard's interactive
	// rendering only makes sense for human users. When stdin is piped
	// (tests, scripted setup) we keep the existing positional + flag
	// behaviour — every step that would otherwise prompt errors out
	// with a clear "missing required value" message.
	interactive := stdinIsTTY(in)

	// Step 1: pick a provider (or use the positional arg).
	provider, err := wizardPickProvider(cmd, args, reader, out, p, g, interactive)
	if err != nil {
		return err
	}
	info, knownProvider := providerInfoFor(provider)

	// Step 2: get the API key (and base URL for vllm).
	var resolvedBaseURL = strings.TrimSpace(baseURL)
	if provider == credstore.VLLM {
		resolvedBaseURL, err = wizardVLLMBaseURL(cmd, reader, out, p, g, resolvedBaseURL, interactive)
		if err != nil {
			return err
		}
	}

	resolvedKey, err := wizardGetAPIKey(cmd, reader, out, p, g, info, knownProvider, apiKey, interactive)
	if err != nil {
		return err
	}

	// Step 3: pick a model. The wizard offers a curated short-list for
	// known providers; vllm + custom providers fall through to a free-
	// text prompt because their model namespace is open-ended.
	resolvedModel, err := wizardPickModel(cmd, reader, out, p, g, provider, knownProvider, model, interactive)
	if err != nil {
		return err
	}

	// Construct the credential and save BEFORE testing — the test step
	// is opt-in; users who hit Ctrl-C on the test confirm should still
	// have their key saved (otherwise the wizard's third step burns the
	// key for no reason).
	cred := credstore.Credential{
		Type:    credstore.CredAPI,
		APIKey:  resolvedKey,
		BaseURL: resolvedBaseURL,
		Model:   resolvedModel,
	}
	store := newStoreFor(cmd)
	if err := store.Set(ctx, provider, cred); err != nil {
		return cliutil.Wrap(err, "could not save credential", "check that "+authStorePath(cmd)+" is writable")
	}

	if interactive {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  "+p.Success(g.Done)+" Saved to: "+p.Surface(authStorePath(cmd))+p.Dim(" (mode 0600)"))
	} else {
		fmt.Fprintf(out, "Saved credential for %s (%s).\n", provider, cred.MaskedKey())
	}

	// Step 4: optionally test the connection. Skipped when --no-test is
	// set OR stdin is non-interactive (so scripted callers don't hang).
	if !noTest && interactive {
		if err := wizardTestConnection(ctx, reader, out, p, g, provider, cred); err != nil {
			// Non-fatal: the credential is saved; the test failure is
			// surfaced for diagnostic purposes. The user can re-test
			// later with `gil auth test <provider>`.
			fmt.Fprintln(out, "  "+p.Caution(g.Failed)+" "+err.Error())
			fmt.Fprintln(out, "  "+p.Dim("retry later with: gil auth test "+string(provider)))
		}
	}

	if interactive {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  "+p.Dim("Next:")+"  "+p.Surface("gil")+p.Dim("  (start chat)"))
		fmt.Fprintln(out, "  "+p.Dim("       gil auth list / edit / test / logout"))
	}
	return nil
}

// wizardPickProvider implements step 1 of the wizard. When args[0] is
// supplied, we honour it (the existing positional contract). Otherwise
// we render the catalog as a numbered picker; arbitrary names are
// accepted with a warning.
func wizardPickProvider(cmd *cobra.Command, args []string, reader *bufio.Reader, out io.Writer, p uistyle.Palette, g uistyle.Glyphs, interactive bool) (credstore.ProviderName, error) {
	if len(args) >= 1 && strings.TrimSpace(args[0]) != "" {
		name := credstore.ProviderName(strings.TrimSpace(args[0]))
		if _, known := providerInfoFor(name); !known {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %q is not a well-known provider; storing it anyway.\n", name)
		}
		return name, nil
	}

	if !interactive {
		// Piped stdin and no positional — the script forgot to specify;
		// fall through to the legacy picker which will return an error
		// when nothing is typed. (Old behaviour preserved.)
		return pickProvider(cmd, args)
	}

	// Banner + numbered list.
	renderWizardBanner(out, p, g, "Provider Setup")
	fmt.Fprintln(out, "  "+p.Bold("1. Pick a provider:"))
	fmt.Fprintln(out)
	for i, info := range providerCatalog {
		fmt.Fprintf(out, "     [%d]  %-40s %s\n",
			i+1,
			p.Surface(info.label),
			p.Dim(info.tagline),
		)
	}
	fmt.Fprintln(out, "     [c]  cancel")
	fmt.Fprintln(out)

	for {
		fmt.Fprintf(out, "  "+p.Dim("Your choice [1-%d]: "), len(providerCatalog))
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if err == io.EOF {
				return "", cliutil.New("login cancelled (EOF)", "")
			}
			continue
		}
		if line == "c" || strings.EqualFold(line, "cancel") {
			return "", cliutil.New("login cancelled", "")
		}
		// Numeric choice.
		var idx int
		if _, errN := fmt.Sscanf(line, "%d", &idx); errN == nil && idx >= 1 && idx <= len(providerCatalog) {
			return providerCatalog[idx-1].name, nil
		}
		// Name match — accept "anthropic" as easily as "1".
		for _, info := range providerCatalog {
			if strings.EqualFold(line, string(info.name)) || strings.EqualFold(line, info.label) {
				return info.name, nil
			}
		}
		fmt.Fprintln(out, "  "+p.Caution("not a valid choice — type a number or the provider name"))
	}
}

// wizardVLLMBaseURL prompts for the vllm endpoint URL when not supplied
// via --base-url. vllm is the only provider where this is mandatory and
// has no canonical default.
func wizardVLLMBaseURL(cmd *cobra.Command, reader *bufio.Reader, out io.Writer, p uistyle.Palette, g uistyle.Glyphs, current string, interactive bool) (string, error) {
	if current != "" {
		return current, nil
	}
	if !interactive {
		return "", cliutil.New("vllm requires --base-url", `pass --base-url http://host:port/v1`)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+p.Bold("2. vLLM endpoint:"))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "     "+p.Dim("Your OpenAI-compatible URL — typically ends in /v1"))
	fmt.Fprintln(out)
	for {
		fmt.Fprint(out, "  "+p.Dim("Base URL (e.g. http://localhost:8000/v1): "))
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		base := strings.TrimSpace(line)
		if base == "" {
			if err == io.EOF {
				return "", cliutil.New("vllm requires --base-url", `pass --base-url http://host:port/v1`)
			}
			fmt.Fprintln(out, "  "+p.Caution("base URL is required for vllm"))
			continue
		}
		return base, nil
	}
}

// wizardGetAPIKey implements step 2. For known providers we render the
// where-to-get-a-key URL + key prefix above the prompt; for vllm an
// empty key is acceptable (some self-hosted endpoints don't enforce
// auth) and the user can press enter to skip.
func wizardGetAPIKey(cmd *cobra.Command, reader *bufio.Reader, out io.Writer, p uistyle.Palette, g uistyle.Glyphs, info providerInfo, known bool, apiKey string, interactive bool) (string, error) {
	if apiKey != "" {
		key := strings.TrimSpace(apiKey)
		if key == "" {
			return "", cliutil.New("API key is empty", `pass --api-key <key>`)
		}
		if known {
			if warn := validateKeyPrefix(info.name, key); warn != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", warn)
			}
		}
		return key, nil
	}
	if !interactive {
		return "", cliutil.New("API key not provided", `pass --api-key <key>`)
	}

	step := "2"
	if info.name == credstore.VLLM {
		step = "3"
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+p.Bold(step+". "+info.label+" API key:"))
	fmt.Fprintln(out)
	if info.keyURL != "" {
		fmt.Fprintln(out, "     "+p.Dim("Get one at  ")+p.Info(info.keyURL))
	}
	if info.keyPrefix != "" {
		fmt.Fprintln(out, "     "+p.Dim("Looks like  ")+p.Surface(info.keyPrefix))
	}
	if info.name == credstore.VLLM {
		fmt.Fprintln(out, "     "+p.Dim("Press enter to skip if your endpoint has no auth."))
	}
	fmt.Fprintln(out)

	for attempt := 0; attempt < 3; attempt++ {
		key, err := wizardReadPassword(cmd, reader, "  API key (input hidden): ")
		if err != nil {
			return "", cliutil.Wrap(err, "could not read API key", "try again, or pass --api-key")
		}
		key = strings.TrimSpace(key)
		// Empty key is OK for vllm (endpoint may not require auth).
		if key == "" && info.name == credstore.VLLM {
			return "", nil
		}
		if key == "" {
			fmt.Fprintln(out, "  "+p.Caution("API key cannot be empty"))
			continue
		}
		if known {
			if warn := validateKeyPrefix(info.name, key); warn != "" {
				fmt.Fprintln(out, "  "+p.Caution("warning: "+warn))
			}
		}
		return key, nil
	}
	return "", cliutil.New("too many empty API key attempts", "run `gil auth login` again when ready")
}

// wizardReadPassword reads a secret from stdin. When stdin is a real
// TTY we use term.ReadPassword (echo off); otherwise we read a line
// from the wizard's existing bufio.Reader so we don't drop bytes
// across calls. Reusing the reader is critical: each call to the
// existing readLine helper builds a fresh bufio.Reader, which buffers
// the entire stream and then throws those bytes away — fine for one
// call, broken across the wizard's three or four sequential prompts.
func wizardReadPassword(cmd *cobra.Command, reader *bufio.Reader, prompt string) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) && os.Getenv("GIL_TEST_FORCE_TTY") != "1" {
		fmt.Fprint(cmd.OutOrStdout(), prompt)
		key, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.OutOrStdout())
		if err != nil {
			return "", err
		}
		return string(key), nil
	}
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// wizardPickModel implements step 3. For curated providers we render the
// short-list + "Other" sentinel; for vllm + custom providers we go
// straight to a free-text prompt.
//
// Empty model (user pressed enter on the free-text prompt) is allowed
// for known providers — the chat surface falls back to a sane default
// per provider, and recording an explicit empty Model is the same as
// "no preference set". For vllm we require a non-empty value because
// the chat surface has no usable default.
func wizardPickModel(cmd *cobra.Command, reader *bufio.Reader, out io.Writer, p uistyle.Palette, g uistyle.Glyphs, name credstore.ProviderName, known bool, modelFlag string, interactive bool) (string, error) {
	if modelFlag != "" {
		return strings.TrimSpace(modelFlag), nil
	}
	if !interactive {
		// Non-interactive path: preserve the old behaviour. Empty model
		// is fine; the chat surface falls back to provider defaults.
		return "", nil
	}

	choices := recommendedModels(name)
	step := "3"
	if name == credstore.VLLM {
		step = "4"
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+p.Bold(step+". Default model:"))
	fmt.Fprintln(out)

	if len(choices) == 0 {
		// No curated list (vllm or custom provider) — free-text prompt.
		if name == credstore.VLLM {
			fmt.Fprintln(out, "     "+p.Dim("Type the model id your endpoint exposes."))
			fmt.Fprintln(out, "     "+p.Dim("Examples: qwen3-32b, mistral-7b, your-finetune-v2"))
		} else {
			fmt.Fprintln(out, "     "+p.Dim("Type the model id (or press enter to skip)."))
		}
		fmt.Fprintln(out)
		for {
			fmt.Fprint(out, "  "+p.Dim("Model id: "))
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				return "", err
			}
			model := strings.TrimSpace(line)
			if model == "" {
				if name == credstore.VLLM {
					if err == io.EOF {
						return "", cliutil.New("vllm requires a model id", "")
					}
					fmt.Fprintln(out, "  "+p.Caution("vllm requires a model id (no canonical default)"))
					continue
				}
				return "", nil
			}
			return model, nil
		}
	}

	fmt.Fprintln(out, "     "+p.Dim(fmt.Sprintf("Recommended for %s:", name)))
	fmt.Fprintln(out)
	for i, ch := range choices {
		fmt.Fprintf(out, "     [%d]  %-36s %s\n", i+1, p.Surface(ch.Label), p.Dim(ch.Hint))
	}
	fmt.Fprintf(out, "     [%d]  %s\n", len(choices)+1, p.Surface("Other (type model id)"))
	fmt.Fprintln(out)

	for {
		fmt.Fprintf(out, "  "+p.Dim("Your choice [1-%d, default 1]: "), len(choices)+1)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		raw := strings.TrimSpace(line)
		if raw == "" {
			// Default to first.
			return choices[0].ID, nil
		}
		var idx int
		if _, errN := fmt.Sscanf(raw, "%d", &idx); errN == nil {
			if idx >= 1 && idx <= len(choices) {
				return choices[idx-1].ID, nil
			}
			if idx == len(choices)+1 {
				// Free text branch.
				fmt.Fprint(out, "  "+p.Dim("Model id: "))
				line, err := reader.ReadString('\n')
				if err != nil && err != io.EOF {
					return "", err
				}
				return strings.TrimSpace(line), nil
			}
		}
		// Name match — let the user type the model verbatim.
		for _, ch := range choices {
			if strings.EqualFold(raw, ch.ID) || strings.EqualFold(raw, ch.Label) {
				return ch.ID, nil
			}
		}
		// Treat anything else as a free-text model id (so users who
		// know the exact id can type it without picking "Other" first).
		return raw, nil
	}
}

// wizardTestConnection implements step 4. We send a tiny "say ok"
// completion via credstore.TestProvider, then surface result + usage so
// the user knows the credential actually works.
func wizardTestConnection(ctx context.Context, reader *bufio.Reader, out io.Writer, p uistyle.Palette, g uistyle.Glyphs, provider credstore.ProviderName, cred credstore.Credential) error {
	fmt.Fprintln(out)
	fmt.Fprint(out, "  "+p.Dim("Test connection? [Y/n]: "))
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil // user dropped the prompt — not an error
	}
	choice := strings.ToLower(strings.TrimSpace(line))
	if choice == "n" || choice == "no" {
		fmt.Fprintln(out, "  "+p.Dim("(skipped)"))
		return nil
	}
	fmt.Fprintln(out, "  "+p.Dim(g.Running)+" testing…")

	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := credstore.TestProvider(testCtx, provider, cred)
	if err != nil {
		return humaniseTestError(err)
	}
	tokens := ""
	if res.InputTokens > 0 || res.OutputTokens > 0 {
		tokens = fmt.Sprintf(" (in:%d out:%d)", res.InputTokens, res.OutputTokens)
	}
	reply := res.ReplyText
	if reply == "" {
		reply = "(no text)"
	}
	fmt.Fprintf(out, "  %s Connection OK %s%s%s\n",
		p.Success(g.Done),
		p.Dim("·"),
		" "+p.Surface(reply),
		p.Dim(fmt.Sprintf("  · model %s · %dms%s", res.Model, res.Latency.Milliseconds(), tokens)),
	)
	return nil
}

// humaniseTestError translates the most common provider error shapes
// into actionable hints. We keep this conservative: anything we don't
// recognise gets passed through verbatim.
func humaniseTestError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401"), strings.Contains(strings.ToLower(msg), "unauthorized"):
		return fmt.Errorf("%w  ·  hint: check your API key (looks invalid)", err)
	case strings.Contains(msg, "403"):
		return fmt.Errorf("%w  ·  hint: API key lacks permission for this model", err)
	case strings.Contains(msg, "404"):
		return fmt.Errorf("%w  ·  hint: model id not recognised — try `gil auth edit`", err)
	case strings.Contains(strings.ToLower(msg), "no such host"), strings.Contains(strings.ToLower(msg), "connection refused"):
		return fmt.Errorf("%w  ·  hint: check your base URL is reachable", err)
	}
	return err
}

// renderWizardBanner draws the wizard's header. Mirrors the chat
// surface's mission-briefing aesthetic (title plate + thick rule) so a
// user dropping into `gil auth login` from `gil` recognises the visual
// vocabulary. Uses g.HeavyHRule and g.LightHRule so the --ascii fallback
// (`gil --ascii auth login`) renders cleanly without Unicode glyphs.
func renderWizardBanner(out io.Writer, p uistyle.Palette, g uistyle.Glyphs, subtitle string) {
	fmt.Fprintln(out)
	title := p.Primary("G I L")
	fmt.Fprintf(out, "  %s  %s\n", title, p.Dim(g.LightHRule+"  "+subtitle))
	fmt.Fprintln(out, "  "+p.Dim(strings.Repeat(g.HeavyHRule, 76)))
	fmt.Fprintln(out)
}

// providerInfoFor returns the catalog entry for name plus a "known"
// flag. Unknown providers still get a generic providerInfo so the
// wizard's prompts work without special-cased fallback paths.
func providerInfoFor(name credstore.ProviderName) (providerInfo, bool) {
	for _, info := range providerCatalog {
		if info.name == name {
			return info, true
		}
	}
	return providerInfo{
		name:    name,
		label:   string(name),
		tagline: "(custom provider)",
	}, false
}

// stdinIsTTY mirrors stdoutIsTTY but for the input side. The wizard
// only renders its rich multi-step UI when stdin is interactive; piped
// input (tests, scripts) gets the legacy positional-args behaviour.
//
// Tests can flip this to "interactive" via the GIL_TEST_FORCE_TTY env
// var so we can exercise the wizard's prompts without a real PTY. The
// override is undocumented (intentionally — the harness is the only
// caller) and ignored in production binaries since users don't have
// a reason to set it.
func stdinIsTTY(in io.Reader) bool {
	if os.Getenv("GIL_TEST_FORCE_TTY") == "1" {
		return true
	}
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
