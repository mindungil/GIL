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
	"strconv"
	"syscall"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	_ "modernc.org/sqlite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mindungil/gil/core/credstore"
	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/version"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/server/internal/auth"
	"github.com/mindungil/gil/server/internal/metrics"
	"github.com/mindungil/gil/server/internal/service"
	"github.com/mindungil/gil/server/internal/uds"
)

// server wraps a gRPC server, Unix Domain Socket listener, optional TCP listener,
// and SQLite database, providing unified startup and graceful shutdown for the
// gild daemon.
type server struct {
	grpc   *grpc.Server
	lis    net.Listener
	tcpLis net.Listener // nil when --grpc-tcp not set
	db     *sql.DB
}

// newServer creates and initializes a gRPC server listening on sockPath with a SQLite
// database at dbPath. It returns an error if the database cannot be opened, migrations
// fail, the socket cannot be created, or gRPC registration fails. Partial failures
// are cleaned up: if migration or socket creation fails, the database is closed.
//
// authMW, when non-nil, is registered as both a unary and a stream interceptor —
// the same middleware enforces auth on every RPC regardless of transport, with
// per-connection bypass for UDS handled inside the middleware itself.
//
// authFile is the path to the credstore auth.json that the provider factory
// consults before falling back to environment variables. Pass an empty string
// to disable the credstore lookup (useful in tests that only want the env
// path). When non-empty, the file does not need to exist — a missing file
// behaves as "no credentials configured" so existing env-only setups stay
// transparently compatible.
func newServer(dbPath, sockPath, sessionsBase, authFile string, authMW *auth.Middleware) (*server, error) {
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
			case "run-webfetch":
				// Phase 18 Track B e2e: agent calls web_fetch on a URL
				// passed via GIL_MOCK_WEBFETCH_URL (set by
				// phase18_webfetch_test.sh to point at a local Python
				// http.server), inspects the result, then ends the
				// turn. Verifier just passes — assertions live in the
				// bash script which greps the event log for tool_result
				// content matching the fixture's title and converted
				// markdown.
				url := os.Getenv("GIL_MOCK_WEBFETCH_URL")
				if url == "" {
					url = "http://127.0.0.1:0/missing"
				}
				input, _ := json.Marshal(map[string]any{"url": url})
				return provider.NewMockToolProvider([]provider.MockTurn{
					{
						Text: "Fetching the docs page.",
						ToolCalls: []provider.ToolCall{{
							ID: "wf1", Name: "web_fetch", Input: input,
						}},
						StopReason: "tool_use",
					},
					{
						Text:       "Docs read; nothing else needed.",
						StopReason: "end_turn",
					},
				}), "mock-model", nil
			case "run-lsp":
				// Phase 18 Track C e2e: agent calls the lsp tool with the
				// four operations the smoke test exercises (definition,
				// references, hover, document_symbols). The file +
				// line/column come from env vars set by the e2e script
				// (which knows where the fixture file's symbols live).
				// Verifier just passes — assertions live in the bash
				// script which greps the event log for tool_result
				// content matching the fixture's definition/reference
				// targets.
				file := os.Getenv("GIL_MOCK_LSP_FILE")
				if file == "" {
					file = "use.go"
				}
				line := 3
				col := 28
				if v, err := strconv.Atoi(os.Getenv("GIL_MOCK_LSP_LINE")); err == nil && v > 0 {
					line = v
				}
				if v, err := strconv.Atoi(os.Getenv("GIL_MOCK_LSP_COL")); err == nil && v > 0 {
					col = v
				}
				defInput, _ := json.Marshal(map[string]any{"operation": "definition", "file": file, "line": line, "column": col})
				refInput, _ := json.Marshal(map[string]any{"operation": "references", "file": file, "line": line, "column": col})
				hoverInput, _ := json.Marshal(map[string]any{"operation": "hover", "file": file, "line": line, "column": col})
				docInput, _ := json.Marshal(map[string]any{"operation": "document_symbols", "file": file})
				return provider.NewMockToolProvider([]provider.MockTurn{
					{
						Text:       "Looking up the definition.",
						ToolCalls:  []provider.ToolCall{{ID: "lsp1", Name: "lsp", Input: defInput}},
						StopReason: "tool_use",
					},
					{
						Text:       "Finding references.",
						ToolCalls:  []provider.ToolCall{{ID: "lsp2", Name: "lsp", Input: refInput}},
						StopReason: "tool_use",
					},
					{
						Text:       "Reading hover docs.",
						ToolCalls:  []provider.ToolCall{{ID: "lsp3", Name: "lsp", Input: hoverInput}},
						StopReason: "tool_use",
					},
					{
						Text:       "Listing symbols.",
						ToolCalls:  []provider.ToolCall{{ID: "lsp4", Name: "lsp", Input: docInput}},
						StopReason: "tool_use",
					},
					{
						Text:       "Done.",
						StopReason: "end_turn",
					},
				}), "mock-model", nil
			case "run-subagent":
				// Phase 18 Track E e2e: agent calls the subagent tool
				// with a research goal; the sub-loop returns a 1-paragraph
				// finding; the parent picks it up via tool_result and
				// ends its turn. The mock provider serves both parent +
				// sub-loop turns from the same scripted queue, in order:
				//   1. parent: call subagent({goal: ...})
				//   2. sub-loop: return finding text + end_turn
				//   3. parent: end_turn (uses the finding)
				goal := os.Getenv("GIL_MOCK_SUBAGENT_GOAL")
				if goal == "" {
					goal = "find which file defines the main agent loop"
				}
				input, _ := json.Marshal(map[string]any{
					"goal":           goal,
					"max_iterations": 3,
				})
				return provider.NewMockToolProvider([]provider.MockTurn{
					// Parent turn 1: invoke subagent tool.
					{
						Text: "Delegating research to a subagent.",
						ToolCalls: []provider.ToolCall{{
							ID: "sa1", Name: "subagent", Input: input,
						}},
						StopReason: "tool_use",
					},
					// Sub-loop turn 1: return the finding and end the
					// sub-loop. The sub-loop's default tool set
					// (read_file/repomap/memory_load/web_fetch/lsp) is
					// filtered against the parent's available tools, so
					// the sub-loop has whatever overlaps; the only
					// sensible turn for this scripted scenario is
					// "report the finding".
					{
						Text:       "core/runner/runner.go has main loop in func (a *AgentLoop) Run().",
						StopReason: "end_turn",
					},
					// Parent turn 2: incorporate the finding and end.
					{
						Text:       "Got the finding from subagent. All done.",
						StopReason: "end_turn",
					},
					// Parent turn 3: memory milestone gate (post-verify).
					// The runner gives the agent one shot to call
					// memory_update; we just respond with "no update" so
					// the gate completes cleanly.
					{
						Text:       "no update",
						StopReason: "end_turn",
					},
				}), "mock-model", nil
			case "run-plan":
				// Phase 18 Track A e2e: scripted plan-tool flow. The
				// agent (1) sets a 3-item plan, (2) does some work and
				// marks items completed via update_item, (3) ends.
				return provider.NewMockToolProvider([]provider.MockTurn{
					// Turn 1: write the plan.
					{
						Text: "Writing initial plan.",
						ToolCalls: []provider.ToolCall{{
							ID: "p1", Name: "plan",
							Input: json.RawMessage(`{
                                "operation":"set",
                                "items":[
                                    {"text":"create plan-step-1.txt","status":"pending"},
                                    {"text":"create plan-step-2.txt","status":"pending"},
                                    {"text":"create plan-step-3.txt","status":"pending"}
                                ]
                            }`),
						}},
						StopReason: "tool_use",
					},
					// Turn 2: start step 1 (mark in_progress).
					{
						Text: "Starting step 1.",
						ToolCalls: []provider.ToolCall{{
							ID: "p2", Name: "plan",
							Input: json.RawMessage(`{"operation":"update_item","id":"i1","status":"in_progress"}`),
						}},
						StopReason: "tool_use",
					},
					// Turn 3: do step 1 (write the file).
					{
						Text: "Creating plan-step-1.txt",
						ToolCalls: []provider.ToolCall{{
							ID: "w1", Name: "write_file",
							Input: json.RawMessage(`{"path":"plan-step-1.txt","content":"step 1 done\n"}`),
						}},
						StopReason: "tool_use",
					},
					// Turn 4: mark step 1 done; start step 2.
					{
						Text: "Step 1 done; starting step 2.",
						ToolCalls: []provider.ToolCall{
							{ID: "p3", Name: "plan", Input: json.RawMessage(`{"operation":"update_item","id":"i1","status":"completed"}`)},
							{ID: "p4", Name: "plan", Input: json.RawMessage(`{"operation":"update_item","id":"i2","status":"in_progress"}`)},
						},
						StopReason: "tool_use",
					},
					// Turn 5: do step 2.
					{
						Text: "Creating plan-step-2.txt",
						ToolCalls: []provider.ToolCall{{
							ID: "w2", Name: "write_file",
							Input: json.RawMessage(`{"path":"plan-step-2.txt","content":"step 2 done\n"}`),
						}},
						StopReason: "tool_use",
					},
					// Turn 6: mark step 2 done; do step 3 directly.
					{
						Text: "Step 2 done; finishing step 3.",
						ToolCalls: []provider.ToolCall{
							{ID: "p5", Name: "plan", Input: json.RawMessage(`{"operation":"update_item","id":"i2","status":"completed"}`)},
							{ID: "w3", Name: "write_file", Input: json.RawMessage(`{"path":"plan-step-3.txt","content":"step 3 done\n"}`)},
						},
						StopReason: "tool_use",
					},
					// Turn 7: mark step 3 completed and stop.
					{
						Text: "All done.",
						ToolCalls: []provider.ToolCall{{
							ID: "p6", Name: "plan",
							Input: json.RawMessage(`{"operation":"update_item","id":"i3","status":"completed"}`),
						}},
						StopReason: "tool_use",
					},
					// Turn 8: end.
					{Text: "Plan complete.", StopReason: "end_turn"},
				}), "mock-model", nil
			case "run-clarify":
				// Phase 18 Track D e2e: scripted clarify-tool flow. The
				// agent (1) calls clarify with two suggestions and
				// urgency=high, (2) reads the user's answer from the
				// tool_result, (3) ends the turn. The e2e test answers
				// the ask via `gil clarify <id> "yes, deploy" --ask-id <id>`
				// from a side goroutine while the run is paused.
				return provider.NewMockToolProvider([]provider.MockTurn{
					{
						Text: "I need a quick clarification before continuing.",
						ToolCalls: []provider.ToolCall{{
							ID:   "cl1",
							Name: "clarify",
							Input: json.RawMessage(`{
                                "question":"Should I deploy now?",
                                "context":"verifier passed; user did not pre-approve auto-deploy",
                                "suggestions":["yes, deploy","no, hold"],
                                "urgency":"high"
                            }`),
						}},
						StopReason: "tool_use",
					},
					{
						Text:       "Acknowledged the user's answer; finishing.",
						StopReason: "end_turn",
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
			// credstore takes precedence; env var is the legacy
			// fallback so existing CI flows (and the e2e suite) keep
			// working without changes.
			key, _ := lookupCredKey(authFile, credstore.Anthropic)
			if key == "" {
				key = os.Getenv("ANTHROPIC_API_KEY")
			}
			if key == "" {
				// User-facing wording. The CLI converts this to a UserError
				// with hint "gil auth login anthropic" at the gRPC boundary.
				return nil, "", fmt.Errorf("no credentials for anthropic")
			}
			return provider.NewAnthropic(key), "claude-opus-4-7", nil
		case "openai":
			cred, _ := lookupCred(authFile, credstore.OpenAI)
			key := ""
			base := "https://api.openai.com/v1"
			if cred != nil {
				key = cred.APIKey
				if cred.BaseURL != "" {
					base = cred.BaseURL
				}
			}
			if key == "" {
				key = os.Getenv("OPENAI_API_KEY")
			}
			if key == "" {
				return nil, "", fmt.Errorf("no credentials for openai")
			}
			return provider.NewOpenAI(key, base), "gpt-4o", nil
		case "openrouter":
			cred, _ := lookupCred(authFile, credstore.OpenRouter)
			key := ""
			base := "https://openrouter.ai/api/v1"
			if cred != nil {
				key = cred.APIKey
				if cred.BaseURL != "" {
					base = cred.BaseURL
				}
			}
			if key == "" {
				key = os.Getenv("OPENROUTER_API_KEY")
			}
			if key == "" {
				return nil, "", fmt.Errorf("no credentials for openrouter")
			}
			return provider.NewOpenAI(key, base), "anthropic/claude-sonnet-4", nil
		case "vllm", "local":
			cred, _ := lookupCred(authFile, credstore.VLLM)
			key, base := "", ""
			if cred != nil {
				key = cred.APIKey
				base = cred.BaseURL
			}
			if base == "" {
				base = os.Getenv("VLLM_BASE_URL")
			}
			if key == "" {
				key = os.Getenv("VLLM_API_KEY")
			}
			if base == "" {
				// vLLM has no canonical endpoint; refuse rather than guess.
				return nil, "", fmt.Errorf("no credentials for vllm: base URL required")
			}
			// vLLM often runs unauthenticated, so an empty key is allowed —
			// the OpenAI adapter omits the Authorization header in that case.
			// The model name has no default; the caller must specify via
			// spec.Models.Main since each vLLM deploy serves a different model.
			return provider.NewOpenAI(key, base), "", nil
		default:
			return nil, "", fmt.Errorf("unknown provider %q (available: anthropic, openai, openrouter, vllm, mock)", name)
		}
	}

	var grpcOpts []grpc.ServerOption
	if authMW != nil {
		grpcOpts = append(grpcOpts,
			grpc.UnaryInterceptor(authMW.UnaryInterceptor()),
			grpc.StreamInterceptor(authMW.StreamInterceptor()),
		)
	}
	g := grpc.NewServer(grpcOpts...)
	repo := session.NewRepo(db)
	runSvc := service.NewRunService(repo, sessionsBase, factory)
	gilv1.RegisterSessionServiceServer(g, service.NewSessionService(repo, runSvc).WithSessionsBase(sessionsBase).WithBudgetGetter(runSvc))
	gilv1.RegisterInterviewServiceServer(g, service.NewInterviewService(repo, sessionsBase, factory))
	gilv1.RegisterRunServiceServer(g, runSvc)

	return &server{grpc: g, lis: lis, db: db}, nil
}

// lookupCredKey returns the API key stored in authFile for name, or an
// empty string when there is no credstore file, no entry for name, or the
// entry is not an API-key credential. It deliberately swallows read errors
// and returns ("", err) — the factory always falls back to the env var, so
// a corrupt or unreadable auth.json should not crash the daemon. Callers
// that want stricter handling can inspect the returned error.
//
// authFile == "" disables the lookup entirely; this lets tests construct a
// server with no credstore and rely solely on env vars.
func lookupCredKey(authFile string, name credstore.ProviderName) (string, error) {
	if authFile == "" {
		return "", nil
	}
	store := credstore.NewFileStore(authFile)
	cred, err := store.Get(context.Background(), name)
	if err != nil {
		return "", err
	}
	if cred == nil {
		return "", nil
	}
	return cred.APIKey, nil
}

// lookupCred returns the full credential record for name, or (nil, nil)
// when no credstore file or entry exists. Unlike lookupCredKey, this
// preserves BaseURL so providers like vllm and openrouter can use a
// stored endpoint without extra plumbing. Errors are swallowed and
// returned alongside nil so the factory can fall back to env vars.
func lookupCred(authFile string, name credstore.ProviderName) (*credstore.Credential, error) {
	if authFile == "" {
		return nil, nil
	}
	store := credstore.NewFileStore(authFile)
	return store.Get(context.Background(), name)
}

// AttachTCP binds an additional TCP listener and serves the same gRPC server on it.
// Useful when --grpc-tcp is set so gild accepts both UDS (local) and TCP (remote)
// connections through the same auth middleware.
func (s *server) AttachTCP(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("attach tcp %q: %w", addr, err)
	}
	s.tcpLis = lis
	go func() {
		if err := s.grpc.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			slog.Error("gild tcp listener exited", "err", err)
		}
	}()
	return nil
}

