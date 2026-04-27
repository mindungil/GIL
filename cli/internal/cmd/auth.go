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

	"github.com/jedutools/gil/core/cliutil"
	"github.com/jedutools/gil/core/credstore"
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
// Decision tree:
//  1. If <provider> is missing, prompt with a numbered picker over
//     credstore.KnownProviders().
//  2. If --api-key is provided, use it directly. Otherwise read with
//     term.ReadPassword so the key never echoes to the terminal.
//  3. For vllm specifically, prompt for --base-url too, since vllm has no
//     canonical endpoint.
//  4. Validate the prefix non-fatally — wrong prefix is a warning, not a
//     blocker, because some users self-host gateways that proxy under a
//     different prefix.
func authLoginCmd() *cobra.Command {
	var apiKey, baseURL string
	c := &cobra.Command{
		Use:   "login [provider]",
		Short: "Log in to a provider (writes credentials to auth.json)",
		Long: `Add or update a credential for a provider.

If <provider> is omitted, you will be prompted to pick one. If --api-key is
omitted, you will be prompted with terminal echo disabled.

Examples:
  gil auth login                                    # interactive picker
  gil auth login anthropic                          # prompt for key
  gil auth login anthropic --api-key sk-ant-...     # non-interactive
  gil auth login vllm --base-url http://host:8000 --api-key local`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			provider, err := pickProvider(cmd, args)
			if err != nil {
				return err
			}

			key := apiKey
			if key == "" {
				key, err = readPassword(cmd, fmt.Sprintf("Enter API key for %s: ", provider))
				if err != nil {
					return cliutil.Wrap(err, "could not read API key", "try again, or pass --api-key")
				}
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return cliutil.New("API key is empty", `pass --api-key, or type a non-empty value when prompted`)
			}

			if warn := validateKeyPrefix(provider, key); warn != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", warn)
			}

			cred := credstore.Credential{Type: credstore.CredAPI, APIKey: key}
			if provider == credstore.VLLM {
				if baseURL == "" {
					baseURL, err = readLine(cmd, "BaseURL for vllm (e.g. http://localhost:8000/v1): ")
					if err != nil {
						return cliutil.Wrap(err, "could not read base URL", "")
					}
				}
				baseURL = strings.TrimSpace(baseURL)
				if baseURL == "" {
					return cliutil.New("vllm requires --base-url", `pass --base-url http://host:port/v1 (or type one when prompted)`)
				}
				cred.BaseURL = baseURL
			} else if baseURL != "" {
				// Allow custom base URL on any provider (e.g. proxies),
				// just don't require it.
				cred.BaseURL = baseURL
			}

			store := newStoreFor(cmd)
			if err := store.Set(ctx, provider, cred); err != nil {
				return cliutil.Wrap(err, "could not save credential", "check that "+authStorePath(cmd)+" is writable")
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Saved credential for %s (%s).\n", provider, cred.MaskedKey())
			return nil
		},
	}
	c.Flags().StringVar(&apiKey, "api-key", "", "API key (skips interactive prompt)")
	c.Flags().StringVar(&baseURL, "base-url", "", "base URL (vllm/custom endpoints)")
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
				return cliutil.Wrap(err, "could not read credentials", "")
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
			fmt.Fprintln(tw, "PROVIDER\tTYPE\tKEY\tBASE_URL\tUPDATED")
			for _, n := range names {
				cred, err := store.Get(ctx, n)
				if err != nil || cred == nil {
					continue
				}
				baseURL := cred.BaseURL
				if baseURL == "" {
					baseURL = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					n, cred.Type, cred.MaskedKey(), baseURL, formatTime(cred.Updated))
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
				return cliutil.Wrap(err, "could not read credentials", "")
			}
			if err := store.Remove(ctx, provider); err != nil {
				return cliutil.Wrap(err, "could not remove credential", "")
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
				return cliutil.Wrap(err, "could not read credentials", "")
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
					fmt.Fprintf(out, "  %-12s %s %s\n", n, cred.Type, cred.MaskedKey())
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
