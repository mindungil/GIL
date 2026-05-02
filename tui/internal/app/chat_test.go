package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChatModel_InitialPhase_IsIdle(t *testing.T) {
	m := newChatModelForTest()
	if m.phase != ChatPhaseIdle {
		t.Fatalf("expected initial phase ChatPhaseIdle, got %v", m.phase)
	}
}

func TestChatModel_WindowSize_StoresDimensions(t *testing.T) {
	m := newChatModelForTest()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	cm := updated.(*chatModel)
	if cm.width != 100 || cm.height != 32 {
		t.Fatalf("expected width=100 height=32, got width=%d height=%d", cm.width, cm.height)
	}
}

// newChatModelForTest constructs a chatModel without dialing the daemon.
// All gRPC-dependent fields are left nil; tests that need them inject
// their own fake stream.
func newChatModelForTest() *chatModel {
	return &chatModel{phase: ChatPhaseIdle}
}
