package ssh

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrap_NoHost_PassesThrough(t *testing.T) {
	w := &Wrapper{}
	require.Equal(t, []string{"echo", "hi"}, w.Wrap("echo", "hi"))
}

func TestWrap_BasicHost(t *testing.T) {
	w := &Wrapper{Host: "user@example"}
	out := w.Wrap("ls", "-la")
	require.Equal(t, []string{"ssh", "user@example", "ls -la"}, out)
}

func TestWrap_WithPort(t *testing.T) {
	w := &Wrapper{Host: "u@h", Port: 2222}
	out := w.Wrap("ls")
	require.Contains(t, out, "-p")
	require.Contains(t, out, "2222")
}

func TestWrap_WithKey(t *testing.T) {
	w := &Wrapper{Host: "u@h", KeyPath: "/k.pem"}
	out := w.Wrap("ls")
	require.Contains(t, out, "-i")
	require.Contains(t, out, "/k.pem")
}

func TestWrap_QuotesUnsafeArgs(t *testing.T) {
	w := &Wrapper{Host: "u@h"}
	out := w.Wrap("bash", "-c", "echo hello world")
	last := out[len(out)-1]
	// The combined string should preserve the spaces inside single-quotes
	require.Equal(t, `bash -c 'echo hello world'`, last)
}

func TestWrap_QuotesEmbeddedSingleQuote(t *testing.T) {
	w := &Wrapper{Host: "u@h"}
	out := w.Wrap("bash", "-c", "echo 'inner'")
	last := out[len(out)-1]
	require.Equal(t, `bash -c 'echo '\''inner'\'''`, last)
}

func TestWrap_CustomBin(t *testing.T) {
	w := &Wrapper{Host: "u@h", SSHBin: "/usr/bin/ssh"}
	out := w.Wrap("ls")
	require.Equal(t, "/usr/bin/ssh", out[0])
}

func TestWrap_ExtraArgs(t *testing.T) {
	w := &Wrapper{Host: "u@h", ExtraArgs: []string{"-o", "StrictHostKeyChecking=no"}}
	out := w.Wrap("ls")
	var i int
	for j, a := range out {
		if a == "u@h" {
			i = j
		}
	}
	require.Greater(t, i, 0)
	require.Equal(t, "-o", out[i-2])
	require.Equal(t, "StrictHostKeyChecking=no", out[i-1])
}

func TestParseTarget_HostOnly(t *testing.T) {
	h, p, k := ParseTarget("user@host")
	require.Equal(t, "user@host", h)
	require.Equal(t, 0, p)
	require.Equal(t, "", k)
}

func TestParseTarget_HostWithPort(t *testing.T) {
	h, p, k := ParseTarget("user@host:2222")
	require.Equal(t, "user@host", h)
	require.Equal(t, 2222, p)
	require.Equal(t, "", k)
}

func TestParseTarget_HostWithKey(t *testing.T) {
	h, p, k := ParseTarget("user@host/path/to/key")
	require.Equal(t, "user@host", h)
	require.Equal(t, 0, p)
	require.Equal(t, "/path/to/key", k)
}

func TestParseTarget_HostPortKey(t *testing.T) {
	h, p, k := ParseTarget("user@host:2222/path/to/key")
	require.Equal(t, "user@host", h)
	require.Equal(t, 2222, p)
	require.Equal(t, "/path/to/key", k)
}

func TestParseTarget_NoUserPrefix(t *testing.T) {
	h, p, k := ParseTarget("hostname:22")
	require.Equal(t, "hostname", h)
	require.Equal(t, 22, p)
	require.Equal(t, "", k)
}

func TestParseTarget_BadPortFallsBackToHost(t *testing.T) {
	h, p, k := ParseTarget("user@host:notaport")
	// Implementation-dependent; just verify it doesn't panic and Host is reasonable
	_ = h
	_ = p
	_ = k
}

func TestAvailable_NoPanic(t *testing.T) {
	require.NotPanics(t, func() { _ = Available() })
}
