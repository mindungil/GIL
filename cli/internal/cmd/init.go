package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/cliutil"
	"github.com/mindungil/gil/core/credstore"
	"github.com/mindungil/gil/core/paths"
)

// configTOMLStub is the hand-rolled TOML written by `gil init` when no
// global config exists. We keep it hand-rolled (rather than pulling in a
// TOML encoder) because it's a static template and round-tripping a Go
// struct through marshalling buys us nothing here — the comments on each
// line are the load-bearing content for a first-run user.
//
// Reference lift:
//   - opencode's first-run scaffolding writes a similarly commented TOML
//     under the user's config dir.
//   - goose's `goose configure` walkthrough establishes the same defaults
//     contract: provider, model, autonomy, sandbox/backend.
const configTOMLStub = `# gil global config (https://github.com/mindungil/gil)
# Override per-project via <workspace>/.gil/config.toml (Phase 12)

[defaults]
provider = "anthropic"
model = ""                          # use provider default
workspace_backend = "LOCAL_NATIVE"  # LOCAL_NATIVE | LOCAL_SANDBOX | DOCKER | SSH | MODAL | DAYTONA
autonomy = "ASK_DESTRUCTIVE_ONLY"   # FULL | ASK_DESTRUCTIVE_ONLY | ASK_PER_ACTION | PLAN_ONLY
`

