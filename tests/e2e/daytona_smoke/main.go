// daytona_smoke spins up an in-process httptest fake of the Daytona REST
// API, runs the gil daytona Provider against it through the FULL stack
// (Provider → Client → Wrapper.ExecRemote ← bash tool's RemoteExecutor
// fast path), and asserts the result the agent loop would observe.
//
// This is a Go-test-style end-to-end check that lives outside the
// runtime/ module's test binary so the bash phase10_daytona_test.sh can
// invoke it as a single `go run` and check the exit code. No real
// network calls are made; api.daytona.io is never contacted.
//
// Exit code semantics:
//
//	0: every assertion passed, lifecycle Provision → Exec → Teardown OK
//	1: an assertion or an underlying call failed; details printed to stderr
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mindungil/gil/core/tool"
	"github.com/mindungil/gil/runtime/cloud"
	"github.com/mindungil/gil/runtime/daytona"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK: phase 10 e2e — Daytona REST driver under httptest")
}

func run() error {
	var (
		createCalls int32
		execCalls   int32
		deleteCalls int32
		gotAuth     atomic.Value // string
	)

	// Fake Daytona API: single httptest server with the four endpoints
	// the driver touches. Each handler bumps a counter so the smoke can
	// assert that every lifecycle phase actually called out.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/workspaces":
			atomic.AddInt32(&createCalls, 1)
			_ = json.NewEncoder(w).Encode(daytona.Workspace{ID: "ws-smoke", Status: "ready"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			atomic.AddInt32(&execCalls, 1)
			var req daytona.ExecRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			// Echo back the joined args so the caller can verify the
			// request body actually traversed the wire.
			_ = json.NewEncoder(w).Encode(daytona.ExecResult{
				Stdout: "hello from daytona: " + strings.Join(req.Args, " ") + "\n",
				Stderr: "",
				Exit:   0,
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/workspaces/"):
			atomic.AddInt32(&deleteCalls, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// 1. Set DAYTONA_* envs so Available() returns true.
	if err := os.Setenv(daytona.EnvAPIKey, "test-key"); err != nil {
		return fmt.Errorf("set api-key env: %w", err)
	}
	if err := os.Setenv(daytona.EnvAPIBase, srv.URL); err != nil {
		return fmt.Errorf("set api-base env: %w", err)
	}

	prov := daytona.New()
	if !prov.Available() {
		return fmt.Errorf("Provider.Available() should be true with %s set", daytona.EnvAPIKey)
	}

	// 2. Provision: end-to-end Create + (no poll needed since we returned
	//    "ready" immediately, but the path is exercised under e2e_test.go).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sb, err := prov.Provision(ctx, cloud.ProvisionOptions{
		SessionID: "smoke-1",
		Image:     "alpine:latest",
	})
	if err != nil {
		return fmt.Errorf("Provision: %w", err)
	}
	if got := atomic.LoadInt32(&createCalls); got != 1 {
		return fmt.Errorf("expected 1 create call, got %d", got)
	}
	if got, _ := gotAuth.Load().(string); got != "Bearer test-key" {
		return fmt.Errorf("expected Authorization=Bearer test-key, got %q", got)
	}
	if sb.Info["workspace"] != "gil-smoke-1" {
		return fmt.Errorf("expected workspace name gil-smoke-1, got %q", sb.Info["workspace"])
	}
	fmt.Printf("OK: provisioned workspace %s (id=%s, status=%s)\n",
		sb.Info["workspace"], sb.Info["workspace_id"], sb.Info["status"])

	// 3. Verify the Wrapper actually implements RemoteExecutor — that's
	//    the entire reason this driver exists. We check both the runtime/
	//    side interface and the core/tool side interface.
	if _, ok := sb.Wrapper.(cloud.RemoteExecutor); !ok {
		return fmt.Errorf("Wrapper does not implement cloud.RemoteExecutor")
	}
	re, ok := sb.Wrapper.(tool.RemoteExecutor)
	if !ok {
		return fmt.Errorf("Wrapper does not implement core/tool.RemoteExecutor")
	}

	// 4. Drive a command through Wrapper.ExecRemote — this is exactly
	//    the path the bash tool takes when its Wrapper satisfies
	//    RemoteExecutor.
	stdout, stderr, exit, err := re.ExecRemote(ctx, "bash", []string{"-c", "echo hi"})
	if err != nil {
		return fmt.Errorf("ExecRemote: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("expected exit=0, got %d (stderr=%q)", exit, stderr)
	}
	if !strings.Contains(stdout, "hello from daytona") {
		return fmt.Errorf("unexpected stdout: %q", stdout)
	}
	if got := atomic.LoadInt32(&execCalls); got != 1 {
		return fmt.Errorf("expected 1 exec call, got %d", got)
	}
	fmt.Printf("OK: ExecRemote returned exit=%d, stdout=%q\n", exit, strings.TrimSpace(stdout))

	// 5. End-to-end through the bash tool: build a *tool.Bash whose
	//    Wrapper is the daytona Wrapper, run it, and assert the result
	//    came back through the RemoteExecutor fast path (proven by the
	//    second exec call landing on the fake server).
	bash := &tool.Bash{Wrapper: sb.Wrapper.(tool.CommandWrapper)}
	res, err := bash.Run(ctx, json.RawMessage(`{"command":"uname -s"}`))
	if err != nil {
		return fmt.Errorf("bash.Run: %w", err)
	}
	if res.IsError {
		return fmt.Errorf("bash.Run returned IsError=true: %s", res.Content)
	}
	if !strings.Contains(res.Content, "exit=0") {
		return fmt.Errorf("bash.Run output missing exit=0: %s", res.Content)
	}
	if !strings.Contains(res.Content, "hello from daytona") {
		return fmt.Errorf("bash.Run output missing remote stdout: %s", res.Content)
	}
	if got := atomic.LoadInt32(&execCalls); got != 2 {
		return fmt.Errorf("expected 2 exec calls (manual + via bash tool), got %d", got)
	}
	fmt.Println("OK: bash tool routed through RemoteExecutor fast path")

	// 6. Teardown: DELETE /workspaces/{id} must fire exactly once.
	tdCtx, tdCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer tdCancel()
	if err := sb.Teardown(tdCtx); err != nil {
		return fmt.Errorf("Teardown: %w", err)
	}
	if got := atomic.LoadInt32(&deleteCalls); got != 1 {
		return fmt.Errorf("expected 1 delete call, got %d", got)
	}
	fmt.Println("OK: workspace torn down")

	return nil
}
