package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	_ "modernc.org/sqlite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/jedutools/gil/server/internal/service"
	"github.com/jedutools/gil/server/internal/uds"
)

// server wraps a gRPC server, Unix Domain Socket listener, and SQLite database,
// providing unified startup and graceful shutdown for the gild daemon.
type server struct {
	grpc *grpc.Server
	lis  net.Listener
	db   *sql.DB
}

// newServer creates and initializes a gRPC server listening on sockPath with a SQLite
// database at dbPath. It returns an error if the database cannot be opened, migrations
// fail, the socket cannot be created, or gRPC registration fails. Partial failures
// are cleaned up: if migration or socket creation fails, the database is closed.
func newServer(dbPath, sockPath, sessionsBase string) (*server, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if err := session.Migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	lis, err := uds.Listen(sockPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	// Factory provides mock or anthropic provider based on name
	factory := func(name string) (provider.Provider, string, error) {
		switch name {
		case "mock":
			switch os.Getenv("GIL_MOCK_MODE") {
			case "run-hello":
				// MockToolProvider with scripted hello-world turns for RunService
				return provider.NewMockToolProvider([]provider.MockTurn{
					{
						Text: "Creating hello.txt",
						ToolCalls: []provider.ToolCall{
							{
								ID:    "c1",
								Name:  "write_file",
								Input: json.RawMessage(`{"path":"hello.txt","content":"hello\n"}`),
							},
						},
						StopReason: "tool_use",
					},
					{
						Text:       "Done.",
						StopReason: "end_turn",
					},
				}), "mock-model", nil
			case "run-edit-patch-permission":
				// Scripted scenario for Phase 7 e2e: edit → apply_patch → bash rm (denied) → end_turn.
				return provider.NewMockToolProvider([]provider.MockTurn{
					// 1) Edit existing file: add FOO function via SEARCH/REPLACE
					{
						Text: "Adding FOO via edit",
						ToolCalls: []provider.ToolCall{{
							ID: "e1", Name: "edit",
							Input: json.RawMessage(`{"blocks":"main.go\n<<<<<<< SEARCH\nfunc Bar() string { return \"bar\" }\n=======\nfunc FOO() string { return \"foo\" }\nfunc Bar() string { return \"bar\" }\n>>>>>>> REPLACE\n"}`),
						}},
						StopReason: "tool_use",
					},
					// 2) apply_patch: add a new file
					{
						Text: "Adding extra file via apply_patch",
						ToolCalls: []provider.ToolCall{{
							ID: "p1", Name: "apply_patch",
							Input: json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: added.txt\n+hello added\n*** End Patch\n"}`),
						}},
						StopReason: "tool_use",
					},
					// 3) Attempt destructive bash that should be denied
					{
						Text: "Trying rm",
						ToolCalls: []provider.ToolCall{{
							ID: "b1", Name: "bash",
							Input: json.RawMessage(`{"command":"rm -rf /"}`),
						}},
						StopReason: "tool_use",
					},
					// 4) Acknowledge the deny and end turn
					{
						Text:       "Permission denied, recognized; finalizing.",
						StopReason: "end_turn",
					},
				}), "mock-model", nil
			case "run-exec-recipe":
				return provider.NewMockToolProvider([]provider.MockTurn{
					{
						Text: "Running a 3-step recipe via exec.",
						ToolCalls: []provider.ToolCall{{
							ID: "x1", Name: "exec",
							Input: json.RawMessage(`{
    "recipe": {
        "steps": [
            {"tool": "write_file", "args": {"path": "step1.txt", "content": "step1\n"}},
            {"tool": "read_file",  "args": {"path": "step1.txt"}},
            {"tool": "bash",       "args": {"command": "echo done"}}
        ],
        "summary": "wrote: {{step_1_status}}; read content: {{step_2_output}}; bash: {{step_3_status}}"
    }
}`),
						}},
						StopReason: "tool_use",
					},
					{
						Text:       "Recipe complete.",
						StopReason: "end_turn",
					},
				}), "mock-model", nil
			case "run-soak":
				// 30-turn scripted soak that exercises:
				//   - many write_file calls (workspace mutation)
				//   - periodic memory_update (bank persistence)
				//   - one compact_now (compactor exercise)
				//   - one repeated tool sequence (stuck detector + AltToolOrder)
				//   - final end_turn → verifier passes (workspace contains soak.txt)
				return provider.NewMockToolProvider(buildSoakScenario()), "mock-model", nil
			case "run-memory-repomap":
				// Scripted scenario for Phase 6 e2e: repomap → memory_update → write_file → end → milestone update.
				return provider.NewMockToolProvider([]provider.MockTurn{
					{
						Text: "First, let me get a project map.",
						ToolCalls: []provider.ToolCall{{
							ID: "rm1", Name: "repomap", Input: json.RawMessage(`{}`),
						}},
						StopReason: "tool_use",
					},
					{
						Text: "Recording the plan.",
						ToolCalls: []provider.ToolCall{{
							ID: "mu1", Name: "memory_update",
							Input: json.RawMessage(`{"file":"activeContext","content":"creating hello.txt","replace":true}`),
						}},
						StopReason: "tool_use",
					},
					{
						Text: "Creating hello.txt.",
						ToolCalls: []provider.ToolCall{{
							ID: "wf1", Name: "write_file",
							Input: json.RawMessage(`{"path":"hello.txt","content":"hello\n"}`),
						}},
						StopReason: "tool_use",
					},
					{
						Text:       "I'm done.",
						StopReason: "end_turn",
					},
					{
						// Milestone turn (post-verify): record completion in progress.md
						Text: "Recording completion.",
						ToolCalls: []provider.ToolCall{{
							ID: "mu2", Name: "memory_update",
							Input: json.RawMessage(`{"file":"progress","section":"Done","content":"- created hello.txt"}`),
						}},
						StopReason: "tool_use",
					},
				}), "mock-model", nil
			default:
				// Text-only Mock for InterviewService scenarios
				return provider.NewMock([]string{
					`{"domain":"unknown","domain_confidence":0.5,"tech_hints":[],"scale_hint":"unknown","ambiguity":"none"}`,
					"What's your project goal?",
				}), "mock-model", nil
			}
		case "anthropic", "":
			key := os.Getenv("ANTHROPIC_API_KEY")
			if key == "" {
				return nil, "", fmt.Errorf("ANTHROPIC_API_KEY not set")
			}
			return provider.NewAnthropic(key), "claude-opus-4-7", nil
		default:
			return nil, "", fmt.Errorf("unknown provider %q", name)
		}
	}

	g := grpc.NewServer()
	repo := session.NewRepo(db)
	runSvc := service.NewRunService(repo, sessionsBase, factory)
	gilv1.RegisterSessionServiceServer(g, service.NewSessionService(repo, runSvc))
	gilv1.RegisterInterviewServiceServer(g, service.NewInterviewService(repo, sessionsBase, factory))
	gilv1.RegisterRunServiceServer(g, runSvc)

	return &server{grpc: g, lis: lis, db: db}, nil
}

// Serve starts the gRPC server on the underlying Unix Domain Socket listener.
// It blocks until the listener is closed or an error occurs.
func (s *server) Serve() error {
	return s.grpc.Serve(s.lis)
}

// Stop gracefully stops the gRPC server, closes the listener, and closes the database.
// It is safe to call multiple times.
func (s *server) Stop() {
	s.grpc.GracefulStop()
	_ = s.lis.Close()
	_ = s.db.Close()
}

func main() {
	home, _ := os.UserHomeDir()
	defaultBase := filepath.Join(home, ".gil")
	base := flag.String("base", defaultBase, "data directory")
	foreground := flag.Bool("foreground", false, "run in foreground")
	httpAddr := flag.String("http", "", "if non-empty, start HTTP/JSON gateway on this addr (e.g., :8080)")
	flag.Parse()

	if !*foreground {
		fmt.Fprintln(os.Stderr, "gild: --foreground required for now (detach mode in Phase 2)")
		os.Exit(2)
	}

	if err := os.MkdirAll(*base, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "gild:", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(*base, "sessions.db")
	sockPath := filepath.Join(*base, "gild.sock")
	sessionsBase := filepath.Join(*base, "sessions")

	srv, err := newServer(dbPath, sockPath, sessionsBase)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gild:", err)
		os.Exit(1)
	}

	slog.Info("gild ready", "socket", sockPath, "db", dbPath)

	if *httpAddr != "" {
		go func() {
			if err := runHTTPGateway(*httpAddr, sockPath); err != nil {
				slog.Error("http gateway exited", "err", err)
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
		}
	}()

	select {
	case <-stop:
		slog.Info("gild shutting down")
	case err := <-errCh:
		fmt.Fprintln(os.Stderr, "gild serve:", err)
	}
	srv.Stop()
}

// buildSoakScenario returns a ~30-turn scripted sequence for GIL_MOCK_MODE=run-soak.
// It exercises write_file (workspace mutation), memory_update (bank persistence),
// compact_now (compactor trigger), repeated bash calls (stuck-detector trigger),
// and a final end_turn that allows the verifier to confirm soak.txt exists.
//
// Ordering is chosen so that soak.txt and the "wrote 10 files" memory entry are
// persisted before the stuck-triggering bash loop begins. If stuck detection aborts
// the run early, the test assertions about soak.txt + memory still hold.
func buildSoakScenario() []provider.MockTurn {
	var turns []provider.MockTurn

	// Phase 1: 10 write_file calls building up workspace
	for i := 0; i < 10; i++ {
		turns = append(turns, provider.MockTurn{
			Text: fmt.Sprintf("Writing file %d", i),
			ToolCalls: []provider.ToolCall{{
				ID:    fmt.Sprintf("w%d", i),
				Name:  "write_file",
				Input: json.RawMessage(fmt.Sprintf(`{"path":"f%d.txt","content":"soak %d\n"}`, i, i)),
			}},
			StopReason: "tool_use",
		})
	}

	// Phase 2: memory_update to record progress
	turns = append(turns, provider.MockTurn{
		Text: "Recording progress to memory.",
		ToolCalls: []provider.ToolCall{{
			ID:    "mu1",
			Name:  "memory_update",
			Input: json.RawMessage(`{"file":"progress","section":"Done","content":"- wrote 10 files"}`),
		}},
		StopReason: "tool_use",
	})

	// Phase 3: write soak.txt now so it exists even if the run ends early (e.g. stuck).
	turns = append(turns, provider.MockTurn{
		Text: "Writing soak.txt to satisfy the verifier.",
		ToolCalls: []provider.ToolCall{{
			ID:    "wfin",
			Name:  "write_file",
			Input: json.RawMessage(`{"path":"soak.txt","content":"soak complete\n"}`),
		}},
		StopReason: "tool_use",
	})

	// Phase 4: compact_now to exercise compactor
	turns = append(turns, provider.MockTurn{
		Text: "Compacting context.",
		ToolCalls: []provider.ToolCall{{
			ID:    "c1",
			Name:  "compact_now",
			Input: json.RawMessage(`{"reason":"midway"}`),
		}},
		StopReason: "tool_use",
	})

	// Phase 5: 6 repeated identical tool calls to trigger stuck detector.
	// PatternRepeatedActionObservation triggers at 4 identical pairs.
	// The run may abort as stuck here; that is acceptable (test allows stuck status).
	for i := 0; i < 6; i++ {
		turns = append(turns, provider.MockTurn{
			Text: "Looping bash for stuck detection.",
			ToolCalls: []provider.ToolCall{{
				ID:    fmt.Sprintf("b%d", i),
				Name:  "bash",
				Input: json.RawMessage(`{"command":"echo loop"}`),
			}},
			StopReason: "tool_use",
		})
	}

	// Phase 6: memory_update final (may not be reached if stuck fires first)
	turns = append(turns, provider.MockTurn{
		Text: "Final memory update.",
		ToolCalls: []provider.ToolCall{{
			ID:    "mufin",
			Name:  "memory_update",
			Input: json.RawMessage(`{"file":"progress","section":"Done","content":"- soak run complete"}`),
		}},
		StopReason: "tool_use",
	})

	// Phase 7: end_turn → verifier checks soak.txt, then memory milestone fires
	turns = append(turns, provider.MockTurn{
		Text:       "Done.",
		StopReason: "end_turn",
	})

	// Phase 8: milestone gate response (called after verification passes)
	turns = append(turns, provider.MockTurn{
		Text: "Recording milestone completion.",
		ToolCalls: []provider.ToolCall{{
			ID:    "mums",
			Name:  "memory_update",
			Input: json.RawMessage(`{"file":"progress","section":"Done","content":"- milestone gate: soak complete"}`),
		}},
		StopReason: "end_turn",
	})

	return turns
}

// runHTTPGateway starts an HTTP/JSON reverse-proxy that translates REST calls
// to gRPC on the Unix domain socket at sockPath.
func runHTTPGateway(addr, sockPath string) error {
	mux := runtime.NewServeMux()
	ctx := context.Background()
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		}),
	}
	target := "unix:" + sockPath
	if err := gilv1.RegisterSessionServiceHandlerFromEndpoint(ctx, mux, target, opts); err != nil {
		return err
	}
	if err := gilv1.RegisterRunServiceHandlerFromEndpoint(ctx, mux, target, opts); err != nil {
		return err
	}
	if err := gilv1.RegisterInterviewServiceHandlerFromEndpoint(ctx, mux, target, opts); err != nil {
		return err
	}
	slog.Info("http gateway listening", "addr", addr)
	return http.ListenAndServe(addr, mux)
}
