package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mindungil/gil/core/cliutil"
	"github.com/mindungil/gil/core/credstore"
)

// authProviderJSON is the per-provider shape emitted by `gil auth list
// --output json`. The masked_key field uses the same redaction as the
// text renderer (Credential.MaskedKey) so a JSON dump never carries the
// raw secret.
type authProviderJSON struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	MaskedKey string    `json:"masked_key"`
	BaseURL   string    `json:"base_url,omitempty"`
	Model     string    `json:"model,omitempty"`
	Updated   time.Time `json:"updated"`
}

type authListJSON struct {
	Providers []authProviderJSON `json:"providers"`
	File      string             `json:"file"`
}

// authCmd returns the "gil auth" subcommand group.
//
// Auth is a local-only file operation: it reads/writes auth.json and never
// talks to gild. That means it works whether or not the daemon is running,
// which matches the user expectation of running `gil auth login` before
// starting any session.
//
// Reference lift:
//   - opencode `auth/index.ts` — JSON file shape (provider→credential map),
//     atomic write semantics, 0600 permission model.
//   - opencode `cli/cmd/providers.ts` — three-subcommand split (login,
//     list, logout) with a provider picker for the interactive flow.
//   - codex `cli/src/login.rs` — `safe_format_key` masking style and the
//     "logged in using ..." status presentation.
func authCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Manage provider credentials (api keys, oauth)",
		Long: `Manage provider credentials for gil.

Credentials are stored in $GIL_BASE/auth.json (default: ~/.gil/auth.json) with
0600 file permissions. The gild daemon consults this file before falling back
to environment variables, so a configured credential always wins over an
ambient env var.`,
	}
	c.AddCommand(authLoginCmd())
	c.AddCommand(authListCmd())
	c.AddCommand(authLogoutCmd())
	c.AddCommand(authStatusCmd())
	c.AddCommand(authEditCmd())
	c.AddCommand(authTestCmd())
	return c
}

// authStorePath resolves the path to auth.json.
//
// Resolution order (highest priority first):
//
//  1. --auth-file <path>          (hidden test/debug override)
//  2. GIL_AUTH_FILE=<path>        (env var, useful for CI fixtures)
//  3. defaultLayout().AuthFile()  (Phase 11 Track A: XDG Config/auth.json,
//                                  honours GIL_HOME)
//
// The XDG default lands the file under ~/.config/gil/auth.json on Linux,
// which is the documented location across opencode/codex/goose-style
// harnesses. GIL_HOME=<dir> remaps it to <dir>/config/auth.json to make
// sandboxed test runs hermetic.
func authStorePath(cmd *cobra.Command) string {
	if cmd != nil {
		if v, _ := cmd.Flags().GetString("auth-file"); v != "" {
			return v
		}
	}
	if v := os.Getenv("GIL_AUTH_FILE"); v != "" {
		return v
	}
	return defaultLayout().AuthFile()
}

// newStoreFor builds the FileStore for the given command, using the same
// path-resolution rules as `gil auth login`. Each subcommand calls this
// rather than constructing the store inline so test plumbing only needs one
// override point.
func newStoreFor(cmd *cobra.Command) *credstore.FileStore {
	return credstore.NewFileStore(authStorePath(cmd))
}

// addAuthFileFlag wires the hidden --auth-file override on each subcommand.
// Hidden because it's a test/debugging seam, not a user-facing knob.
func addAuthFileFlag(c *cobra.Command) {
	c.Flags().String("auth-file", "", "override auth.json path (test/debug)")
	_ = c.Flags().MarkHidden("auth-file")
}

