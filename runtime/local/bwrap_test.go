package local

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// containsSequence returns true if needle appears as a contiguous
// subsequence within haystack.
func containsSequence(haystack, needle []string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
outer:
	for i := 0; i <= len(haystack)-len(needle); i++ {
		for j, v := range needle {
			if haystack[i+j] != v {
				continue outer
			}
		}
		return true
	}
	return false
}

func TestSandbox_FullAccess_PassesThrough(t *testing.T) {
	s := &Sandbox{Mode: ModeFullAccess}
	got := s.Wrap("echo", "hi")
	assert.Equal(t, []string{"echo", "hi"}, got)
}

func TestSandbox_ReadOnly_BuildsExpectedArgs(t *testing.T) {
	s := &Sandbox{Mode: ModeReadOnly}
	got := s.Wrap("echo", "hi")

	require.Greater(t, len(got), 0, "result must not be empty")
	assert.Equal(t, "bwrap", got[0], "first element must be bwrap binary")

	assert.True(t, containsSequence(got, []string{"--ro-bind", "/", "/"}),
		"must contain --ro-bind / /")
	assert.True(t, containsSequence(got, []string{"--tmpfs", "/tmp"}),
		"must contain --tmpfs /tmp")

	// No --bind flag (writable workspace bind must be absent in ReadOnly).
	for i, v := range got {
		if v == "--bind" {
			t.Errorf("unexpected --bind at index %d", i)
		}
	}

	// Ends with -- echo hi.
	assert.True(t, containsSequence(got, []string{"--", "echo", "hi"}),
		"must end with -- echo hi")
	n := len(got)
	assert.Equal(t, []string{"--", "echo", "hi"}, got[n-3:],
		"last three elements must be -- echo hi")
}

func TestSandbox_WorkspaceWrite_BindsWorkspace(t *testing.T) {
	s := &Sandbox{Mode: ModeWorkspaceWrite, WorkspaceDir: "/work"}
	got := s.Wrap("echo", "hi")

	assert.True(t, containsSequence(got, []string{"--bind", "/work", "/work"}),
		"must contain --bind /work /work")
}

func TestSandbox_WorkspaceWrite_EmptyWorkspaceOmitsBind(t *testing.T) {
	s := &Sandbox{Mode: ModeWorkspaceWrite, WorkspaceDir: ""}
	got := s.Wrap("echo", "hi")

	for i, v := range got {
		if v == "--bind" {
			t.Errorf("unexpected --bind at index %d when WorkspaceDir is empty", i)
		}
	}
}

func TestSandbox_CustomBwrapPath(t *testing.T) {
	s := &Sandbox{
		Mode:      ModeReadOnly,
		BwrapPath: "/usr/local/bin/bwrap",
	}
	got := s.Wrap("echo", "hi")

	require.Greater(t, len(got), 0)
	assert.Equal(t, "/usr/local/bin/bwrap", got[0])
}

func TestSandbox_ExtraReadOnlyBinds(t *testing.T) {
	s := &Sandbox{
		Mode:               ModeReadOnly,
		ExtraReadOnlyBinds: [][2]string{{"/src", "/dst"}},
	}
	got := s.Wrap("echo", "hi")

	assert.True(t, containsSequence(got, []string{"--ro-bind", "/src", "/dst"}),
		"must contain --ro-bind /src /dst")
}

func TestMode_String(t *testing.T) {
	cases := []struct {
		mode Mode
		want string
	}{
		{ModeReadOnly, "read_only"},
		{ModeWorkspaceWrite, "workspace_write"},
		{ModeFullAccess, "full_access"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.mode.String(), "Mode(%d).String()", tc.mode)
	}
}

func TestAvailable_DoesNotPanic(t *testing.T) {
	// bwrap is likely not installed on CI; we only assert no panic.
	assert.NotPanics(t, func() {
		Available()
	})
}
