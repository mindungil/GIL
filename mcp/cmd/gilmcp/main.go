package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jedutools/gil/core/mcp/jsonrpc"
	"github.com/jedutools/gil/core/paths"
	"github.com/jedutools/gil/mcp/internal/server"
	"github.com/jedutools/gil/sdk"
)

func main() {
	socket := flag.String("socket", defaultSocket(), "gild UDS socket path")
	flag.Parse()

	cli, err := sdk.Dial(*socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gilmcp: failed to dial gild:", err)
		os.Exit(1)
	}
	defer cli.Close()

	srv := &server.Server{Client: cli}
	transport := jsonrpc.NewTransport(os.Stdin, os.Stdout, srv.Handle)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		cancel()
	}()

	if err := transport.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "gilmcp:", err)
		os.Exit(1)
	}
}

// defaultSocket resolves the gild UDS path through the same XDG-aware
// layout the rest of gil uses (paths.FromEnv → State/gild.sock). Falls
// back to /tmp/gil/gild.sock only when HOME cannot be resolved at all,
// which preserves prior behaviour for minimal containers without a
// home directory.
func defaultSocket() string {
	l, err := paths.FromEnv()
	if err != nil {
		return "/tmp/gil/gild.sock"
	}
	return l.Sock()
}