// initCmd returns the `gil init` subcommand.
//
// init is the user's entry-point on a fresh machine: it materialises the
// XDG-derived layout, writes a documented config.toml stub, runs the
// legacy ~/.gil migration if there's anything to migrate, and (unless
// --no-auth) drops them into `gil auth login` so they leave with at least
// one provider configured.
//
// Reference lift:
//   - aider's onboarding.py sequence — "no model? offer to log in; offer
//     docs URL on failure" — informs the auth-skip messaging here.
//   - goose's handle_first_time_setup banner + "rerun this command later"
//     reassurance — we keep the same user contract.
//
// Design rationale:
//   - init MUST work without the daemon running (we do not call
//     ensureDaemon). It's a local file operation only.
//   - All steps are idempotent: re-running init on a healthy install is a
//     no-op with informational output, never an error.
//   - --no-auth + --no-config make the command CI-safe: a scripted
//     installer can call `gil init --no-auth --no-config` to lay down
//     just the directory tree and exit.
func initCmd() *cobra.Command {
	var (
		noAuth   bool
		noConfig bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "first-time setup: create config dirs, write defaults, run auth login",
		Long: `Set up gil on a fresh machine.

This creates the XDG-standard config/data/state/cache directories, writes a
documented config.toml stub, migrates any pre-existing ~/.gil tree, and
(unless --no-auth) drops you into "gil auth login" to configure a provider.

init is idempotent: re-running it on a healthy install is a no-op with
informational output. Use --no-auth for CI/scripted installs that will
configure credentials separately.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			out := cmd.OutOrStdout()

			layout, err := paths.FromEnv()
			if err != nil {
				return cliutil.Wrap(err,
					"could not resolve gil's directory layout",
					`set GIL_HOME=<dir> to override, or check that HOME is set`)
			}

			fmt.Fprintf(out, "Setting up gil at %s\n\n", layout.Config)

			// 1. Materialise the four roots. EnsureDirs is idempotent
			//    (MkdirAll), so we report "Created:" for everything we
			//    actually had to create and "Already exists" otherwise.
			created, err := ensureLayoutWithReport(layout)
			if err != nil {
				return cliutil.Wrap(err,
					"could not create gil's directories",
					"check filesystem permissions on $HOME or $GIL_HOME")
			}
			printDirReport(out, layout, created)

			// 2. Config stub — only when --no-config is not set AND the
			//    file is absent. We never overwrite a user-edited config.
			cfgPath := layout.ConfigFile()
			if noConfig {
				fmt.Fprintln(out, "  (skipped config.toml; --no-config)")
			} else {
				wrote, err := writeConfigStubIfMissing(cfgPath)
				if err != nil {
					return cliutil.Wrap(err,
						fmt.Sprintf("could not write %s", cfgPath),
						"check filesystem permissions on the config dir")
				}
				if wrote {
					fmt.Fprintf(out, "  config file  %s\n", cfgPath)
				} else {
					fmt.Fprintf(out, "  config file  %s (already exists; not overwriting)\n", cfgPath)
				}
			}
			fmt.Fprintln(out)

			// 3. Legacy ~/.gil migration. The function is idempotent and
			//    a no-op when there's nothing to do; we only print the
			//    "Migrated:" block when something actually moved.
			moved, err := paths.MigrateLegacyTilde(layout)
			if err != nil {
				return cliutil.Wrap(err,
					"could not migrate legacy ~/.gil tree",
					"check that the legacy directory is readable, or move it aside manually")
			}
			if moved {
				fmt.Fprintln(out, "Migrated:")
				fmt.Fprintln(out, "  ~/.gil/sessions     -> "+layout.SessionsDir())
				fmt.Fprintln(out, "  ~/.gil/sessions.db  -> "+layout.SessionsDB())
				fmt.Fprintln(out, "  ~/.gil/gild.sock    -> "+layout.Sock())
				fmt.Fprintln(out, "  ~/.gil/gild.pid     -> "+layout.Pid())
				fmt.Fprintln(out, "  ~/.gil/shadow       -> "+layout.ShadowGitBase())
				fmt.Fprintln(out)
			} else {
				fmt.Fprintln(out, "No legacy ~/.gil to migrate.")
				fmt.Fprintln(out)
			}

			// 4. Provider credentials. We check the credstore (using the
			//    same path-resolution rules as `gil auth login`) before
			//    deciding whether to prompt. If the user already has at
			//    least one provider configured, we skip the prompt and
			//    surface a one-line confirmation instead — re-running
			//    init should never re-prompt for a key that's already
			//    saved.
			store := credstore.NewFileStore(layout.AuthFile())
			names, err := store.List(ctx)
			if err != nil {
				return cliutil.Wrap(err,
					"could not read credentials file",
					fmt.Sprintf("check that %s is readable", layout.AuthFile()))
			}

			switch {
			case len(names) > 0:
				fmt.Fprintf(out, "Auth: already configured (%d %s)\n\n",
					len(names), pluralProviders(len(names)))
			case noAuth:
				fmt.Fprintln(out, "Auth: skipped (--no-auth). Run \"gil auth login\" to configure a provider.")
				fmt.Fprintln(out)
			default:
				fmt.Fprintln(out, "Auth: launching \"gil auth login\" for first-time setup.")
				fmt.Fprintln(out)
				// Re-use the existing auth login subcommand. It owns
				// stdin/stdout/stderr already; we plumb the same streams
				// through so the prompt shows up where the user expects.
				login := authLoginCmd()
				login.SetIn(cmd.InOrStdin())
				login.SetOut(cmd.OutOrStdout())
				login.SetErr(cmd.ErrOrStderr())
				login.SetArgs(nil)
				if err := login.ExecuteContext(ctx); err != nil {
					// Don't bury the error — but do guide the user to
					// the recovery path. A failed/cancelled auth login
					// must NOT roll back the directories or config; the
					// next run of `gil init` (or `gil auth login`) will
					// pick up where we left off.
					fmt.Fprintln(out)
					fmt.Fprintln(out, "Auth setup did not complete; you can finish later with \"gil auth login\".")
					fmt.Fprintln(out)
				}
			}

			fmt.Fprintln(out, "Next steps:")
			fmt.Fprintln(out, "  1. Configure a provider:    gil auth login")
			fmt.Fprintln(out, "  2. Start an interview:      gil interview")
			fmt.Fprintln(out, "  3. See what's set up:       gil doctor")
			return nil
		},
	}
	cmd.Flags().BoolVar(&noAuth, "no-auth", false, "skip the auth login step (CI-friendly)")
	cmd.Flags().BoolVar(&noConfig, "no-config", false, "skip writing config.toml stub")
	return cmd
}

// ensureLayoutWithReport is EnsureDirs split into create-tracking. We need
// to know which dirs we actually created so the user-facing report can
// say "Created" vs nothing — paths.Layout.EnsureDirs alone returns no
// such info because mkdir -p is intentionally silent on existing dirs.
//
// It returns the set of paths newly created (others already existed) and
// the first error, if any.
func ensureLayoutWithReport(l paths.Layout) (map[string]bool, error) {
	created := map[string]bool{}
	for _, d := range []string{l.Config, l.Data, l.State, l.Cache} {
		// Stat first; mkdir -p doesn't tell us whether it had to do
		// anything, so we record presence pre-call.
		_, err := os.Stat(d)
		existed := err == nil
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return created, fmt.Errorf("init: stat %s: %w", d, err)
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return created, fmt.Errorf("init: mkdir %s: %w", d, err)
		}
		if !existed {
			created[d] = true
		}
	}
	return created, nil
}

// printDirReport renders the per-directory line, using "Created:" once
// for the section header and labelling each dir either as freshly
// created or already-existing. Two-column alignment is hand-tuned (12
// chars for the label) so the output is stable without bringing in
// tabwriter — the section is small and the labels are fixed-width by
// design.
func printDirReport(w interface{ Write(p []byte) (int, error) }, l paths.Layout, created map[string]bool) {
	type row struct {
		label, path string
	}
	rows := []row{
		{"config dir", l.Config},
		{"data dir", l.Data},
		{"state dir", l.State},
		{"cache dir", l.Cache},
	}
	anyCreated := false
	for _, r := range rows {
		if created[r.path] {
			anyCreated = true
			break
		}
	}
	if anyCreated {
		fmt.Fprintln(w, "Created:")
	} else {
		fmt.Fprintln(w, "Layout (all dirs already exist):")
	}
	for _, r := range rows {
		marker := ""
		if !created[r.path] {
			marker = " (existing)"
		}
		fmt.Fprintf(w, "  %-12s %s%s\n", r.label, r.path, marker)
	}
}

// writeConfigStubIfMissing writes configTOMLStub to path with mode 0600
// only when the file does not yet exist. Returns (true, nil) when it
// wrote, (false, nil) when the file already existed (idempotent skip),
// and any I/O error. We pick 0600 because config.toml may grow
// provider-specific knobs later that the user prefers to keep private.
func writeConfigStubIfMissing(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(configTOMLStub), 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// pluralProviders gives the right English form for the auth-already-
// configured one-liner. Trivial helper, but it keeps the call-site
// readable.
func pluralProviders(n int) string {
	if n == 1 {
		return "provider"
	}
	return "providers"
}
