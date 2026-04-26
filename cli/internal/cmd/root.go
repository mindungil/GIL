package cmd

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// defaultBase returns the default gil data directory (~/.gil).
// If the user's home directory cannot be resolved, falls back to /tmp/gil.
func defaultBase() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/tmp/gil"
	}
	return filepath.Join(home, ".gil")
}

// defaultSocket returns the default path to the gild Unix Domain Socket.
func defaultSocket() string {
	return filepath.Join(defaultBase(), "gild.sock")
}

// Root returns the root cobra command for the gil CLI.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "gil",
		Short: "gil — autonomous coding harness",
	}
	root.AddCommand(daemonCmd())
	root.AddCommand(newCmd())
	root.AddCommand(statusCmd())
	return root
}
