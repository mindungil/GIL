package app

import "github.com/charmbracelet/bubbles/key"

// KeyMap holds the key bindings for the TUI.
type KeyMap struct {
	Quit    key.Binding
	Refresh key.Binding
	Up      key.Binding
	Down    key.Binding
	Enter   key.Binding
}

// DefaultKeys returns the default key bindings.
func DefaultKeys() KeyMap {
	return KeyMap{
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("k/↑", "up")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("j/↓", "down")),
		Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	}
}
