package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mindungil/gil/core/checkpoint"
	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/exec"
	"github.com/mindungil/gil/core/lsp"
	"github.com/mindungil/gil/core/mcpregistry"
	"github.com/mindungil/gil/core/memory"
	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/permission"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/runner"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	"github.com/mindungil/gil/core/stuck"
	"github.com/mindungil/gil/core/tool"
	"github.com/mindungil/gil/core/verify"
	"github.com/mindungil/gil/core/workspace"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/runtime/cloud"
	"github.com/mindungil/gil/runtime/daytona"
	"github.com/mindungil/gil/runtime/docker"
	"github.com/mindungil/gil/runtime/local"
	"github.com/mindungil/gil/runtime/modal"
	"github.com/mindungil/gil/runtime/ssh"
	"github.com/mindungil/gil/server/internal/metrics"
)

// runProgressSnap holds live iteration/token counters for an active run.
//
// cost / budgetExceeded / budgetReason are populated by the run's event
// subscriber when budget_warning / budget_exceeded fire so other RPCs
// (Session.toProto) can surface the alert state without needing to
// re-derive the per-iteration cost themselves.
type runProgressSnap struct {
	iters          int32
	tokens         int64
	cost           float64
	budgetExceeded bool
	budgetReason   string
}

// pendingAsk records everything AnswerPermission needs to dispatch a
// user's answer back to the blocked AskCallback AND to persist the
// rule when the user picks a SESSION or ALWAYS tier.
//
// We keep tool/key here (not just a chan) so the user's choice can be
// translated to a wildcard pattern without re-parsing the originating
// permission_ask event. The evaluator pointer lets AppendSession attach
// the session-scoped rule to the correct evaluator instance — relevant
// when multiple runs of the same session reuse one RunService process.
type pendingAsk struct {
	ch        chan bool
	tool      string
	key       string
	evaluator *permission.EvaluatorWithStore // nil ⇒ no session-scoped layer (FULL autonomy)
}

// RunService handles RunService gRPC. Loads frozen spec, builds tools/verifier,
// runs AgentLoop synchronously or in background (detach mode). Tail subscribes
// to the live event stream.
type RunService struct {
	gilv1.UnimplementedRunServiceServer

	repo            *session.Repo
	sessionsBase    string
	providerFactory ProviderFactory

	mu          sync.Mutex
	runStreams  map[string]*event.Stream            // per-session live event streams
	runProgress map[string]*runProgressSnap         // per-session live progress counters
	pendingAsks map[string]map[string]*pendingAsk   // sessionID → requestID → ask context
	// runLoops holds a pointer to the AgentLoop for each in-flight run so
	// surface-side RPCs (RequestCompact, PostHint) can stage actions for
	// the next iteration without preempting the current tool call. The
	// entry is removed in executeRun's defer once Run() returns. Nil when
	// no run is in flight for that session.
	runLoops    map[string]*runner.AgentLoop
}

// NewRunService constructs the service.
func NewRunService(repo *session.Repo, sessionsBase string, factory ProviderFactory) *RunService {
	return &RunService{
		repo:            repo,
		sessionsBase:    sessionsBase,
		providerFactory: factory,
		runStreams:      make(map[string]*event.Stream),
		runProgress:     make(map[string]*runProgressSnap),
		pendingAsks:     make(map[string]map[string]*pendingAsk),
		runLoops:        make(map[string]*runner.AgentLoop),
	}
}

// Progress returns a live snapshot of iteration and token counts for the given
// session. Returns ok=false when no run is active for that session.
func (s *RunService) Progress(sessionID string) (iters int32, tokens int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.runProgress[sessionID]
	if !ok {
		return 0, 0, false
	}
	return p.iters, p.tokens, true
}

// Budget implements service.BudgetGetter. Returns the live cost +
// sticky budget_exceeded flag for the in-flight run. ok=false when
// there is no active run for sessionID — SessionService falls back
// to the persisted rollup in that case.
func (s *RunService) Budget(sessionID string) (cost float64, exceeded bool, reason string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.runProgress[sessionID]
	if !ok {
		return 0, false, "", false
	}
	return p.cost, p.budgetExceeded, p.budgetReason, true
}

func (s *RunService) sessionDir(sessionID string) string {
	return filepath.Join(s.sessionsBase, sessionID)
}

