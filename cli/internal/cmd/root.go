package cmd

import (
	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/version"
)

// outputFormat is the value of the persistent `--output` flag wired in
// Root(). Subcommands consult it via outputJSON() so we do not have to
// thread a per-command boolean through every RunE. Valid values are
// "text" (default) and "json"; unknown values fall through to text so
// adding a new format later is forward-compatible.
//
// The variable is package-scoped because cobra's PersistentFlags binding
// requires a stable address. Tests reset it to "text" via the helper
// resetOutputFormatForTest at the bottom of root.go.
var outputFormat = "text"

// asciiMode is the persistent --ascii flag. When set the visual
// surfaces (gil, gil watch, gil status visual mode) swap their
// Unicode glyphs for the ASCII fallbacks defined in
// cli/internal/cmd/uistyle/glyph.go. The default keeps the spec's
// Unicode set per terminal-aesthetic.md §3.
var asciiMode = false

// outputJSON reports whether the user asked for JSON via the persistent
// --output flag. We compare case-insensitively so `--output JSON` works
// the same as `--output json` (matches goose/codex tolerance).
func outputJSON() bool {
	switch outputFormat {
	case "json", "JSON", "Json":
		return true
	default:
		return false
	}
}

// resetOutputFormatForTest restores the package-level outputFormat to its
// default. Tests that mutate the flag (or that exercise multiple commands
// in one process) call this in t.Cleanup so a stale "json" value from a
// previous test does not bleed into a sibling.
func resetOutputFormatForTest() {
	outputFormat = "text"
	asciiMode = false
}

// defaultLayout returns the XDG-derived layout (or the GIL_HOME single-
// tree override when set). It silently falls back to /tmp/gil/* if the
// user's HOME cannot be resolved at all — in practice that only happens
// inside the most minimal containers, and we never want gil to refuse
// to start because of it.
func defaultLayout() paths.Layout {
	l, err := paths.FromEnv()
	if err != nil {
		return paths.Layout{
			Config: "/tmp/gil/config",
			Data:   "/tmp/gil/data",
			State:  "/tmp/gil/state",
			Cache:  "/tmp/gil/cache",
		}
	}
	return l
}

// defaultBase returns the State root, used by ensureDaemon to mkdir the
// area before exec'ing gild and to locate the socket. It is a thin
// shim during the Layout migration so existing single-string callers
// (resume.go, run.go, …) keep compiling untouched.
func defaultBase() string {
	return defaultLayout().State
}

// defaultSocket returns the default path to the gild Unix Domain Socket.
func defaultSocket() string {
	return defaultLayout().Sock()
}

// Root returns the root cobra command for the gil CLI.
//
// SilenceUsage / SilenceErrors are set so Cobra does not print the usage
// banner or its own "Error: ..." line on a RunE failure. Error presentation
// is owned by main.go via cliutil.Exit, which emits the user-facing Msg+Hint
// pair (or just the message for non-UserError values). Without these flags
// every failure prints the error twice — once by Cobra, once by Exit.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:           "gil",
		Short:         "gil — autonomous coding harness",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Version powers cobra's built-in `gil --version` / `gil -v`
		// flag handling. Setting it here (not via main) keeps the wiring
		// in one place; the underlying string comes from the shared
		// core/version package, which is stamped via -ldflags at build
		// time and falls back to runtime/debug.BuildInfo otherwise.
		Version: version.String(),
		// Args=NoArgs forbids `gil <unknown>` (cobra would otherwise
		// emit "unknown command"); we want our own tighter error path,
		// and the no-arg case is handled by RunE below.
		Args: cobra.NoArgs,
		// RunE only fires on bare `gil` (no subcommand, no `--help`,
		// no `--version`). Cobra resolves --help / --version itself
		// before this hook, so the mission-control summary is the
		// only thing the user sees when they type `gil` alone.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSummary(cmd.OutOrStdout(), defaultSocket(), defaultBase(), asciiMode)
		},
	}
	// SetVersionTemplate strips cobra's default "gil version vX.Y.Z\n"
	// banner in favour of just the version line — matches the goose /
	// codex shape and is friendlier to scripts that read --version
	// output directly.
	root.SetVersionTemplate("gil {{.Version}}\n")
	// Mirror the build-time version into the doctor package so its
	// header line and JSON output use the same source of truth as
	// `gil --version`. Without this, doctor would still rely on
	// runtime/debug.BuildInfo for release builds, missing the
	// -ldflags-stamped value.
	SetVersion(version.Short())
	// Persistent --output flag (Phase 12, Track G / T13). Subcommands
	// that have a structured form check outputJSON() and emit JSON
	// instead of the human table. Default "text" preserves the existing
	// CLI surface 1:1; unknown values fall through to text.
	root.PersistentFlags().StringVar(&outputFormat, "output", "text", "output format: text|json")
	// --ascii is the global toggle for the Unicode glyph set used by
	// the visual surfaces (no-arg summary, watch, status). Off by
	// default so the spec aesthetic ships out of the box; users on
	// terminals without a Unicode font opt in (LANG=C is also a
	// reasonable trigger but we leave that to the caller; the env
	// variable does not auto-flip the flag).
	root.PersistentFlags().BoolVar(&asciiMode, "ascii", false, "use ASCII fallback glyphs (no Unicode)")
	root.AddCommand(daemonCmd())
	root.AddCommand(authCmd())
	root.AddCommand(initCmd())
	root.AddCommand(doctorCmd())
	root.AddCommand(newCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(sessionCmd())
	root.AddCommand(interviewCmd())
	root.AddCommand(resumeCmd())
	root.AddCommand(specCmd())
	root.AddCommand(runCmd())
	root.AddCommand(eventsCmd())
	root.AddCommand(watchCmd())
	root.AddCommand(exportCmd())
	root.AddCommand(importCmd())
	root.AddCommand(restoreCmd())
	root.AddCommand(costCmd())
	root.AddCommand(statsCmd())
	root.AddCommand(mcpCmd())
	root.AddCommand(permissionsCmd())
	root.AddCommand(updateCmd())
	root.AddCommand(newCompletionCmd(root))
	return root
}
