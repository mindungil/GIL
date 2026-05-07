// prompt_smoke is a hand-run smoke binary that exercises the daemon's
// SessionService.Prompt RPC end-to-end: it dials the local gild socket,
// sends a natural-language prompt, and prints every Part the agent loop
// streams back (TextDelta, ToolCallPart, ToolResultPart, etc.).
//
// Usage:
//   go run ./tests/dogfood/prompt_smoke "최근에 내가 뭐 작업했어?"
//
// This is the post-M3 replacement for the deleted interview-flow probes;
// nothing in CI runs it because it requires a live provider endpoint.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: prompt_smoke <prompt text...>")
		os.Exit(2)
	}
	text := strings.Join(os.Args[1:], " ")

	sock := os.Getenv("GIL_SOCKET")
	if sock == "" {
		sock = "/home/ubuntu/.local/state/gil/gild.sock"
	}

	cli, err := sdk.Dial(sock)
	if err != nil {
		die("dial:", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sid := os.Getenv("GIL_SESSION")
	agent := os.Getenv("GIL_AGENT")
	stream, err := cli.Prompt(ctx, sdk.PromptOptions{SessionID: sid, Text: text, Agent: agent})
	if err != nil {
		die("prompt:", err)
	}

	fmt.Printf(">> user: %s\n\n---- stream ----\n", text)
	tools := 0
	textBytes := 0
	turns := 0
	for {
		part, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			die("recv:", err)
		}
		switch b := part.GetBody().(type) {
		case *gilv1.Part_Text:
			textBytes += len(b.Text.Content)
			fmt.Print(b.Text.Content)
		case *gilv1.Part_ToolCall:
			tools++
			fmt.Printf("\n[tool.call #%d] %s id=%s args=%s\n",
				tools, b.ToolCall.Name, b.ToolCall.Id, b.ToolCall.InputJson)
		case *gilv1.Part_ToolResult:
			fmt.Printf("[tool.result] call_id=%s err=%v body=%s\n",
				b.ToolResult.CallId, b.ToolResult.IsError, truncate(b.ToolResult.Content, 240))
		case *gilv1.Part_SessionAllocated:
			fmt.Printf("[session.allocated] id=%s\n", b.SessionAllocated.SessionId)
		case *gilv1.Part_Metrics:
			turns++
			fmt.Printf("\n[metrics] tokens_in=%d tokens_out=%d cost=$%.5f latency=%dms\n",
				b.Metrics.TokensIn, b.Metrics.TokensOut, b.Metrics.CostUsd, b.Metrics.LatencyMs)
		case *gilv1.Part_Done:
			fmt.Printf("\n[done] stop=%s err=%s\n", b.Done.StopReason, b.Done.ErrorMessage)
		default:
			fmt.Printf("\n[unknown part type %T]\n", b)
		}
	}
	fmt.Printf("\n---- summary: tools=%d, text_bytes=%d, turns=%d ----\n", tools, textBytes, turns)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(+" + fmt.Sprint(len(s)-n) + "B)"
}

func die(prefix string, err error) {
	fmt.Fprintln(os.Stderr, prefix, err)
	os.Exit(1)
}
