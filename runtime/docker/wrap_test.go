package docker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrap_NoContainer_PassesThrough(t *testing.T) {
	w := &Wrapper{}
	out := w.Wrap("echo", "hi")
	require.Equal(t, []string{"echo", "hi"}, out)
}

func TestWrap_BasicExec(t *testing.T) {
	w := &Wrapper{Container: "myc"}
	out := w.Wrap("ls", "-la")
	require.Equal(t, []string{"docker", "exec", "myc", "ls", "-la"}, out)
}

func TestWrap_WithWorkDir(t *testing.T) {
	w := &Wrapper{Container: "myc", WorkDir: "/workspace"}
	out := w.Wrap("ls")
	require.Contains(t, out, "-w")
	require.Contains(t, out, "/workspace")
}

func TestWrap_WithUser(t *testing.T) {
	w := &Wrapper{Container: "myc", User: "1000"}
	out := w.Wrap("ls")
	// -u must appear before container name
	var idxU, idxC int
	for i, a := range out {
		if a == "-u" {
			idxU = i
		}
		if a == "myc" {
			idxC = i
		}
	}
	require.Greater(t, idxU, 0)
	require.Greater(t, idxC, idxU)
}

func TestWrap_CustomDockerBin(t *testing.T) {
	w := &Wrapper{Container: "myc", DockerBin: "/usr/local/bin/docker"}
	out := w.Wrap("ls")
	require.Equal(t, "/usr/local/bin/docker", out[0])
}

func TestWrap_ImplementsCommandWrapperShape(t *testing.T) {
	// Compile-time check — same Wrap signature as bwrap and seatbelt
	var _ interface{ Wrap(string, ...string) []string } = (*Wrapper)(nil)
}

func TestAvailable_NoPanic(t *testing.T) {
	require.NotPanics(t, func() { _ = Available() })
}

func TestContainer_StartRequiresImage(t *testing.T) {
	c := &Container{Name: "x"}
	err := c.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "Image is required")
}

func TestContainer_StartRequiresName(t *testing.T) {
	c := &Container{Image: "alpine"}
	err := c.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "Name is required")
}

func TestContainer_StopWithoutStartIsNoop(t *testing.T) {
	c := &Container{Name: "x", Image: "alpine"}
	require.NoError(t, c.Stop(context.Background()))
}

func TestContainer_StartStop_SmokeIfDockerAvailable(t *testing.T) {
	if !Available() {
		t.Skip("docker not available")
	}
	// Skip in CI to avoid dependency on actually pulling an image
	t.Skip("smoke test requires docker daemon + image; run manually")
	// Manual run:
	// c := &Container{Name: "gil-test", Image: "alpine:latest", HostMount: "/tmp"}
	// require.NoError(t, c.Start(context.Background()))
	// defer c.Stop(context.Background())
	// ...
}
