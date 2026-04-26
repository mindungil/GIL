package cloud

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubProvider satisfies Provider; used to assert interface shape.
type stubProvider struct {
	available bool
	err       error
}

func (s *stubProvider) Name() string    { return "stub" }
func (s *stubProvider) Available() bool { return s.available }
func (s *stubProvider) Provision(ctx context.Context, opts ProvisionOptions) (*Sandbox, error) {
	if !s.available {
		return nil, ErrNotConfigured
	}
	if s.err != nil {
		return nil, s.err
	}
	return &Sandbox{
		Wrapper:  &stubWrapper{},
		Teardown: func(context.Context) error { return nil },
		Info:     map[string]string{"vm_id": "test123"},
	}, nil
}

type stubWrapper struct{}

func (s *stubWrapper) Wrap(cmd string, args ...string) []string {
	return append([]string{cmd}, args...)
}

func TestProvider_InterfaceShape(t *testing.T) {
	var _ Provider = (*stubProvider)(nil)
	var _ CommandWrapper = (*stubWrapper)(nil)
}

func TestStubProvider_Unavailable_ReturnsErrNotConfigured(t *testing.T) {
	p := &stubProvider{available: false}
	_, err := p.Provision(context.Background(), ProvisionOptions{})
	require.ErrorIs(t, err, ErrNotConfigured)
}

func TestStubProvider_Available_ReturnsSandbox(t *testing.T) {
	p := &stubProvider{available: true}
	sb, err := p.Provision(context.Background(), ProvisionOptions{Image: "alpine"})
	require.NoError(t, err)
	require.NotNil(t, sb.Wrapper)
	require.NotNil(t, sb.Teardown)
	require.Equal(t, "test123", sb.Info["vm_id"])
}

func TestStubProvider_PropagatesError(t *testing.T) {
	p := &stubProvider{available: true, err: errors.New("boom")}
	_, err := p.Provision(context.Background(), ProvisionOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}