// authLoginCmd implements `gil auth login [<provider>]`.
//
// PHASE 25 REDESIGN: replaced the bare one-line "Enter API key" prompt
// with a multi-step interactive wizard (provider picker → key → model →
// optional connection test). The non-interactive contract — calling
// with --api-key + positional provider — is preserved bug-for-bug so
// scripted installs still work; only the interactive UX changed.
//
// See auth_wizard.go for the wizard implementation. Reference lifts:
//   - opencode's providers.ts — multi-step provider login flow
//   - cline/cli's ModelPicker.tsx — curated model list per provider
//   - aider/onboarding.py — try-key-then-test contract
//   - goose/configure — confirm-each-step layout
func authLoginCmd() *cobra.Command {
	var apiKey, baseURL, model string
	var noTest bool
	c := &cobra.Command{
		Use:   "login [provider]",
		Short: "Log in to a provider (writes credentials to auth.json)",
		Long: `Add or update a credential for a provider.

When run without arguments, gil drops you into a wizard that walks you
through picking a provider, entering its API key, and choosing a default
model. Each step is confirmed before moving on; the wizard ends with an
optional "test connection" round-trip.

Existing non-interactive flags continue to work: pass --api-key (and
--base-url for vllm) to skip every prompt and write the credential
directly.

Examples:
  gil auth login                                          # full wizard
  gil auth login anthropic                                # skip provider picker
  gil auth login anthropic --api-key sk-ant-... --model claude-haiku-4-5
  gil auth login vllm --base-url http://host:8000/v1 --api-key local --model qwen3-32b`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLoginWizard(cmd, args, apiKey, baseURL, model, noTest)
		},
	}
	c.Flags().StringVar(&apiKey, "api-key", "", "API key (skips interactive prompt)")
	c.Flags().StringVar(&baseURL, "base-url", "", "base URL (vllm/custom endpoints)")
	c.Flags().StringVar(&model, "model", "", "default model id for this provider (skips model picker)")
	c.Flags().BoolVar(&noTest, "no-test", false, "skip the post-login connection test")
	addAuthFileFlag(c)
	return c
}

// authListCmd implements `gil auth list`.
//
// Output is a tabwriter-aligned table mirroring `gil status`. Keys are
// masked through Credential.MaskedKey so a copy/paste of the terminal does
// not leak the secret.
func authListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List configured provider credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			store := newStoreFor(cmd)
			names, err := store.List(ctx)
			if err != nil {
				return cliutil.Wrap(err, "could not read credentials", "run `gil doctor` to inspect filesystem permissions on the auth file")
			}
			out := cmd.OutOrStdout()
			if outputJSON() {
				return writeAuthListJSON(ctx, out, store, names, authStorePath(cmd))
			}
			if len(names) == 0 {
				fmt.Fprintf(out, "No credentials configured. Run \"gil auth login <provider>\" to add one.\n")
				fmt.Fprintf(out, "File: %s\n", authStorePath(cmd))
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PROVIDER\tTYPE\tKEY\tMODEL\tBASE_URL\tUPDATED")
			for _, n := range names {
				cred, err := store.Get(ctx, n)
				if err != nil || cred == nil {
					continue
				}
				baseURL := cred.BaseURL
				if baseURL == "" {
					baseURL = "-"
				}
				model := cred.Model
				if model == "" {
					model = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					n, cred.Type, cred.MaskedKey(), model, baseURL, formatTime(cred.Updated))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(out, "\nFile: %s\n", authStorePath(cmd))
			return nil
		},
	}
	addAuthFileFlag(c)
	return c
}

