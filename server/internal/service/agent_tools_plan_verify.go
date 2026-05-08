package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
)

// agent_tools_plan_verify.go ships the M5 verify-loop discipline tools
// (design doc §3.1, §3.2). plan_steps lets the agent declare what it's
// going to do *and the command that proves each step is done*; verify
// runs that command and transitions the step's status.
//
// Status transitions are deliberately one-way at the system level:
//
//	pending ──verify ok───►  verified
//	pending ──verify err──►  failed
//	failed  ──verify ok───►  verified
//
// The agent cannot self-mark a step verified by sending status in
// plan_steps — only verify can do that, and only by running the
// declared acceptance_check command. This is the codex differentiator:
// codex's "verify after edit" is a prompt suggestion; here it's a
// state machine that gates progression.
//
// V1 scope: per-session in-memory store (lost on daemon restart, same
// pattern as todoStore + chatHistory). SQLite persistence is a
// follow-up alongside the chatHistory persistence work.

// --- plan store ------------------------------------------------------

type planStep struct {
	ID              int    `json:"id"`
	Description     string `json:"description"`
	AcceptanceCheck string `json:"acceptance_check"`
	Status          string `json:"status"` // "pending" | "verified" | "failed"
	LastFailure     string `json:"last_failure,omitempty"`
}

type planStore struct {
	mu    sync.Mutex
	items map[string][]*planStep
}

var globalPlanStore = &planStore{items: make(map[string][]*planStep)}

// replace overwrites the plan for sessionID with the given descriptions.
// Preserves status (and last_failure) for items whose
// (description, acceptance_check) pair matches an existing entry.
func (p *planStore) replace(sessionID string, descs []planStepInput) []*planStep {
	p.mu.Lock()
	defer p.mu.Unlock()

	prior := p.items[sessionID]
	priorByKey := make(map[string]*planStep, len(prior))
	for _, s := range prior {
		priorByKey[s.Description+"\x00"+s.AcceptanceCheck] = s
	}

	out := make([]*planStep, len(descs))
	for i, d := range descs {
		key := d.Description + "\x00" + d.AcceptanceCheck
		if existing, ok := priorByKey[key]; ok {
			out[i] = &planStep{
				ID:              i + 1,
				Description:     d.Description,
				AcceptanceCheck: d.AcceptanceCheck,
				Status:          existing.Status,
				LastFailure:     existing.LastFailure,
			}
		} else {
			out[i] = &planStep{
				ID:              i + 1,
				Description:     d.Description,
				AcceptanceCheck: d.AcceptanceCheck,
				Status:          "pending",
			}
		}
	}
	p.items[sessionID] = out
	return out
}

func (p *planStore) snapshot(sessionID string) []*planStep {
	p.mu.Lock()
	defer p.mu.Unlock()
	src := p.items[sessionID]
	out := make([]*planStep, len(src))
	for i, s := range src {
		cp := *s
		out[i] = &cp
	}
	return out
}

func (p *planStore) markVerified(sessionID string, stepID int) error {
	return p.transition(sessionID, stepID, "verified", "")
}

func (p *planStore) markFailed(sessionID string, stepID int, errMsg string) error {
	return p.transition(sessionID, stepID, "failed", errMsg)
}

func (p *planStore) transition(sessionID string, stepID int, status, lastFailure string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	steps := p.items[sessionID]
	if stepID < 1 || stepID > len(steps) {
		return fmt.Errorf("step %d not found (plan has %d steps)", stepID, len(steps))
	}
	s := steps[stepID-1]
	s.Status = status
	if status == "failed" {
		s.LastFailure = lastFailure
	} else {
		s.LastFailure = ""
	}
	return nil
}

