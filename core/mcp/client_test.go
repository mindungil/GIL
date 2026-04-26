package mcp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClient_RemoteTool_Wraps(t *testing.T) {
	info := RemoteToolInfo{
		Name:        "echo",
		Description: "echoes",
		InputSchema: map[string]any{"type": "object"},
	}
	rt := &RemoteTool{Info: info}
	require.Equal(t, "echo", rt.Name())
	require.Equal(t, "echoes", rt.Description())
	var s map[string]any
	require.NoError(t, json.Unmarshal(rt.Schema(), &s))
	require.Equal(t, "object", s["type"])
}

// Full subprocess test using a tiny inline Go program written to a temp
// file at test time, then `go run`. Skipped when go is not in PATH.
func TestClient_LaunchRealSubprocess(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	// Write a tiny Go program that responds to initialize + tools/list + tools/call.
	src := `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type req struct {
	Jsonrpc string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id,omitempty\"`" + `
	Method  string          ` + "`json:\"method\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if len(r.ID) == 0 {
			continue
		}
		var result any
		switch r.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "echo", "version": "0.0.1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "echoes", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "echoed"}}}
		default:
			result = map[string]any{}
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result})
		fmt.Fprintln(os.Stdout, string(out))
	}
}
`
	dir := t.TempDir()
	srcPath := dir + "/main.go"
	require.NoError(t, os.WriteFile(srcPath, []byte(src), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := Launch(ctx, LaunchOptions{
		Command: "go",
		Args:    []string{"run", srcPath},
	})
	require.NoError(t, err)
	defer cli.Close()

	require.NoError(t, cli.Initialize(ctx))

	tools, err := cli.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.Equal(t, "echo", tools[0].Name)

	text, isErr, err := cli.CallTool(ctx, "echo", map[string]any{"x": 1})
	require.NoError(t, err)
	require.Equal(t, "echoed", text)
	require.False(t, isErr)
}

func TestClient_Close_Idempotent(t *testing.T) {
	// No Launch; just synthesize a Client to test Close behavior.
	pr, pw := io.Pipe()
	c := &Client{stdin: pw, stdout: pr, done: make(chan struct{})}
	close(c.done) // simulate readPump exit
	require.NoError(t, c.Close())
	require.NoError(t, c.Close()) // second call is a no-op
}