// writeAuthListJSON emits the structured `--output json` view of
// `gil auth list`. The shape is `{"providers":[...], "file":"<path>"}`
// so consumers can reach `.providers[].masked_key` for a quick "which
// provider is wired" probe and `.file` for "where do I edit it".
//
// Each entry's masked_key uses the same Credential.MaskedKey rule as the
// text path — never the raw secret — so a JSON dump captured in chat or
// CI logs never leaks credentials.
func writeAuthListJSON(ctx context.Context, w io.Writer, store interface {
	Get(context.Context, credstore.ProviderName) (*credstore.Credential, error)
}, names []credstore.ProviderName, file string) error {
	rows := make([]authProviderJSON, 0, len(names))
	for _, n := range names {
		cred, err := store.Get(ctx, n)
		if err != nil || cred == nil {
			continue
		}
		rows = append(rows, authProviderJSON{
			Name:      string(n),
			Type:      string(cred.Type),
			MaskedKey: cred.MaskedKey(),
			BaseURL:   cred.BaseURL,
			Model:     cred.Model,
			Updated:   cred.Updated,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(authListJSON{Providers: rows, File: file})
}

// authLogoutCmd implements `gil auth logout <provider>`.
//
// Idempotent: removing a provider that isn't configured is a successful
// no-op with an informational message, so scripts can call this without
// guarding.
func authLogoutCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "logout <provider>",
		Short: "Remove a stored credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			provider := credstore.ProviderName(strings.TrimSpace(args[0]))
			if provider == "" {
				return cliutil.New("provider name is empty", `usage: gil auth logout <provider>`)
			}
			store := newStoreFor(cmd)
			existed, err := store.Get(ctx, provider)
			if err != nil {
				return cliutil.Wrap(err, "could not read credentials", "run `gil doctor` to inspect filesystem permissions on the auth file")
			}
			if err := store.Remove(ctx, provider); err != nil {
				return cliutil.Wrap(err, "could not remove credential", "check that the auth file is writable; run `gil auth list` to confirm what's configured")
			}
			out := cmd.OutOrStdout()
			if existed == nil {
				fmt.Fprintf(out, "No credential for %s; nothing to remove.\n", provider)
			} else {
				fmt.Fprintf(out, "Removed credential for %s.\n", provider)
			}
			return nil
		},
	}
	addAuthFileFlag(c)
	return c
}

// authStatusCmd implements `gil auth status`.
//
// Status is a presentation cousin of list: it cross-references the credstore
// with the env vars gild's factory falls back to, so the user sees the full
// "what gild will pick" picture.
func authStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show configured providers and which env vars override them",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			store := newStoreFor(cmd)
			names, err := store.List(ctx)
			if err != nil {
				return cliutil.Wrap(err, "could not read credentials", "run `gil doctor` to inspect filesystem permissions on the auth file")
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "auth file: %s\n\n", authStorePath(cmd))

			if len(names) == 0 {
				fmt.Fprintln(out, "credentials: (none configured)")
			} else {
				fmt.Fprintln(out, "credentials:")
				for _, n := range names {
					cred, err := store.Get(ctx, n)
					if err != nil || cred == nil {
						continue
					}
					modelTag := ""
					if cred.Model != "" {
						modelTag = "  model=" + cred.Model
					}
					fmt.Fprintf(out, "  %-12s %s %s%s\n", n, cred.Type, cred.MaskedKey(), modelTag)
				}
			}

			fmt.Fprintln(out)
			fmt.Fprintln(out, "environment:")
			envs := []struct {
				key      string
				provider credstore.ProviderName
			}{
				{"ANTHROPIC_API_KEY", credstore.Anthropic},
				{"OPENAI_API_KEY", credstore.OpenAI},
				{"OPENROUTER_API_KEY", credstore.OpenRouter},
				{"VLLM_API_KEY", credstore.VLLM},
				{"VLLM_BASE_URL", credstore.VLLM},
			}
			any := false
			for _, e := range envs {
				if v := os.Getenv(e.key); v != "" {
					any = true
					fmt.Fprintf(out, "  %s set (provider: %s)\n", e.key, e.provider)
				}
			}
			if !any {
				fmt.Fprintln(out, "  (no provider env vars set)")
			}
			return nil
		},
	}
	addAuthFileFlag(c)
	return c
}

