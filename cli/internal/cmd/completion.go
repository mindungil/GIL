package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newCompletionCmd returns the "completion" subcommand which prints a
// shell completion script for the requested shell to stdout.
//
// Cobra already implements the heavy lifting (GenBashCompletionV2,
// GenZshCompletion, GenFishCompletion, GenPowerShellCompletion); this
// command is a thin, discoverable wrapper that mirrors the surface
// other harnesses expose (e.g. `codex completion <shell>`).
func newCompletionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "completion <bash|zsh|fish|powershell>",
		Short: "generate shell completion script",
		Long: `Output shell completion script for the given shell.

Install:
  bash: gil completion bash | sudo tee /etc/bash_completion.d/gil
  zsh:  gil completion zsh > "${fpath[1]}/_gil"
  fish: gil completion fish > ~/.config/fish/completions/gil.fish
  powershell: gil completion powershell | Out-String | Invoke-Expression`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(out, true)
			case "zsh":
				return root.GenZshCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			case "powershell":
				return root.GenPowerShellCompletion(out)
			}
			return fmt.Errorf("unknown shell: %s", args[0])
		},
	}
}
