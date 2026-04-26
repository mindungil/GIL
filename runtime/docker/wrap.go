// Package docker provides a CommandWrapper that routes commands into a
// Docker container via `docker exec`. Mirrors runtime/local.Sandbox so
// core/tool.Bash can use either based on spec.workspace.backend.
//
// For Phase 7 this is "per-command exec" against a CALLER-MANAGED container
// (the caller is responsible for `docker run -d --name <name> <image>` and
// stopping it after the run). RunService bootstraps the container before
// the AgentLoop starts and tears it down on cleanup.
package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Wrapper builds `docker exec` argument lists.
type Wrapper struct {
	Container string // container name; required for Wrap to produce useful output
	WorkDir   string // optional -w <path>; if empty, uses container default
	DockerBin string // defaults to "docker"
	User      string // optional -u <user>; if empty, uses container default
}

// Wrap returns the argv that runs `cmd args...` inside the configured
// container via `docker exec`. When Container is empty, returns the
// command unchanged (treats it as a passthrough — useful for tests and
// graceful degradation).
//
// Layout: ["docker", "exec", ["-w", workdir]?, ["-u", user]?, container, cmd, args...]
func (w *Wrapper) Wrap(cmd string, args ...string) []string {
	if w.Container == "" {
		out := make([]string, 0, 1+len(args))
		return append(append(out, cmd), args...)
	}
	bin := w.DockerBin
	if bin == "" {
		bin = "docker"
	}
	out := []string{bin, "exec"}
	if w.WorkDir != "" {
		out = append(out, "-w", w.WorkDir)
	}
	if w.User != "" {
		out = append(out, "-u", w.User)
	}
	out = append(out, w.Container, cmd)
	out = append(out, args...)
	return out
}

// Available returns true when the docker binary is callable.
func Available() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// Container is a short-lived session container. Start launches it; Stop
// removes it. Use defer Stop after Start to guarantee cleanup.
type Container struct {
	Name      string
	Image     string
	HostMount string // host path bound into the container at /workspace
	DockerBin string

	started bool
}

// Start spins up the container in detached mode with the workspace mount.
// Equivalent to:
//
//	docker run -d --rm --name <Name> -v <HostMount>:/workspace -w /workspace <Image> sleep infinity
//
// `sleep infinity` keeps the container alive so subsequent `docker exec`
// calls can land. RunService stops it after AgentLoop.Run returns.
func (c *Container) Start(ctx context.Context) error {
	bin := c.DockerBin
	if bin == "" {
		bin = "docker"
	}
	if c.Image == "" {
		return fmt.Errorf("docker.Container.Start: Image is required")
	}
	if c.Name == "" {
		return fmt.Errorf("docker.Container.Start: Name is required")
	}

	args := []string{"run", "-d", "--rm", "--name", c.Name}
	if c.HostMount != "" {
		args = append(args, "-v", c.HostMount+":/workspace", "-w", "/workspace")
	}
	args = append(args, c.Image, "sleep", "infinity")

	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	c.started = true
	return nil
}

// Stop removes the container. Idempotent (safe to call without Start).
func (c *Container) Stop(ctx context.Context) error {
	if !c.started {
		return nil
	}
	bin := c.DockerBin
	if bin == "" {
		bin = "docker"
	}
	cmd := exec.CommandContext(ctx, bin, "rm", "-f", c.Name)
	out, err := cmd.CombinedOutput()
	c.started = false // mark stopped even on error so we don't retry
	if err != nil {
		return fmt.Errorf("docker rm -f: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