// TCPAddr returns the actual bound TCP address (useful when caller passed :0).
// Returns "" when no TCP listener is attached.
func (s *server) TCPAddr() string {
	if s.tcpLis == nil {
		return ""
	}
	return s.tcpLis.Addr().String()
}

// Serve starts the gRPC server on the underlying Unix Domain Socket listener.
// It blocks until the listener is closed or an error occurs.
func (s *server) Serve() error {
	return s.grpc.Serve(s.lis)
}

// Stop gracefully stops the gRPC server, closes the listener(s), and closes the
// database. It is safe to call multiple times.
func (s *server) Stop() {
	s.grpc.GracefulStop()
	_ = s.lis.Close()
	if s.tcpLis != nil {
		_ = s.tcpLis.Close()
	}
	_ = s.db.Close()
}

func main() {
	// Path resolution priority (highest first):
	//   --home / --base   single-tree override (treated like GIL_HOME).
	//                     --base is the legacy spelling kept as an alias so
	//                     the existing e2e suite and any third-party scripts
	//                     keep working without edits.
	//   GIL_HOME env      single-tree, picked up by paths.FromEnv.
	//   default           XDG-derived layout (~/.config/gil, …).
	home := flag.String("home", "", "single-tree override for all gil dirs (alias of GIL_HOME); empty means use XDG defaults")
	base := flag.String("base", "", "deprecated alias for --home; kept for backwards compatibility")
	foreground := flag.Bool("foreground", false, "run in foreground")
	httpAddr := flag.String("http", "", "if non-empty, start HTTP/JSON gateway on this addr (e.g., :8080)")
	user := flag.String("user", "", "user namespace; appends users/<name>/ to every layout dir")
	metricsAddr := flag.String("metrics", "", "if non-empty, expose Prometheus metrics on this addr (e.g., :9090)")
	grpcTCP := flag.String("grpc-tcp", "", "if non-empty, also serve gRPC on this TCP addr (e.g., :7070); auth recommended for non-loopback binds")
	authIssuer := flag.String("auth-issuer", "", "OIDC issuer URL (e.g., https://accounts.google.com); when set, enables bearer-token auth")
	authAudience := flag.String("auth-audience", "", "expected OIDC token audience (`aud` claim); validated when --auth-issuer is set")
	authAllowUDS := flag.Bool("auth-allow-uds", true, "skip auth on UDS connections (assumed local-trusted via socket file mode 0600)")
	authEnforceSub := flag.String("auth-enforce-sub", "", "if non-empty, require token `sub` claim to equal this value (pairs with --user)")
	// --version is handled before any other side-effect (path resolution,
	// migration, listener bind) so a fresh-install user can run
	// `gild --version` even when nothing else is configured. We mirror the
	// cobra-style "gild vX.Y.Z" output the cli root produces, so the four
	// gil binaries are interchangeable for version-sniffing scripts.
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Fprintf(os.Stdout, "gild %s\n", version.String())
		return
	}

	if !*foreground {
		fmt.Fprintln(os.Stderr, "gild: --foreground required for now (detach mode in Phase 2)")
		os.Exit(2)
	}

	// --home wins over --base; either, when set, takes precedence over
	// GIL_HOME and the XDG defaults. Using os.Setenv is the simplest way
	// to funnel all three precedence rungs through paths.FromEnv.
	switch {
	case *home != "":
		os.Setenv("GIL_HOME", *home)
	case *base != "":
		// Soft deprecation notice; do not fail because all our own e2e
		// scripts still pass --base.
		fmt.Fprintln(os.Stderr, "gild: --base is deprecated; use --home (or set GIL_HOME)")
		os.Setenv("GIL_HOME", *base)
	}

	layout, err := paths.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gild:", err)
		os.Exit(1)
	}
	layout = layout.WithUser(*user)

	if err := layout.EnsureDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "gild:", err)
		os.Exit(1)
	}

	// Best-effort migration of any pre-XDG ~/.gil tree on this user. The
	// function is idempotent so calling it on every start is fine; we only
	// surface failures (success and "nothing to do" are both silent so
	// existing log output stays unchanged).
	if migrated, err := paths.MigrateLegacyTilde(layout); err != nil {
		slog.Warn("legacy ~/.gil migration failed", "err", err)
	} else if migrated {
		slog.Info("migrated legacy ~/.gil tree into XDG layout")
	}

	metrics.SetVersion(version.Short())

	dbPath := layout.SessionsDB()
	sockPath := layout.Sock()
	sessionsBase := layout.SessionsDir()
	authFile := layout.AuthFile()

	// Optional OIDC auth: only construct the middleware when an issuer is set,
	// so the default "no flags" gild behaviour is byte-for-byte unchanged.
	var authMW *auth.Middleware
	if *authIssuer != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		v, err := auth.NewVerifier(ctx, *authIssuer, *authAudience, 5*time.Minute, nil)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "gild auth:", err)
			os.Exit(1)
		}
		authMW = &auth.Middleware{
			Verifier:       v,
			AllowUDS:       *authAllowUDS,
			EnforceUserSub: *authEnforceSub,
		}
		slog.Info("gild auth enabled", "issuer", *authIssuer, "audience", *authAudience, "allow_uds", *authAllowUDS)
	}

	srv, err := newServer(dbPath, sockPath, sessionsBase, authFile, authMW)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gild:", err)
		os.Exit(1)
	}

	if *grpcTCP != "" {
		if err := srv.AttachTCP(*grpcTCP); err != nil {
			fmt.Fprintln(os.Stderr, "gild:", err)
			os.Exit(1)
		}
		slog.Info("gild tcp listening", "addr", srv.TCPAddr())
	}

	slog.Info("gild ready", "socket", sockPath, "db", dbPath)

	if *httpAddr != "" {
		go func() {
			if err := runHTTPGateway(*httpAddr, sockPath); err != nil {
				slog.Error("http gateway exited", "err", err)
			}
		}()
	}

	if *metricsAddr != "" {
		go func() {
			mux := http.NewServeMux()
			mux.Handle("/metrics", promhttp.Handler())
			slog.Info("metrics endpoint listening", "addr", *metricsAddr)
			if err := http.ListenAndServe(*metricsAddr, mux); err != nil {
				slog.Error("metrics endpoint exited", "err", err)
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
