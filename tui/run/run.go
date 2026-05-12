// Package run is the public entry point for launching the gil chat
// TUI from outside the tui module. The cli module imports this
// instead of reaching into tui/internal/app, keeping the bubbletea
// surface of the tui module fully internal.
package run

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/sdk"
	"github.com/mindungil/gil/tui/internal/app"
)

// Chat dials the gild socket and runs the prompt-centric chat TUI
// until the user exits or ctx is cancelled. Returns the program
// error, if any.
func Chat(ctx context.Context, socket string) error {
	cli, err := sdk.Dial(socket)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer cli.Close()

	m := app.NewChatModelForRun(socket, cli)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
}
