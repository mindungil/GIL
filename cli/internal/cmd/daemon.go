package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// daemonCmd returns the "daemon" subcommand, which in Phase 1 simply shows
// guidance on how to start the gild daemon manually.
func daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Show how to start the gild daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("In Phase 1, start gild manually:")
			fmt.Println()
			fmt.Println("  gild --foreground")
			fmt.Println()
			fmt.Println("Default socket:", defaultSocket())
			fmt.Println("(automatic spawn arrives in Phase 2)")
			return nil
		},
	}
}
