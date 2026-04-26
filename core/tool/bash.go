package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// Bash runs shell commands in the project's working directory with a timeout.
type Bash struct {
	WorkingDir string
	Timeout    time.Duration  // per-command timeout; 0 → 60s default
	Wrapper    CommandWrapper // optional; if non-nil, command is wrapped before exec
}

const bashSchema = `{
  "type":"object",
  "properties":{
    "command":{"type":"string","description":"shell command to execute"}
  },
  "required":["command"]
}`

// Name implements Tool.
func (b *Bash) Name() string { return "bash" }

// Description implements Tool.
func (b *Bash) Description() string {
	return "Execute a shell command in the project working directory and return stdout+stderr."
}

// Schema implements Tool.
func (b *Bash) Schema() json.RawMessage { return json.RawMessage(bashSchema) }

// Run executes the command via `bash -c` with a hard timeout. Truncates large
// stdout/stderr. Returns IsError=true when the command exits non-zero.
func (b *Bash) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("bash unmarshal: %w", err)
	}
	if args.Command == "" {
		return Result{Content: "command is empty", IsError: true}, nil
	}

	timeout := b.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Remote-executor fast path: HTTP-bound backends (e.g., Daytona REST) hand
	// back stdout/stderr/exit in a single call, so there is no exec.Cmd to
	// build. Detected by an optional RemoteExecutor interface on the wrapper;
	// wrappers that don't implement it fall through to the legacy argv path.
	if re, ok := b.Wrapper.(RemoteExecutor); ok && re != nil {
		stdout, stderr, exitCode, runErr := re.ExecRemote(cctx, "bash", []string{"-c", args.Command})
		output := fmt.Sprintf("exit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s",
			exitCode, truncate(stdout, 8192), truncate(stderr, 4096))
		return Result{Content: output, IsError: runErr != nil || exitCode != 0}, nil
	}

	argv := []string{"bash", "-c", args.Command}
	if b.Wrapper != nil {
		argv = b.Wrapper.Wrap(argv[0], argv[1:]...)
	}
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = b.WorkingDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := cmd.ProcessState.ExitCode()

	output := fmt.Sprintf("exit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s",
		exitCode, truncate(stdout.String(), 8192), truncate(stderr.String(), 4096))

	return Result{Content: output, IsError: runErr != nil}, nil
}

// truncate returns s if len(s) <= max, else s[:max] + ellipsis.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