// buildTools returns the tool set for a run, configured per spec.Workspace.Backend.
// Returns (tools, error). Unsupported backends produce errors so RunService.Start
// can refuse the run rather than silently degrading.
func buildTools(workspaceDir string, ws *gilv1.Workspace) ([]tool.Tool, error) {
	backend := gilv1.WorkspaceBackend_LOCAL_NATIVE
	if ws != nil && ws.Backend != gilv1.WorkspaceBackend_BACKEND_UNSPECIFIED {
		backend = ws.Backend
	}
	switch backend {
	case gilv1.WorkspaceBackend_LOCAL_NATIVE:
		return []tool.Tool{
			&tool.Bash{WorkingDir: workspaceDir},
			&tool.WriteFile{WorkingDir: workspaceDir},
			&tool.ReadFile{WorkingDir: workspaceDir},
			&tool.Repomap{Root: workspaceDir},
		}, nil
	case gilv1.WorkspaceBackend_LOCAL_SANDBOX:
		if !local.Available() {
			return nil, fmt.Errorf("workspace backend LOCAL_SANDBOX requires bwrap, but it is not installed")
		}
		sb := &local.Sandbox{
			WorkspaceDir: workspaceDir,
			Mode:         local.ModeWorkspaceWrite,
		}
		return []tool.Tool{
			&tool.Bash{WorkingDir: workspaceDir, Wrapper: sb},
			&tool.WriteFile{WorkingDir: workspaceDir},
			&tool.ReadFile{WorkingDir: workspaceDir},
			&tool.Repomap{Root: workspaceDir},
		}, nil
	case gilv1.WorkspaceBackend_DOCKER:
		if !docker.Available() {
			return nil, fmt.Errorf("workspace backend DOCKER requires docker, but it is not in PATH")
		}
		// Tools are returned bare; executeRun wraps the Bash tool with
		// docker.Wrapper after starting the session container.
		return []tool.Tool{
			&tool.Bash{WorkingDir: workspaceDir},
			&tool.WriteFile{WorkingDir: workspaceDir},
			&tool.ReadFile{WorkingDir: workspaceDir},
			&tool.Repomap{Root: workspaceDir},
		}, nil
	case gilv1.WorkspaceBackend_SSH:
		if !ssh.Available() {
			return nil, fmt.Errorf("workspace backend SSH requires ssh, but it is not in PATH")
		}
		if ws == nil || ws.Path == "" {
			return nil, fmt.Errorf("workspace backend SSH requires spec.workspace.path (e.g., user@host or user@host:port/key)")
		}
		host, port, keyPath := ssh.ParseTarget(ws.Path)
		if host == "" {
			return nil, fmt.Errorf("workspace backend SSH: failed to parse target %q", ws.Path)
		}
		sshWrap := &ssh.Wrapper{Host: host, Port: port, KeyPath: keyPath}
		return []tool.Tool{
			&tool.Bash{WorkingDir: workspaceDir, Wrapper: sshWrap},
			// File ops stay LOCAL — Phase 8 limitation; remote file sync deferred to Phase 9.
			&tool.WriteFile{WorkingDir: workspaceDir},
			&tool.ReadFile{WorkingDir: workspaceDir},
		}, nil

	case gilv1.WorkspaceBackend_VM:
		return nil, fmt.Errorf("workspace backend VM not yet supported (Phase 9+)")

	case gilv1.WorkspaceBackend_MODAL:
		if !modal.New().Available() {
			return nil, fmt.Errorf("workspace backend MODAL requires MODAL_TOKEN_ID + MODAL_TOKEN_SECRET env vars + modal CLI")
		}
		// Tools returned bare; executeRun does Provision and rewires Bash.Wrapper.
		return []tool.Tool{
			&tool.Bash{WorkingDir: workspaceDir},
			&tool.WriteFile{WorkingDir: workspaceDir},
			&tool.ReadFile{WorkingDir: workspaceDir},
		}, nil

	case gilv1.WorkspaceBackend_DAYTONA:
		if !daytona.New().Available() {
			return nil, fmt.Errorf("workspace backend DAYTONA requires DAYTONA_API_KEY env var")
		}
		return []tool.Tool{
			&tool.Bash{WorkingDir: workspaceDir},
			&tool.WriteFile{WorkingDir: workspaceDir},
			&tool.ReadFile{WorkingDir: workspaceDir},
		}, nil

	default:
		return nil, fmt.Errorf("unknown workspace backend: %v", backend)
	}
}

// Start runs the agent loop and returns the result. When req.Detach is true,
// the loop runs in a goroutine and the method returns immediately with
// Status="started".
func (s *RunService) Start(ctx context.Context, req *gilv1.StartRunRequest) (*gilv1.StartRunResponse, error) {
	sess, err := s.repo.Get(ctx, req.SessionId)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
		}
		return nil, status.Errorf(codes.Internal, "session lookup: %v", err)
	}
	if sess.Status != "frozen" {
		return nil, status.Errorf(codes.FailedPrecondition,
			"session %q must be frozen before run (current status: %s)", req.SessionId, sess.Status)
	}

	store := specstore.NewStore(s.sessionDir(req.SessionId))
	spec, err := store.Load()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load spec: %v", err)
	}

	// Apply layered workspace defaults BEFORE we resolve provider /
	// build tools, so that fields the interview left blank (provider,
	// model, autonomy, backend) inherit from `<workspace>/.gil/config.toml`
	// or `$XDG_CONFIG_HOME/gil/config.toml`. Spec values that ARE set
	// always win — the interview is the source of truth, the layered
	// config is only a backstop for what the user did not pin.
	workspaceDir := sess.WorkingDir
	if spec.Workspace != nil && spec.Workspace.Path != "" {
		workspaceDir = spec.Workspace.Path
	}
	wsRoot, _ := workspace.Discover(workspaceDir)
	var globalCfgPath string
	if layout, lerr := paths.FromEnv(); lerr == nil {
		globalCfgPath = layout.ConfigFile()
	}
	wsCfg, cfgErr := workspace.Resolve(globalCfgPath, workspace.LocalConfigFile(wsRoot))
	if cfgErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "workspace config: %v", cfgErr)
	}
	spec = workspace.ApplyDefaults(spec, wsCfg)

	prov, model, err := s.providerFactory(req.Provider)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "provider: %v", err)
	}
	if req.Model != "" {
		model = req.Model
	} else if spec.Models != nil && spec.Models.Main != nil && spec.Models.Main.ModelId != "" {
		// When the run request does NOT pin a model but the (now
		// defaults-applied) spec does, honour the spec — otherwise the
		// project-local config.toml's model would be ignored as soon as
		// the provider factory returned its own default.
		model = spec.Models.Main.ModelId
	}
	prov = provider.NewRetry(prov)

	tools, err := buildTools(workspaceDir, spec.Workspace)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "workspace backend: %v", err)
	}
	ver := verify.NewRunner(workspaceDir)

	// Mark running BEFORE spawning goroutine so the client sees consistent state.
	if err := s.repo.UpdateStatus(ctx, req.SessionId, "running"); err != nil {
		return nil, status.Errorf(codes.Internal, "update status: %v", err)
	}
	metrics.SessionsRunning.Inc()

	if req.Detach {
		go func() {
			// Use a background context: the gRPC ctx cancels when Start returns.
			bgCtx := context.Background()
			_, _ = s.executeRun(bgCtx, req.SessionId, spec, prov, model, tools, ver, workspaceDir)
		}()
		return &gilv1.StartRunResponse{Status: "started"}, nil
	}
	return s.executeRun(ctx, req.SessionId, spec, prov, model, tools, ver, workspaceDir)
}