// pickProvider resolves the provider name either from the positional arg or
// an interactive picker. Picker is keyboard-driven (numbered choices read
// from stdin) — no third-party prompt library, just the standard library.
func pickProvider(cmd *cobra.Command, args []string) (credstore.ProviderName, error) {
	if len(args) >= 1 && args[0] != "" {
		name := credstore.ProviderName(strings.TrimSpace(args[0]))
		// Accept arbitrary names (so users can configure custom
		// providers), but call out unknown names as a hint.
		known := false
		for _, k := range credstore.KnownProviders() {
			if k == name {
				known = true
				break
			}
		}
		if !known {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %q is not a well-known provider; storing it anyway.\n", name)
		}
		return name, nil
	}

	out := cmd.OutOrStdout()
	known := credstore.KnownProviders()
	fmt.Fprintln(out, "Select a provider:")
	for i, p := range known {
		fmt.Fprintf(out, "  [%d] %s\n", i+1, p)
	}
	fmt.Fprintf(out, "  [%d] cancel\n", len(known)+1)

	line, err := readLine(cmd, "> ")
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", cliutil.New("no provider selected", `pass <provider> as a positional arg`)
	}
	// Accept either a number or a name.
	for i, p := range known {
		if line == fmt.Sprintf("%d", i+1) || strings.EqualFold(line, string(p)) {
			return p, nil
		}
	}
	if line == fmt.Sprintf("%d", len(known)+1) || strings.EqualFold(line, "cancel") {
		return "", cliutil.New("login cancelled", "")
	}
	return "", cliutil.New(fmt.Sprintf("unrecognised choice %q", line), `pick a number from the list, or pass <provider> directly`)
}

// readLine reads a single line of plaintext from the command's stdin (or
// os.Stdin if not set). It writes the prompt to stdout first.
func readLine(cmd *cobra.Command, prompt string) (string, error) {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	in := cmd.InOrStdin()
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readPassword reads a secret from stdin without echoing. When stdin is not
// a TTY (e.g. piped input in tests), it falls back to readLine — that's what
// codex's read_api_key_from_stdin does and it keeps the command scriptable.
func readPassword(cmd *cobra.Command, prompt string) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cmd.OutOrStdout(), prompt)
		key, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.OutOrStdout())
		if err != nil {
			return "", err
		}
		return string(key), nil
	}
	// Non-TTY: read a plain line. Useful for piped input and tests.
	return readLine(cmd, prompt)
}

// validateKeyPrefix returns a non-empty warning string when the key shape
// looks wrong for the chosen provider. It never blocks because some users
// route through proxies that rewrite or wrap keys.
func validateKeyPrefix(provider credstore.ProviderName, key string) string {
	switch provider {
	case credstore.Anthropic:
		if !strings.HasPrefix(key, "sk-ant-") {
			return `anthropic keys typically start with "sk-ant-"; saving anyway`
		}
	case credstore.OpenAI:
		if !strings.HasPrefix(key, "sk-") {
			return `openai keys typically start with "sk-"; saving anyway`
		}
	case credstore.OpenRouter:
		if !strings.HasPrefix(key, "sk-or-v1-") && !strings.HasPrefix(key, "sk-or-") {
			return `openrouter keys typically start with "sk-or-v1-"; saving anyway`
		}
	}
	return ""
}

