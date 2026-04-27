package app

import "github.com/charmbracelet/bubbles/key"

// KeyMap holds the key bindings for the TUI.
type KeyMap struct {
	Quit    key.Binding
	Refresh key.Binding
	Up      key.Binding
	Down    key.Binding
	Enter   key.Binding
	// Slash opens the slash-command input prompt. We bind both "/" and
	// ":" because users coming from vi-style TUIs reach for ":" first.
	Slash key.Binding
	// DismissSlash dismisses the previous slash-command output panel
	// when no input prompt is open.
	DismissSlash key.Binding

	// Phase 14 additions.
	Checkpoints  key.Binding // 'c' opens the checkpoint timeline modal
	ToggleFilter key.Binding // 't' toggles activity filter (milestones ↔ all)
}

// DefaultKeys returns the default key bindings.
func DefaultKeys() KeyMap {
	return KeyMap{
		Quit:         key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Refresh:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Up:           key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("k/↑", "up")),
		Down:         key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("j/↓", "down")),
		Enter:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Slash:        key.NewBinding(key.WithKeys("/", ":"), key.WithHelp("/", "command")),
		DismissSlash: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "dismiss")),
		Checkpoints:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "checkpoints")),
		ToggleFilter: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "toggle activity filter")),
	}
}