// makeAskCallback returns an AskCallback for use in AgentLoop. When the agent
// encounters a Decision=Ask, this callback: generates a ULID request_id,
// stores a per-request entry in pendingAsks (with the tool/key/evaluator
// context AnswerPermission needs to persist a rule), emits a permission_ask
// event (so TUI subscribers can display a modal), then blocks for up to 60s
// waiting for an AnswerPermission RPC. Timeout = deny, matching Phase 7
// semantics.
func (s *RunService) makeAskCallback(sessionID string, stream *event.Stream, evaluator *permission.EvaluatorWithStore) func(context.Context, runner.AskRequest) bool {
	return func(ctx context.Context, req runner.AskRequest) bool {
		reqID := ulid.Make().String()
		ch := make(chan bool, 1)

		s.mu.Lock()
		if s.pendingAsks[sessionID] == nil {
			s.pendingAsks[sessionID] = make(map[string]*pendingAsk)
		}
		s.pendingAsks[sessionID][reqID] = &pendingAsk{
			ch:        ch,
			tool:      req.Tool,
			key:       req.Key,
			evaluator: evaluator,
		}
		s.mu.Unlock()

		// Emit permission_ask event so TUI subscribers see it.
		data, _ := json.Marshal(map[string]any{
			"request_id": reqID,
			"tool":       req.Tool,
			"key":        req.Key,
		})
		_, _ = stream.Append(event.Event{
			Timestamp: time.Now().UTC(),
			Source:    event.SourceSystem,
			Kind:      event.KindNote,
			Type:      "permission_ask",
			Data:      data,
		})

		defer func() {
			s.mu.Lock()
			delete(s.pendingAsks[sessionID], reqID)
			s.mu.Unlock()
		}()

		select {
		case allow := <-ch:
			return allow
		case <-ctx.Done():
			return false
		case <-time.After(60 * time.Second):
			return false // timeout = deny (matches Phase 7 default-deny semantics)
		}
	}
}

// AnswerPermission delivers a yes/no answer to a pending permission_ask
// channel and, when the user picked a SESSION or ALWAYS tier, records the
// rule in the appropriate store (in-memory session list or on-disk
// PersistentStore). Returns delivered=false when the request_id is not
// pending (already answered, timed out, or never existed) — that is not an
// error, just a normal race outcome.
//
// Backwards compatibility: when req.Decision is UNSPECIFIED the server
// treats the call as a legacy "once" answer driven by req.Allow. This
// keeps the existing `gil run` CLI / phase07 e2e flow working unchanged.
func (s *RunService) AnswerPermission(ctx context.Context, req *gilv1.AnswerPermissionRequest) (*gilv1.AnswerPermissionResponse, error) {
	s.mu.Lock()
	chs, ok := s.pendingAsks[req.SessionId]
	var ask *pendingAsk
	if ok {
		ask = chs[req.RequestId]
	}
	s.mu.Unlock()

	if ask == nil {
		return &gilv1.AnswerPermissionResponse{Delivered: false}, nil
	}

	// Resolve allow + persistence intent. Decision wins when set; allow
	// is the legacy fallback (always once-tier).
	allow, persist := resolveDecision(req.Decision, req.Allow)

	// Persistence side-effects happen BEFORE we unblock the runner so a
	// "session_allow" answer is in the in-memory list before the next
	// tool call evaluates against it. Failures are logged but never block
	// the user's answer — the runner must always be unblocked or it
	// hangs for 60s.
	if persist.IsSession() && ask.evaluator != nil {
		list := "allow"
		if persist.IsDeny() {
			list = "deny"
		}
		ask.evaluator.AppendSession(list, ask.key)
	}
	if persist.IsAlways() && ask.evaluator != nil && ask.evaluator.Store != nil && ask.evaluator.ProjectPath != "" {
		list := "always_allow"
		if persist.IsDeny() {
			list = "always_deny"
		}
		_ = ask.evaluator.Store.Append(ask.evaluator.ProjectPath, list, ask.key)
	}

	select {
	case ask.ch <- allow:
		return &gilv1.AnswerPermissionResponse{Delivered: true}, nil
	default:
		// Already answered (channel buffer=1).
		return &gilv1.AnswerPermissionResponse{Delivered: false}, nil
	}
}

