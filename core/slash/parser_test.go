package slash

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLine_Basic(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Command
		matched bool
	}{
		{"plain", "/help", Command{Name: "help", Args: nil, Raw: "/help"}, true},
		{"with-arg", "/model gpt-4o", Command{Name: "model", Args: []string{"gpt-4o"}, Raw: "/model gpt-4o"}, true},
		{"multi-args", "/cost --json -v", Command{Name: "cost", Args: []string{"--json", "-v"}, Raw: "/cost --json -v"}, true},
		{"leading-spaces", "   /status", Command{Name: "status", Args: nil, Raw: "/status"}, true},
		{"embedded-not-allowed", "hello /help", Command{}, false},
		{"empty", "", Command{}, false},
		{"just-slash", "/", Command{}, false},
		{"slash-spaces", "/   ", Command{}, false},
		{"no-slash", "help", Command{}, false},
		{"tabs-between-args", "/diff\tstaged\tHEAD", Command{Name: "diff", Args: []string{"staged", "HEAD"}, Raw: "/diff\tstaged\tHEAD"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseLine(tc.in)
			require.Equal(t, tc.matched, ok)
			if tc.matched {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestRegistry_RegisterLookupList(t *testing.T) {
	r := NewRegistry()
	r.Register(Spec{Name: "help", Summary: "show help", Handler: func(context.Context, Command) (string, error) { return "ok", nil }})
	r.Register(Spec{Name: "quit", Aliases: []string{"exit", "q"}, Summary: "exit", Handler: func(context.Context, Command) (string, error) { return "", nil }})

	// Lookup canonical
	s, ok := r.Lookup("help")
	require.True(t, ok)
	require.Equal(t, "help", s.Name)

	// Case-insensitive
	s, ok = r.Lookup("HELP")
	require.True(t, ok)
	require.Equal(t, "help", s.Name)

	// Alias resolution
	s, ok = r.Lookup("exit")
	require.True(t, ok)
	require.Equal(t, "quit", s.Name)
	s, ok = r.Lookup("q")
	require.True(t, ok)
	require.Equal(t, "quit", s.Name)

	// Unknown
	_, ok = r.Lookup("nope")
	require.False(t, ok)

	// List sorted, no alias duplicates
	list := r.List()
	require.Len(t, list, 2)
	require.Equal(t, "help", list[0].Name)
	require.Equal(t, "quit", list[1].Name)
}

func TestRegistry_RegisterEmptyName(t *testing.T) {
	r := NewRegistry()
	r.Register(Spec{Name: "  ", Handler: func(context.Context, Command) (string, error) { return "", nil }})
	require.Empty(t, r.List())
}

func TestRegistry_HandlerErrorRoundtrip(t *testing.T) {
	r := NewRegistry()
	sentinel := errors.New("boom")
	r.Register(Spec{Name: "x", Handler: func(context.Context, Command) (string, error) { return "", sentinel }})
	s, ok := r.Lookup("x")
	require.True(t, ok)
	_, err := s.Handler(context.Background(), Command{Name: "x"})
	require.ErrorIs(t, err, sentinel)
}
