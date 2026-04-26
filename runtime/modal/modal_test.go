package modal

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/runtime/cloud"
)

func TestProvider_Available_RequiresEnvVars(t *testing.T) {
	// Save and clear env to ensure deterministic test
	saved1 := os.Getenv(EnvTokenID)
	saved2 := os.Getenv(EnvTokenSecret)
	defer os.Setenv(EnvTokenID, saved1)
	defer os.Setenv(EnvTokenSecret, saved2)
	os.Unsetenv(EnvTokenID)
	os.Unsetenv(EnvTokenSecret)

	p := New()
	require.False(t, p.Available())
}

func TestProvider_Provision_NotConfigured_ReturnsErr(t *testing.T) {
	saved1 := os.Getenv(EnvTokenID)
	defer os.Setenv(EnvTokenID, saved1)
	os.Unsetenv(EnvTokenID)
	p := New()
	_, err := p.Provision(context.Background(), cloud.ProvisionOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, cloud.ErrNotConfigured))
}

func TestProvider_ImplementsCloudProvider(t *testing.T) {
	var _ cloud.Provider = (*Provider)(nil)
}

func TestWrapper_Wrap_BuildsModalSandboxExec(t *testing.T) {
	w := &Wrapper{ModalBin: "modal", SandboxName: "gil-abc"}
	out := w.Wrap("ls", "-la")
	require.Equal(t, []string{"modal", "sandbox", "exec", "gil-abc", "--", "ls", "-la"}, out)
}

func TestWrapper_Wrap_DefaultsModalBin(t *testing.T) {
	w := &Wrapper{SandboxName: "gil-x"}
	out := w.Wrap("ls")
	require.Equal(t, "modal", out[0])
}

func TestWrapper_ImplementsCloudWrapper(t *testing.T) {
	var _ cloud.CommandWrapper = (*Wrapper)(nil)
}