// resolveDecision maps the wire fields to (allow, PersistDecision). The
// PersistDecision drives what AnswerPermission persists; the bool drives
// what the AskCallback returns to the runner.
//
// When dec is UNSPECIFIED we honour the legacy `allow` bool and treat it
// as a once-tier answer (no persistence side-effect). When dec is set,
// `allow` is ignored — clients that speak the new protocol always set
// the enum.
func resolveDecision(dec gilv1.PermissionDecision, allow bool) (bool, permission.PersistDecision) {
	switch dec {
	case gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_ONCE:
		return true, permission.PersistAllowOnce
	case gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_SESSION:
		return true, permission.PersistAllowSession
	case gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_ALWAYS:
		return true, permission.PersistAllowAlways
	case gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ONCE:
		return false, permission.PersistDenyOnce
	case gilv1.PermissionDecision_PERMISSION_DECISION_DENY_SESSION:
		return false, permission.PersistDenySession
	case gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ALWAYS:
		return false, permission.PersistDenyAlways
	}
	// UNSPECIFIED → legacy bool path.
	if allow {
		return true, permission.PersistAllowOnce
	}
	return false, permission.PersistDenyOnce
}

// executeRun performs the actual agent loop execution and cleanup. It is called
// either directly (synchronous path) or from a detached goroutine.
func (s *RunService) executeRun(
	ctx context.Context,
	sessionID string,
	spec *gilv1.FrozenSpec,
	prov provider.Provider,
	model string,
	tools []tool.Tool,
	ver *verify.Runner,
	workspaceDir string,
) (*gilv1.StartRunResponse, error) {
	// DOCKER backend: spin up a per-session container and rewire the Bash tool.
	if spec.Workspace != nil && spec.Workspace.Backend == gilv1.WorkspaceBackend_DOCKER {
		image := "alpine:latest"
		if spec.Workspace.Path != "" {
			image = spec.Workspace.Path
		}
		dockerContainer := &docker.Container{
			Name:      "gil-" + sessionID,
			Image:     image,
			HostMount: workspaceDir,
		}
		if err := dockerContainer.Start(ctx); err != nil {
			_ = s.repo.UpdateStatus(ctx, sessionID, "stopped")
			return nil, status.Errorf(codes.Internal, "docker start: %v", err)
		}
		defer func() {
			// Best-effort cleanup with a short timeout context.
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = dockerContainer.Stop(stopCtx)
		}()
		// Rewire the Bash tool's Wrapper to point at the running container.
		for _, t := range tools {
			if b, ok := t.(*tool.Bash); ok {
				b.Wrapper = &docker.Wrapper{
					Container: dockerContainer.Name,
					WorkDir:   "/workspace",
				}
			}
		}
	}

	// SSH backend: push before run, pull after.
	// NOTE: RemoteDir mirrors LocalDir (same absolute path assumed on remote).
	// This is the Phase 9 convention; a future phase can add spec.workspace.remote_path.
	var sshSyncer *ssh.Syncer
	if spec.Workspace != nil && spec.Workspace.Backend == gilv1.WorkspaceBackend_SSH {
		if !ssh.SyncAvailable() {
			// Soft-warn: continue without sync if rsync absent. Agent can still
			// exec remote commands but file changes won't sync.
			// Emit a single event so observers see the limitation.
			// (stream not yet created here; note is emitted after stream init below)
			_ = sshSyncer // will remain nil, handled after stream init
		} else {
			host, port, key := ssh.ParseTarget(spec.Workspace.Path)
			sshSyncer = &ssh.Syncer{
				Wrapper:   &ssh.Wrapper{Host: host, Port: port, KeyPath: key},
				LocalDir:  workspaceDir,
				RemoteDir: workspaceDir,
				ExtraArgs: []string{"--exclude=.git/"},
			}
		}
	}

	// Cloud backends (MODAL, DAYTONA): Provision a sandbox and rewire Bash.
	var cloudSandbox *cloud.Sandbox
	var cloudProvider cloud.Provider
	if spec.Workspace != nil {
		switch spec.Workspace.Backend {
		case gilv1.WorkspaceBackend_MODAL:
			cloudProvider = modal.New()
		case gilv1.WorkspaceBackend_DAYTONA:
			cloudProvider = daytona.New()
		}
	}
	if cloudProvider != nil {
		sb, err := cloudProvider.Provision(ctx, cloud.ProvisionOptions{
			Image:        spec.Workspace.Path, // overload Path as image ref
			WorkspaceDir: workspaceDir,
			SessionID:    sessionID,
		})
		if err != nil {
			_ = s.repo.UpdateStatus(ctx, sessionID, "stopped")
			return nil, status.Errorf(codes.FailedPrecondition, "cloud provision (%s): %v", cloudProvider.Name(), err)
		}
		cloudSandbox = sb
		defer func() {
			tdCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = sb.Teardown(tdCtx)
		}()
		for _, t := range tools {
			if b, ok := t.(*tool.Bash); ok {
				b.Wrapper = sb.Wrapper
			}
		}
	}
	_ = cloudSandbox // suppress unused if no other refs

	// Create per-session event stream + persister.
	eventDir := filepath.Join(s.sessionDir(sessionID), "events")
	persister, err := event.NewPersister(eventDir)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create persister: %v", err)
	}
	defer persister.Close()

	stream := event.NewStream()

	// Register stream and progress snap under the lock.
	s.mu.Lock()
	s.runStreams[sessionID] = stream
	s.runProgress[sessionID] = &runProgressSnap{}
	s.mu.Unlock()

	// Cleanup on exit: remove stream, progress, and loop entries, decrement running gauge.
	defer func() {
		s.mu.Lock()
		delete(s.runStreams, sessionID)
		delete(s.runProgress, sessionID)
		delete(s.runLoops, sessionID)
		s.mu.Unlock()
		metrics.SessionsRunning.Dec()
	}()

	// Persistence subscriber: write every event to disk.
	persistSub := stream.Subscribe(256)
	persistDone := make(chan struct{})
	go func() {
		defer close(persistDone)
		for evt := range persistSub.Events() {
			_ = persister.Write(evt)
		}
	}()

	// Progress subscriber: track iterations and accumulated tokens.
	progSub := stream.Subscribe(256)
	progDone := make(chan struct{})
	go func() {
		defer close(progDone)
		for evt := range progSub.Events() {
			s.mu.Lock()
			snap := s.runProgress[sessionID]
			if snap != nil {
				if evt.Type == "iteration_start" {
					snap.iters++
				}
				if evt.Metrics.Tokens > 0 {
					snap.tokens += evt.Metrics.Tokens
				}
				// Budget signals: parse the JSON payload so the live
				// cost + sticky exceeded flag are available to
				// SessionService.toProto(). budget_warning carries the
				// running cost; budget_exceeded latches the alert bit.
				if evt.Type == "budget_warning" || evt.Type == "budget_exceeded" {
					var d struct {
						Reason string  `json:"reason"`
						Used   float64 `json:"used"`
					}
					if jerr := json.Unmarshal(evt.Data, &d); jerr == nil {
						if d.Reason == "cost" && d.Used > snap.cost {
							snap.cost = d.Used
						}
						if evt.Type == "budget_exceeded" {
							snap.budgetExceeded = true
							snap.budgetReason = d.Reason
						}
					}
				}
			}
			s.mu.Unlock()
		}
	}()

	// Metrics subscriber: bump Prometheus counters based on event type.
	metricsSub := stream.Subscribe(256)
	metricsDone := make(chan struct{})
	go func() {
		defer close(metricsDone)
		for evt := range metricsSub.Events() {
			switch evt.Type {
			case "iteration_start":
				metrics.RunIterationsTotal.Inc()
			case "compact_done":
				metrics.CompactDoneTotal.Inc()
			case "stuck_detected":
				var d map[string]any
				if err := json.Unmarshal(evt.Data, &d); err == nil {
					if p, ok := d["pattern"].(string); ok {
						metrics.StuckDetectedTotal.WithLabelValues(p).Inc()
					}
				}
			case "tool_result":
				var d map[string]any
				if err := json.Unmarshal(evt.Data, &d); err == nil {
					name, _ := d["name"].(string)
					isErr, _ := d["is_error"].(bool)
					result := "ok"
					if isErr {
						result = "error"
					}
					if name != "" {
						metrics.ToolCallsTotal.WithLabelValues(name, result).Inc()
					}
				}
			}
		}
	}()

	// MCP registry: load global + project mcp.toml entries and merge with
	// any spec-pinned servers (currently the spec only carries a name list
	// in Tools.McpServers, so the spec-side map is empty — but the merge
	// helper is shape-ready for the day spec embeds full launch records).
	// Spec wins on name collision; a single mcp_registry_loaded event makes
	// the merge visible in `gil events` so users can confirm what the
	// daemon actually saw.
	{
		regGlobal := ""
		if layout, lerr := paths.FromEnv(); lerr == nil {
			regGlobal = layout.MCPConfigFile()
		}
		regProject := ""
		if spec.Workspace != nil && spec.Workspace.Path != "" {
			regProject = workspace.LocalMCPFile(spec.Workspace.Path)
		}
		reg := &mcpregistry.Registry{GlobalPath: regGlobal, ProjectPath: regProject}
		registryServers, regErr := reg.Load()
		if regErr != nil {
			// Non-fatal: continue with spec-only. The event records the
			// failure so observers can diagnose without scraping logs.
			data, _ := json.Marshal(map[string]any{"err": regErr.Error()})
			_, _ = stream.Append(event.Event{
				Timestamp: time.Now().UTC(),
				Source:    event.SourceSystem,
				Kind:      event.KindNote,
				Type:      "mcp_registry_load_error",
				Data:      data,
			})
		} else {
			specServers := map[string]mcpregistry.Server{} // future: derived from spec.MCP
			merged := mergeMCPServers(specServers, registryServers)
			shadowed := shadowedRegistryNames(specServers, registryServers)
			names := make([]string, 0, len(merged))
			for n := range merged {
				names = append(names, n)
			}
			data, _ := json.Marshal(map[string]any{
				"global_path":  regGlobal,
				"project_path": regProject,
				"server_count": len(merged),
				"server_names": names,
				"shadowed":     shadowed,
			})
			_, _ = stream.Append(event.Event{
				Timestamp: time.Now().UTC(),
				Source:    event.SourceSystem,
				Kind:      event.KindNote,
				Type:      "mcp_registry_loaded",
				Data:      data,
			})
		}
	}

	// SSH sync: now that stream exists, emit unavailable warning or do push+defer-pull.
	if spec.Workspace != nil && spec.Workspace.Backend == gilv1.WorkspaceBackend_SSH {
		if sshSyncer == nil && ssh.SyncAvailable() == false {
			data, _ := json.Marshal(map[string]any{
				"reason": "rsync not in PATH; file changes will not sync",
			})
			_, _ = stream.Append(event.Event{
				Timestamp: time.Now().UTC(),
				Source:    event.SourceSystem,
				Kind:      event.KindNote,
				Type:      "ssh_sync_unavailable",
				Data:      data,
			})
		} else if sshSyncer != nil {
			// Push before run.
			pushCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			pushErr := sshSyncer.Push(pushCtx)
			cancel()
			if pushErr != nil {
				data, _ := json.Marshal(map[string]any{"phase": "push", "err": pushErr.Error()})
				_, _ = stream.Append(event.Event{
					Timestamp: time.Now().UTC(),
					Source:    event.SourceSystem,
					Kind:      event.KindNote,
					Type:      "ssh_sync_error",
					Data:      data,
				})
				sshSyncer = nil // disable pull-after
			} else {
				_, _ = stream.Append(event.Event{
					Timestamp: time.Now().UTC(),
					Source:    event.SourceSystem,
					Kind:      event.KindNote,
					Type:      "ssh_sync_pushed",
				})
			}
			// Defer pull-after (runs even on run error; uses background context).
			defer func() {
				if sshSyncer == nil {
					return
				}
				pullCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				if err := sshSyncer.Pull(pullCtx); err != nil {
					data, _ := json.Marshal(map[string]any{"phase": "pull", "err": err.Error()})
					_, _ = stream.Append(event.Event{
						Timestamp: time.Now().UTC(),
						Source:    event.SourceSystem,
						Kind:      event.KindNote,
						Type:      "ssh_sync_error",
						Data:      data,
					})
				} else {
					_, _ = stream.Append(event.Event{
						Timestamp: time.Now().UTC(),
						Source:    event.SourceSystem,
						Kind:      event.KindNote,
						Type:      "ssh_sync_pulled",
					})
				}
			}()
		}
	}

	bank := memory.New(filepath.Join(s.sessionDir(sessionID), "memory"))
	if err := bank.Init(); err != nil {
		return nil, status.Errorf(codes.Internal, "memory bank init: %v", err)
	}
	if _, err := bank.InitFromSpec(spec); err != nil {
		// Soft failure: Init already created the stubs; log via event and continue.
		_ = err
	}

	tools = append(tools,
		&tool.MemoryUpdate{Bank: bank},
		&tool.MemoryLoad{Bank: bank},
		&tool.Edit{WorkingDir: workspaceDir},
		&tool.ApplyPatch{WorkspaceDir: workspaceDir},
		// web_fetch / web_search are always-on. The fetch tool is
		// read-only and unconditionally available; the search tool
		// reports "no backend configured" gracefully when neither
		// BRAVE_SEARCH_API_KEY nor TAVILY_API_KEY is set, so the agent
		// can decide whether to fall back to web_fetch on a known URL.
		&tool.WebFetch{},
		&tool.WebSearch{},
	)

	// LSP manager + tool (Phase 18 Track C). One manager per run, scoped
	// to the workspace root; servers (gopls / pyright /
	// typescript-language-server / rust-analyzer) are spawned lazily on
	// first use, so runs that never touch the lsp tool pay nothing. The
	// deferred Shutdown reaps every spawned subprocess when the run
	// ends, even on panic / cancel.
	//
	// When workspaceDir is empty (rare — local backend without a
	// configured path) we still construct the manager so the lsp tool
	// can return its actionable "no language server configured" hint
	// instead of a misleading "tool unavailable" error.
	lspMgr := lsp.NewManager(workspaceDir)
	tools = append(tools, &tool.LSP{Manager: lspMgr, WorkingDir: workspaceDir})
	defer func() {
		// Best-effort: a 5-second budget is plenty for the polite
		// shutdown handshake; if any server hangs, the manager force-
		// kills internally so we never leak children.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = lspMgr.Shutdown(shutdownCtx)
	}()

	// exec tool: Recipe runner. Inner tools = everything else built so far.
	// Filtering happens inside ExecTool.Run defensively.
	execTool := &exec.ExecTool{Tools: tools}
	// Wire Emit so exec_step_* events flow to the per-session stream.
	execTool.Emit = func(typ string, data map[string]any) {
		b, _ := json.Marshal(data)
		_, _ = stream.Append(event.Event{
			Timestamp: time.Now().UTC(),
			Source:    event.SourceSystem,
			Kind:      event.KindNote,
			Type:      typ,
			Data:      b,
		})
	}
	tools = append(tools, execTool)

	loop := runner.NewAgentLoop(spec, prov, model, tools, ver)
	loop.Events = stream
	loop.Memory = bank

	// Register the loop pointer so RequestCompact / PostHint RPCs can
	// stage actions for the next iteration boundary. Cleared in the
	// existing exit-cleanup defer below alongside runStreams /
	// runProgress so the lifetime matches the run exactly.
	s.mu.Lock()
	s.runLoops[sessionID] = loop
	s.mu.Unlock()
	// Tell the runner where the user's project lives so it can run the
	// AGENTS.md / CLAUDE.md / .cursor/rules tree-walk and inject the
	// resulting context into the system prompt. Empty workspaceDir leaves
	// loadInstructions a no-op (the runner will not default to cwd, which
	// would otherwise leak whatever directory gild was launched from).
	loop.Workspace = workspaceDir

	// Wire compact_now tool: must be added after loop is created so we can pass
	// the loop itself as the CompactRequester. Appended last so it appears in
	// the tool list but doesn't shadow other tools.
	tools = append(tools, &tool.CompactNow{Requester: loop})
	// Rebuild the loop's internal tool set to include compact_now.
	loop.Tools = tools

	// Wire stuck detector so the long-run soak and production runs can detect
	// repeated-action patterns and surface them as events. No recovery strategy
	// here; every signal is unrecovered (counts toward the 3-signal abort).
	loop.StuckDetector = &stuck.Detector{Window: 50}

	// Build permission gate from spec.risk.autonomy. Returns nil for FULL.
	// Wrap the spec evaluator with EvaluatorWithStore so persistent
	// (always_allow / always_deny) and session-scoped lists layer on top.
	//
	// At FULL autonomy with NO persistent rules we keep Permission nil
	// (matches Phase 5/6 unrestricted behaviour). The moment the user
	// records any always_allow/always_deny for this project — or the
	// spec demands gating — we wrap so the persistent layer can still
	// intervene. This way the user's "FULL = trust the agent" choice is
	// not silently overridden by an empty wrapper that would still
	// promote unmatched calls to DecisionAsk.
	var autonomy gilv1.AutonomyDial
	if spec.Risk != nil {
		autonomy = spec.Risk.Autonomy
	}
	specEval := permission.FromAutonomy(autonomy)
	var persistStore *permission.PersistentStore
	if layout, lerr := paths.FromEnv(); lerr == nil {
		// EnsureDirs is idempotent and cheap; calling it here means the
		// PersistentStore can write its TOML even when gild was started
		// without going through the production main (e.g., in tests that
		// instantiate RunService directly with GIL_HOME pointed at a
		// fresh tmpdir). PersistentStore.Append assumes the parent dir
		// already exists, so this is the contract closure point.
		_ = layout.EnsureDirs()
		persistStore = &permission.PersistentStore{
			Path: filepath.Join(layout.State, "permissions.toml"),
		}
	}
	// Resolve project key: must be absolute. workspaceDir is already absolute
	// when set from spec.Workspace.Path / sess.WorkingDir, but defensively
	// run filepath.Abs so the EvaluatorWithStore.Load contract holds.
	projectPath := workspaceDir
	if abs, err := filepath.Abs(workspaceDir); err == nil {
		projectPath = abs
	}

	// Check whether the project already has persistent rules. We only
	// need to wrap when EITHER the spec gates anything OR the user has
	// stored persistent rules for this project. (A future TUI session
	// may still add session-scoped rules — those go through the
	// evaluator we keep on the pendingAsk so they are wired only when
	// the wrapper is in effect.)
	hasPersistentRules := false
	if persistStore != nil && filepath.IsAbs(projectPath) {
		if rules, _ := persistStore.Load(projectPath); rules != nil {
			if len(rules.AlwaysAllow) > 0 || len(rules.AlwaysDeny) > 0 {
				hasPersistentRules = true
			}
		}
	}

	var evaluator *permission.EvaluatorWithStore
	if specEval != nil || hasPersistentRules {
		evaluator = &permission.EvaluatorWithStore{
			Inner:       specEval,
			Store:       persistStore,
			ProjectPath: projectPath,
		}
		loop.Permission = evaluator
	}
	// else: Permission stays nil → runner skips the gate entirely.

	// Wire the interactive Ask callback: emits permission_ask events and blocks
	// waiting for an AnswerPermission RPC. Times out to deny after 60s.
	// (When evaluator is nil the callback still fires on the runner's
	// AskCallback path, but the runner never reaches Ask without an
	// evaluator, so this is essentially a no-op.)
	loop.AskCallback = s.makeAskCallback(sessionID, stream, evaluator)

	shadowBase := filepath.Join(s.sessionDir(sessionID), "shadow")
	loop.Checkpoint = checkpoint.New(workspaceDir, shadowBase)

	res, runErr := loop.Run(ctx)

	// Drain all subscribers before syncing to disk (order-independent).
	persistSub.Close()
	<-persistDone
	progSub.Close()
	<-progDone
	metricsSub.Close()
	<-metricsDone

	_ = persister.Sync()

	finalStatus := "stopped"
	if res != nil && res.Status == "done" {
		finalStatus = "done"
	}
	_ = s.repo.UpdateStatus(ctx, sessionID, finalStatus)

	if runErr != nil && res == nil {
		return nil, status.Errorf(codes.Internal, "run: %v", runErr)
	}

	resp := &gilv1.StartRunResponse{
		Status:     res.Status,
		Iterations: int32(res.Iterations),
		Tokens:     res.Tokens,
	}
	for _, vr := range res.VerifyAll {
		resp.VerifyResults = append(resp.VerifyResults, &gilv1.VerifyResult{
			Name: vr.Name, Passed: vr.Passed, ExitCode: int32(vr.ExitCode),
			Stdout: vr.Stdout, Stderr: vr.Stderr,
		})
	}
	if res.FinalError != nil {
		resp.ErrorMessage = res.FinalError.Error()
	}
	return resp, nil
}

