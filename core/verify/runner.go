// Package verify executes spec.Verification.Checks (shell assertions) and
// reports per-check results. This is the objective stop signal — when all
// checks return their expected exit code, the run is considered complete.
package verify

import (
	"bytes"
	"context"
	"os/exec"
	"time"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// Result is the outcome of a single check.
type Result struct {
	Name     string
	Passed   bool
	ExitCode int
	Stdout   string // truncated to 4KB
	Stderr   string // truncated to 4KB
	Duration time.Duration
}

// Runner executes Check shell commands in a working directory.
type Runner struct {
	WorkingDir string
}

// NewRunner returns a Runner bound to workingDir.
func NewRunner(workingDir string) *Runner {
	return &Runner{WorkingDir: workingDir}
}

// RunAll runs every check and returns the per-check results plus an overall
// allPass flag. Each check has a 60s wall-clock timeout. Empty checks slice
// trivially returns (nil, true).
func (r *Runner) RunAll(ctx context.Context, checks []*gilv1.Check) ([]Result, bool) {
	if len(checks) == 0 {
		return nil, true
	}
	out := make([]Result, 0, len(checks))
	allPass := true
	for _, c := range checks {
		res := r.runOne(ctx, c)
		out = append(out, res)
		if !res.Passed {
			allPass = false
		}
	}
	return out, allPass
}

func (r *Runner) runOne(ctx context.Context, c *gilv1.Check) Result {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-c", c.Command)
	cmd.Dir = r.WorkingDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run() // we read exit code from ProcessState
	exitCode := cmd.ProcessState.ExitCode()

	expected := int(c.ExpectedExitCode)
	passed := exitCode == expected

	return Result{
		Name:     c.Name,
		Passed:   passed,
		ExitCode: exitCode,
		Stdout:   trunc4k(stdout.String()),
		Stderr:   trunc4k(stderr.String()),
		Duration: time.Since(start),
	}
}

func trunc4k(s string) string {
	const max = 4096
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