// formatTime renders an Updated timestamp in a short, locale-stable form
// for table output. Zero times render as "-" so a hand-edited file with
// missing timestamps doesn't show a Y2K-era date.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// authEditCmd implements `gil auth edit <provider>`.
//
// Edit re-runs the wizard for an existing entry, pre-populating the
// resolved credential's BaseURL and Model so the user can keep what's
// already there by pressing enter at each step. The API key is always
// re-prompted (not echoed); pressing enter on the key prompt keeps the
// existing key intact, otherwise the new value replaces it.
//
// Reference lift: opencode's providers logout/login round-trip is the
// closest equivalent — they require remove + re-add. We give a single
// command for the common "I just want to change my model" case so users
// don't have to remember their full key to update an unrelated field.
func authEditCmd() *cobra.Command {
	var apiKey, baseURL, model string
	var noTest bool
	c := &cobra.Command{
		Use:   "edit <provider>",
		Short: "Edit an existing credential's key/base-url/model",
		Long: `Re-prompt for the API key, base URL, and default model on an
existing credential entry, keeping any value the user does not change.

This is the right command when you want to switch models on an existing
provider (no need to re-paste your API key) or to retarget a vllm
endpoint at a new URL.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			provider := credstore.ProviderName(strings.TrimSpace(args[0]))
			if provider == "" {
				return cliutil.New("provider name is empty", "usage: gil auth edit <provider>")
			}
			store := newStoreFor(cmd)
			existing, err := store.Get(ctx, provider)
			if err != nil {
				return cliutil.Wrap(err, "could not read credential", "run `gil auth list` to confirm what's configured")
			}
			if existing == nil {
				return cliutil.New(fmt.Sprintf("no credential for %s", provider),
					"register one first: gil auth login "+string(provider))
			}

			// Pre-fill from the existing credential. The wizard treats
			// these as user-supplied (so prompts are skipped); flags
			// the user passed on the command line still win.
			if apiKey == "" {
				// Don't pre-fill the key — force re-prompt OR honour
				// the special "skip" sentinel below. We pass the
				// existing key under a hidden mechanism: a flag on the
				// command's Annotations that the wizard reads.
			}
			if baseURL == "" {
				baseURL = existing.BaseURL
			}
			if model == "" {
				model = existing.Model
			}

			// Run the wizard with the positional arg so it skips step 1.
			argsForWizard := []string{string(provider)}
			// Special handling for the API key: when the user didn't
			// pass --api-key, we want the wizard to prompt with the
			// option of pressing enter to keep the existing key. We
			// pass the existing key via cobra Annotations as a
			// workaround so the wizard has access without changing its
			// signature. (The Annotations map is a stable cobra
			// mechanism and not part of the user-facing surface.)
			if cmd.Annotations == nil {
				cmd.Annotations = map[string]string{}
			}
			cmd.Annotations["existing_api_key"] = existing.APIKey

			return runLoginWizard(cmd, argsForWizard, apiKey, baseURL, model, noTest)
		},
	}
	c.Flags().StringVar(&apiKey, "api-key", "", "new API key (skips key prompt)")
	c.Flags().StringVar(&baseURL, "base-url", "", "new base URL (overrides stored value)")
	c.Flags().StringVar(&model, "model", "", "new default model id")
	c.Flags().BoolVar(&noTest, "no-test", false, "skip the post-edit connection test")
	addAuthFileFlag(c)
	return c
}

// authTestCmd implements `gil auth test <provider>`.
//
// Sends a tiny "say ok" completion against the stored credential so the
// user can verify the key still works without re-entering anything.
// Output mirrors the wizard's step-4 line.
func authTestCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "test <provider>",
		Short: "Test the stored credential by sending a tiny completion",
		Long: `Send a one-token "say ok" completion against the credential
stored for <provider>. This is the same smoke test the login wizard
offers at the end of registration; running it again is the cheapest way
to verify a key still works (e.g., after a provider rotated keys or
a self-hosted vllm endpoint moved).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			provider := credstore.ProviderName(strings.TrimSpace(args[0]))
			store := newStoreFor(cmd)
			cred, err := store.Get(ctx, provider)
			if err != nil {
				return cliutil.Wrap(err, "could not read credential", "run `gil auth list` to confirm what's configured")
			}
			if cred == nil {
				return cliutil.New(fmt.Sprintf("no credential for %s", provider),
					"register one first: gil auth login "+string(provider))
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Testing %s (%s)…\n", provider, cred.MaskedKey())

			testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			res, terr := credstore.TestProvider(testCtx, provider, *cred)
			if terr != nil {
				// Use the same humanised translation as the wizard so
				// the user sees a consistent error vocabulary across
				// surfaces.
				return humaniseTestError(terr)
			}
			tokens := ""
			if res.InputTokens > 0 || res.OutputTokens > 0 {
				tokens = fmt.Sprintf(" (in:%d out:%d)", res.InputTokens, res.OutputTokens)
			}
			reply := res.ReplyText
			if reply == "" {
				reply = "(no text)"
			}
			fmt.Fprintf(out, "OK  reply=%q  model=%s  latency=%dms%s\n",
				reply, res.Model, res.Latency.Milliseconds(), tokens)
			return nil
		},
	}
	addAuthFileFlag(c)
	return c
}
