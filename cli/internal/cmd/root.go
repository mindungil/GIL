package cmd

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// defaultBase returns the default base directory for gil data (~/.gil).
func defaultBase() string {
	home, _ := os.UserHomeDir()
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
	return root
}