// Restore rolls back the session's workspace to the given checkpoint step.
// Positive step counts oldest-first (step=1 → oldest); negative counts
// newest-first (step=-1 → most recent). step=0 is invalid.
func (s *RunService) Restore(ctx context.Context, req *gilv1.RestoreRequest) (*gilv1.RestoreResponse, error) {
	if req.Step == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "step must be non-zero (1-indexed; negatives count from latest)")
	}
	sess, err := s.repo.Get(ctx, req.SessionId)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
		}
		return nil, status.Errorf(codes.Internal, "session lookup: %v", err)
	}
	// Refuse restore on running sessions to avoid concurrent workspace mutation.
	if sess.Status == "running" {
		return nil, status.Errorf(codes.FailedPrecondition,
			"cannot restore session %q while running; stop it first", req.SessionId)
	}
	workspaceDir := sess.WorkingDir
	spec, err := specstore.NewStore(s.sessionDir(req.SessionId)).Load()
	if err == nil && spec.Workspace != nil && spec.Workspace.Path != "" {
		workspaceDir = spec.Workspace.Path
	}
	shadowBase := filepath.Join(s.sessionDir(req.SessionId), "shadow")
	sg := checkpoint.New(workspaceDir, shadowBase)
	commits, err := sg.ListCommits(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list checkpoints: %v", err)
	}
	if len(commits) == 0 {
		return nil, status.Errorf(codes.FailedPrecondition,
			"session %q has no checkpoints", req.SessionId)
	}
	// commits is newest-first. Resolve step:
	//   step  1 → oldest (commits[len-1])
	//   step  N → commits[len-N]
	//   step -1 → newest (commits[0])
	//   step -N → commits[N-1]
	var idx int
	if req.Step > 0 {
		idx = len(commits) - int(req.Step)
	} else {
		idx = int(-req.Step) - 1
	}
	if idx < 0 || idx >= len(commits) {
		return nil, status.Errorf(codes.OutOfRange,
			"step %d out of range (have %d checkpoints)", req.Step, len(commits))
	}
	target := commits[idx]
	if err := sg.Restore(ctx, target.SHA); err != nil {
		return nil, status.Errorf(codes.Internal, "restore: %v", err)
	}
	return &gilv1.RestoreResponse{
		CommitSha:        target.SHA,
		CommitMessage:    target.Message,
		TotalCheckpoints: int32(len(commits)),
	}, nil
}

