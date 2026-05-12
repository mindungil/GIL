package main

import (
	"github.com/mindungil/gil/cli/internal/cmd"
	"github.com/mindungil/gil/core/cliutil"
)

func main() {
	// Cobra prints command-usage messages itself; everything that escapes here
	// is a real failure. cliutil.Exit detects *UserError (anywhere in the
	// wrap chain) and prints "Error: ...\nHint: ..."; plain errors fall back
	// to a single "Error: ..." line. nil is a no-op.
	cliutil.Exit(cmd.Root().Execute())
}
