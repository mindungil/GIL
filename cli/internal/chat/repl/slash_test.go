package repl

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseInput_BarePrompt(t *testing.T) {
	in, cmd, args := ParseInput("add dark mode toggle")
	require.Equal(t, InputPrompt, in)
	require.Equal(t, "", cmd)
	require.Equal(t, "add dark mode toggle", args)
}

func TestParseInput_SlashWithArgs(t *testing.T) {
	in, cmd, args := ParseInput("/switch add-dark-0428")
	require.Equal(t, InputSlash, in)
	require.Equal(t, "switch", cmd)
	require.Equal(t, "add-dark-0428", args)
}

func TestParseInput_SlashNoArgs(t *testing.T) {
	in, cmd, args := ParseInput("/sessions")
	require.Equal(t, InputSlash, in)
	require.Equal(t, "sessions", cmd)
	require.Equal(t, "", args)
}

func TestParseInput_BlankIsBlank(t *testing.T) {
	in, cmd, args := ParseInput("   ")
	require.Equal(t, InputBlank, in)
	require.Equal(t, "", cmd)
	require.Equal(t, "", args)
}

func TestKnownSlash_HasV1Set(t *testing.T) {
	for _, name := range []string{
		"sessions", "switch", "new", "quit",
		"spec", "status", "diff", "merge",
		"run", "help", "compact",
	} {
		require.True(t, IsKnownSlash(name), "missing slash: /%s", name)
	}
	require.False(t, IsKnownSlash("interrupt"), "/interrupt is V1.1")
	require.False(t, IsKnownSlash("stop"), "/stop is V1.1")
	require.False(t, IsKnownSlash("bogus"))
}
