package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/sdk"
)

func TestRenderCheckpointModal_Empty(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	out := renderCheckpointModal(60, CheckpointModalState{Open: true})
	require.Contains(t, out, "Checkpoints")
	require.Contains(t, out, "no checkpoints yet")
	require.Contains(t, out, "esc close")
}

func TestRenderCheckpointModal_WithEntries(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	st := CheckpointModalState{
		Open: true,
		Entries: []CheckpointEntry{
			{Step: 1, When: "18:01:23", Iter: 1, Summary: "baseline"},
			{Step: 2, When: "18:04:11", Iter: 3, Summary: "wired theme provider"},
			{Step: 3, When: "18:09:55", Iter: 7, Summary: "first 2 checks"},
		},
		Selected: 2,
	}
	out := renderCheckpointModal(80, st)
	require.Contains(t, out, "step")
	require.Contains(t, out, "when")
	require.Contains(t, out, "summary")
	require.Contains(t, out, "baseline")
	require.Contains(t, out, "wired theme provider")
	require.Contains(t, out, "first 2 checks")
	// Selected row marked by › (Arrow glyph).
	require.Contains(t, out, "›")
}

func TestRenderCheckpointModal_ErrorInline(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	st := CheckpointModalState{
		Open:    true,
		Entries: []CheckpointEntry{{Step: 1, When: "00:00:00", Iter: 1, Summary: "x"}},
		Error:   "session is RUNNING",
	}
	out := renderCheckpointModal(60, st)
	require.Contains(t, out, "session is RUNNING")
	require.Contains(t, out, "✗")
}

func TestExtractCheckpointEntries(t *testing.T) {
	events := []*tailEventLite{
		{Type: "iteration_start", When: "18:01:00", Iter: 1},
		{Type: "checkpoint_committed", When: "18:01:23", Iter: 1, Note: "baseline"},
		{Type: "iteration_start", When: "18:04:00", Iter: 3},
		{Type: "checkpoint_committed", When: "18:04:11", Iter: 3, Note: "wired theme"},
		{Type: "tool_call", When: "18:05:00", Iter: 3},
	}
	entries := extractCheckpointEntries(events)
	require.Len(t, entries, 2)
	require.Equal(t, 1, entries[0].Step)
	require.Equal(t, int32(1), entries[0].Iter)
	require.Equal(t, "baseline", entries[0].Summary)
	require.Equal(t, 2, entries[1].Step)
	require.Equal(t, int32(3), entries[1].Iter)
}

// TestUpdate_CKey_OpensCheckpointModal verifies the 'c' key opens the
// modal and restores correctly.
func TestUpdate_CKey_OpensCheckpointModal(t *testing.T) {
	m := &Model{
		keys: DefaultKeys(),
		sessions: []*sdk.Session{
			{ID: "s1", Status: "RUNNING"},
		},
		width: 100, height: 30,
	}
	m.slash = stubSlashState(m.sessions, 0, m)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	require.True(t, m2.(*Model).checkpoints.Open)
}

// TestUpdate_TKey_TogglesActivityFilter exercises the 't' shortcut.
func TestUpdate_TKey_TogglesActivityFilter(t *testing.T) {
	m := &Model{
		keys: DefaultKeys(),
		sessions: []*sdk.Session{{ID: "s1", Status: "RUNNING"}},
		width: 100, height: 30,
		activityFilter: FilterMilestones,
	}
	m.slash = stubSlashState(m.sessions, 0, m)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	require.Equal(t, FilterAll, m2.(*Model).activityFilter)
	m3, _ := m2.(*Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	require.Equal(t, FilterMilestones, m3.(*Model).activityFilter)
}

// TestCheckpointModal_NavAndClose drives ↑/↓/esc.
func TestCheckpointModal_NavAndClose(t *testing.T) {
	m := &Model{
		keys: DefaultKeys(),
		sessions: []*sdk.Session{{ID: "s1", Status: "DONE"}},
		width: 100, height: 30,
		checkpoints: CheckpointModalState{
			Open: true,
			Entries: []CheckpointEntry{
				{Step: 1, Summary: "a"},
				{Step: 2, Summary: "b"},
				{Step: 3, Summary: "c"},
			},
		},
	}
	m.slash = stubSlashState(m.sessions, 0, m)
	// Down twice
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 1, m2.(*Model).checkpoints.Selected)
	m3, _ := m2.(*Model).Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 2, m3.(*Model).checkpoints.Selected)
	// Down at end stays
	m4, _ := m3.(*Model).Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 2, m4.(*Model).checkpoints.Selected)
	// Up once
	m5, _ := m4.(*Model).Update(tea.KeyMsg{Type: tea.KeyUp})
	require.Equal(t, 1, m5.(*Model).checkpoints.Selected)
	// Esc closes.
	m6, _ := m5.(*Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.False(t, m6.(*Model).checkpoints.Open)
}

func TestRenderPermissionModal_AllOptions(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	ask := &pendingAskMsg{Tool: "bash", Key: "rm -rf /tmp/x"}
	out := renderPermissionModal(ask, 70)
	for _, snip := range []string{
		"Permission",
		"agent wants to run",
		"bash",
		"rm -rf /tmp/x",
		"[a] allow once",
		"[s] allow session",
		"[A] allow always",
		"[d] deny once",
		"[D] deny always",
		"[esc] cancel",
	} {
		require.Truef(t, strings.Contains(out, snip), "missing %q in:\n%s", snip, out)
	}
}