func renderPlan(steps []*planStep) string {
	if len(steps) == 0 {
		return "(no plan)"
	}
	var b strings.Builder
	for _, s := range steps {
		mark := "[ ]"
		switch s.Status {
		case "verified":
			mark = "[✓]"
		case "failed":
			mark = "[✗]"
		}
		fmt.Fprintf(&b, "%s %d. %s\n    check: %s\n", mark, s.ID, s.Description, s.AcceptanceCheck)
		if s.LastFailure != "" {
			fmt.Fprintf(&b, "    last failure: %s\n", oneLine(s.LastFailure))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// --- plan_steps tool -------------------------------------------------

type planStepInput struct {
	Description     string `json:"description"`
	AcceptanceCheck string `json:"acceptance_check"`
}

type toolPlanSteps struct{}

func (t *toolPlanSteps) name() string { return "plan_steps" }

func (t *toolPlanSteps) description() string {
	return "Declare or update a per-session execution plan. Each step has a description and an acceptance_check command. " +
		"Replaces any existing plan; pass the full list every call. " +
		"The status of each step (pending/verified/failed) is system-managed: only the verify tool can mark a step verified or failed by running its acceptance_check. " +
		"Prefer plan_steps over todowrite when the work needs verification gates; use todowrite for free-form scratchpad lists."
}

func (t *toolPlanSteps) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"items":{
				"type":"array",
				"description":"Full ordered plan. Replaces the existing plan; pass [] to clear.",
				"items":{
					"type":"object",
					"properties":{
						"description":{"type":"string","description":"What this step accomplishes."},
						"acceptance_check":{"type":"string","description":"Shell command whose exit 0 proves the step is done (e.g. 'go build ./...', 'pytest tests/foo.py'). Run by verify tool."}
					},
					"required":["description","acceptance_check"],
					"additionalProperties":false
				}
			}
		},
		"required":["items"],
		"additionalProperties":false
	}`)
}

func (t *toolPlanSteps) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Items []planStepInput `json:"items"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	for i, it := range args.Items {
		if strings.TrimSpace(it.Description) == "" {
			return provider.ToolResult{
				Content: fmt.Sprintf("item %d: description is empty", i+1),
				IsError: true,
			}, nil
		}
		if strings.TrimSpace(it.AcceptanceCheck) == "" {
			return provider.ToolResult{
				Content: fmt.Sprintf("item %d: acceptance_check is empty — every step must declare a verify command", i+1),
				IsError: true,
			}, nil
		}
	}
	steps := globalPlanStore.replace(sessionID, args.Items)
	return provider.ToolResult{Content: renderPlan(steps)}, nil
}

// --- verify tool -----------------------------------------------------

const (
	verifyTimeout       = 60 * time.Second
	verifyMaxOutput     = 16 * 1024
	verifyMaxFailures   = 8
	verifyTailLineCount = 20
)

type toolVerify struct {
	repo    *session.Repo
	tracker *turnDiffTracker
}

func (t *toolVerify) name() string { return "verify" }

func (t *toolVerify) description() string {
	return "Run a verification command and return a structured result (exit_code, duration, stdout/stderr tails, parsed test failures). " +
		"When step_id is provided AND a plan_steps plan exists, success transitions the step to 'verified'; failure transitions it to 'failed' with the stderr tail attached. " +
		"Use after every code-changing tool call (write_file, edit_file, apply_patch) — the system enforces verify-before-progression. " +
		"Capped at 60s and 16 KB combined output."
}

