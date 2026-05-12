// oidc_client is a tiny gRPC test client used by the Phase 10 Track E e2e
// test. It dials gild's TCP listener, optionally attaches a Bearer token from
// a file, and calls SessionService.List. Exit code maps to gRPC outcome:
//
//	0 — RPC succeeded (OK)
//	2 — Unauthenticated
//	3 — other error
//
// The harness asserts these exit codes to confirm middleware behaviour.
//
// Usage:
//
//	oidc_client -addr 127.0.0.1:7070                    # no token
//	oidc_client -addr 127.0.0.1:7070 -token-file <path> # with token
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7070", "gild gRPC TCP addr")
	tokenFile := flag.String("token-file", "", "if set, read bearer token from this file")
	flag.Parse()

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(3)
	}
	defer conn.Close()

	client := gilv1.NewSessionServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if *tokenFile != "" {
		raw, err := os.ReadFile(*tokenFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read token-file:", err)
			os.Exit(3)
		}
		tok := strings.TrimSpace(string(raw))
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	}

	resp, err := client.List(ctx, &gilv1.ListRequest{})
	if err != nil {
		code := status.Code(err)
		fmt.Fprintf(os.Stderr, "list rpc: code=%s msg=%s\n", code, err)
		switch code {
		case codes.Unauthenticated:
			os.Exit(2)
		default:
			os.Exit(3)
		}
	}
	fmt.Printf("OK list returned %d sessions\n", len(resp.Sessions))
}
