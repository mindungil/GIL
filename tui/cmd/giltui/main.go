package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jedutools/gil/tui/internal/app"
)

func main() {
	socket := app.DefaultSocket()
	if v := os.Getenv("GIL_SOCKET"); v != "" {
		socket = v
	}
	m, err := app.New(socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "giltui:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "giltui:", err)
		os.Exit(1)
	}
}
