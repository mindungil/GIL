package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// runCompletion executes `gil completion <shell>` against a fresh root and
// returns the captured stdout/stderr buffer plus the resulting error.
func runCompletion(t *testing.T, shell string) (string, error) {
	t.Helper()
	root := Root()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	args := []string{"completion"}
	if shell != "" {
		args = append(args, shell)
	}
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

func TestCompletion_Bash(t *testing.T) {
	out, err := runCompletion(t, "bash")
	require.NoError(t, err)
	require.NotEmpty(t, out)
	// Cobra emits helper functions prefixed with __<root>_ (e.g. __gil_debug);
	// "_gil_" appears as a substring in those identifiers.
	require.Contains(t, out, "_gil_")
}

func TestCompletion_Zsh(t *testing.T) {
	out, err := runCompletion(t, "zsh")
	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Contains(t, out, "#compdef gil")
}

func TestCompletion_Fish(t *testing.T) {
	out, err := runCompletion(t, "fish")
	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Contains(t, out, "complete -c gil")
}

func TestCompletion_PowerShell(t *testing.T) {
	out, err := runCompletion(t, "powershell")
	require.NoError(t, err)
	require.NotEmpty(t, out)
	// PowerShell completion script registers an argument completer for `gil`.
	require.Contains(t, strings.ToLower(out), "gil")
}

func TestCompletion_RejectsInvalidShell(t *testing.T) {
	_, err := runCompletion(t, "tcsh")
	require.Error(t, err)
}

func TestCompletion_RequiresArg(t *testing.T) {
	_, err := runCompletion(t, "")
	require.Error(t, err)
}

// TestCompletion_RegisteredOnRoot ensures the subcommand is wired into Root().
func TestCompletion_RegisteredOnRoot(t *testing.T) {
	root := Root()
	var found *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			found = c
			break
		}
	}
	require.NotNil(t, found, "completion subcommand must be registered on root")
	require.ElementsMatch(t, []string{"bash", "zsh", "fish", "powershell"}, found.ValidArgs)
}
