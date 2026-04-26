package cmd

import (
	"github.com/spf13/cobra"

	"github.com/jedutools/gil/core/paths"
)

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
	}
	root.AddCommand(daemonCmd())
	root.AddCommand(newCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(interviewCmd())
	root.AddCommand(resumeCmd())
	root.AddCommand(specCmd())
	root.AddCommand(runCmd())
	root.AddCommand(eventsCmd())
	root.AddCommand(restoreCmd())
	root.AddCommand(newCompletionCmd(root))
	return root
}
