package daytona

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/runtime/cloud"
)

func TestProvider_Available_RequiresAPIKey(t *testing.T) {
	saved := os.Getenv(EnvAPIKey)
	defer os.Setenv(EnvAPIKey, saved)
	os.Unsetenv(EnvAPIKey)
	require.False(t, New().Available())
	os.Setenv(EnvAPIKey, "test-key")
	require.True(t, New().Available())
}

func TestProvider_Provision_NotConfigured_ReturnsErr(t *testing.T) {
	saved := os.Getenv(EnvAPIKey)
	defer os.Setenv(EnvAPIKey, saved)
	os.Unsetenv(EnvAPIKey)
	p := New()
	_, err := p.Provision(context.Background(), cloud.ProvisionOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, cloud.ErrNotConfigured))
}

func TestProvider_Provision_Configured_ReturnsSandbox(t *testing.T) {
	saved := os.Getenv(EnvAPIKey)
	defer os.Setenv(EnvAPIKey, saved)
	os.Setenv(EnvAPIKey, "test-key")
	p := New()
	sb, err := p.Provision(context.Background(), cloud.ProvisionOptions{
		Image:     "alpine",
		SessionID: "abc",
	})
	require.NoError(t, err)
	require.NotNil(t, sb)
	require.Equal(t, "alpine", sb.Info["image"])
	require.Equal(t, "gil-abc", sb.Info["workspace"])
	require.NotNil(t, sb.Teardown)
}

func TestWrapper_Wrap(t *testing.T) {
	w := &Wrapper{WorkspaceName: "gil-x"}
	out := w.Wrap("ls", "-la")
	require.Equal(t, []string{"daytona", "exec", "gil-x", "--", "ls", "-la"}, out)
}

func TestProvider_ImplementsCloudProvider(t *testing.T) {
	var _ cloud.Provider = (*Provider)(nil)
}

func TestWrapper_ImplementsCloudWrapper(t *testing.T) {
	var _ cloud.CommandWrapper = (*Wrapper)(nil)
}