func (t *toolVerify) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"description":{"type":"string","description":"Short label for the check (e.g. 'build passes', 'auth tests')."},
			"command":{"type":"string","description":"Shell command to run. Run via bash -c in the session working directory."},
			"step_id":{"type":"integer","description":"Optional plan step id to transition on result. Must be between 1 and len(plan).","minimum":1}
		},
		"required":["description","command"],
		"additionalProperties":false
	}`)
}

func (t *toolVerify) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Description string `json:"description"`
		Command     string `json:"command"`
		StepID      int    `json:"step_id"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Command) == "" {
		return provider.ToolResult{Content: "command is empty", IsError: true}, nil
	}
	wd, err := sessionWD(ctx, t.repo, sessionID)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	cctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	t0 := time.Now()
	cmd := exec.CommandContext(cctx, "bash", "-c", args.Command)
	cmd.Dir = wd
	// verify is allowed to mutate the FS (e.g. tests writing test
	// artifacts), so flag the diff tracker as polluted to be safe.
	if t.tracker != nil {
		t.tracker.markExternal(sessionID)
	}
	out, runErr := cmd.CombinedOutput()
	duration := time.Since(t0)

	exitCode := 0
	timedOut := false
	if runErr != nil {
		if cctx.Err() == context.DeadlineExceeded {
			timedOut = true
			exitCode = 124
		} else {
			var ee *exec.ExitError
			if errors.As(runErr, &ee) {
				exitCode = ee.ExitCode()
			} else {
				exitCode = -1
			}
		}
	}

	body := string(out)
	truncated := len(out) > verifyMaxOutput
	if truncated {
		body = string(out[:verifyMaxOutput])
	}
	tail := tailLines(body, verifyTailLineCount)
	failures := parseTestFailures(body)

	pass := exitCode == 0 && !timedOut

	// Transition the linked plan step.
	var transition string
	if args.StepID > 0 {
		var err error
		if pass {
			err = globalPlanStore.markVerified(sessionID, args.StepID)
			transition = fmt.Sprintf("step %d → verified", args.StepID)
		} else {
			err = globalPlanStore.markFailed(sessionID, args.StepID, tail)
			transition = fmt.Sprintf("step %d → failed", args.StepID)
		}
		if err != nil {
			transition = fmt.Sprintf("(plan transition error: %v)", err)
		}
	}

	var b strings.Builder
	verdict := "PASS"
	if !pass {
		verdict = "FAIL"
	}
	if timedOut {
		verdict = "TIMEOUT"
	}
	fmt.Fprintf(&b, "[%s] %s — exit=%d, duration=%s\n", verdict, args.Description, exitCode, duration.Truncate(time.Millisecond))
	fmt.Fprintf(&b, "$ %s\n", oneLine(args.Command))
	if transition != "" {
		fmt.Fprintf(&b, "%s\n", transition)
	}
	if len(failures) > 0 {
		fmt.Fprintf(&b, "parsed failures (%d):\n", len(failures))
		for _, f := range failures {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	if tail != "" {
		fmt.Fprintf(&b, "--- tail ---\n%s", tail)
		if !strings.HasSuffix(tail, "\n") {
			b.WriteString("\n")
		}
	}
	if truncated {
		fmt.Fprintf(&b, "(output truncated to %d bytes)\n", verifyMaxOutput)
	}

	// Append the current plan so the agent always sees the post-verify
	// state of every step.
	plan := globalPlanStore.snapshot(sessionID)
	if len(plan) > 0 {
		fmt.Fprintf(&b, "\n--- plan ---\n%s\n", renderPlan(plan))
	}

	return provider.ToolResult{
		Content: strings.TrimRight(b.String(), "\n"),
		IsError: !pass,
	}, nil
}

// --- helpers ---------------------------------------------------------

func tailLines(s string, n int) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// parseTestFailures pulls test names out of common test runner output:
//
//	pytest:  "FAILED tests/foo.py::test_bar"
//	go test: "--- FAIL: TestFoo"
//	jest:    "  ✕ test name"
//
// Best-effort. Returns at most verifyMaxFailures entries.
var (
	rePytest = regexp.MustCompile(`^FAILED\s+(\S+)`)
	reGoTest = regexp.MustCompile(`^---\s+FAIL:\s+(\S+)`)
	reJest   = regexp.MustCompile(`^\s*✕\s+(.+)$`)
)

func parseTestFailures(out string) []string {
	var failures []string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case rePytest.MatchString(line):
			m := rePytest.FindStringSubmatch(line)
			failures = append(failures, "pytest: "+m[1])
		case reGoTest.MatchString(line):
			m := reGoTest.FindStringSubmatch(line)
			failures = append(failures, "go: "+m[1])
		case reJest.MatchString(line):
			m := reJest.FindStringSubmatch(line)
			failures = append(failures, "jest: "+m[1])
		}
		if len(failures) >= verifyMaxFailures {
			break
		}
	}
	return failures
}