// toProtoEvent converts a core event.Event to its proto representation.
func toProtoEvent(e event.Event) *gilv1.Event {
	return &gilv1.Event{
		Id:        e.ID,
		Timestamp: timestamppb.New(e.Timestamp),
		Source:    eventSourceToProto(e.Source),
		Kind:      eventKindToProto(e.Kind),
		Type:      e.Type,
		DataJson:  e.Data,
		Cause:     e.Cause,
		Metrics: &gilv1.EventMetrics{
			Tokens:    e.Metrics.Tokens,
			CostUsd:   e.Metrics.CostUSD,
			LatencyMs: e.Metrics.LatencyMs,
		},
	}
}

func eventSourceToProto(s event.Source) gilv1.EventSource {
	switch s {
	case event.SourceAgent:
		return gilv1.EventSource_AGENT
	case event.SourceUser:
		return gilv1.EventSource_USER
	case event.SourceEnvironment:
		return gilv1.EventSource_ENVIRONMENT
	case event.SourceSystem:
		return gilv1.EventSource_SYSTEM
	default:
		return gilv1.EventSource_SOURCE_UNSPECIFIED
	}
}

func eventKindToProto(k event.Kind) gilv1.EventKind {
	switch k {
	case event.KindAction:
		return gilv1.EventKind_ACTION
	case event.KindObservation:
		return gilv1.EventKind_OBSERVATION
	case event.KindNote:
		return gilv1.EventKind_NOTE
	default:
		return gilv1.EventKind_KIND_UNSPECIFIED
	}
}

// Tail subscribes to the per-session live event stream and forwards each
// event to the gRPC client. Returns NotFound if no run is active for the
// session. (Replay from disk is Phase 6.)
func (s *RunService) Tail(req *gilv1.TailRequest, stream gilv1.RunService_TailServer) error {
	s.mu.Lock()
	rs, ok := s.runStreams[req.SessionId]
	s.mu.Unlock()
	if !ok {
		return status.Errorf(codes.NotFound,
			"no active run for session %q (replay from disk is Phase 6)", req.SessionId)
	}

	sub := rs.Subscribe(256)
	defer sub.Close()

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-sub.Events():
			if !ok {
				return nil
			}
			if err := stream.Send(toProtoEvent(e)); err != nil {
				return err
			}
		}
	}
}
