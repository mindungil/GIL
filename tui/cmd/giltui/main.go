package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/core/version"
	"github.com/mindungil/gil/tui/internal/app"
)

func main() {
	// --version handled before any side-effect (TUI alt-screen init,
	// socket dial). Matches the gil/gild/gilmcp shape: "<binary> vX.Y.Z".
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *versionFlag {
		fmt.Fprintf(os.Stdout, "giltui %s\n", version.String())
		return
	}

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
