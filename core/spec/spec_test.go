package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func validSpec() *gilv1.FrozenSpec {
	return &gilv1.FrozenSpec{
		SpecId:    "01HXY1",
		SessionId: "01HSESS",
		Goal: &gilv1.Goal{
			OneLiner:               "build a CLI",
			SuccessCriteriaNatural: []string{"a", "b", "c"},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"go"}},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{
			{Name: "build", Kind: gilv1.CheckKind_SHELL, Command: "go build"},
		}},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX},
		Models:    &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"}},
		Risk:      &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL},
	}
}

func TestSpec_Freeze_ProducesLock(t *testing.T) {
	fs := validSpec()

	lock, err := Freeze(fs)
	require.NoError(t, err)
	require.Len(t, lock, 64) // SHA-256 hex
	require.Equal(t, lock, fs.ContentSha256)
}

func TestSpec_VerifyLock_DetectsTamper(t *testing.T) {
	fs := validSpec()
	_, err := Freeze(fs)
	require.NoError(t, err)

	ok, err := VerifyLock(fs)
	require.NoError(t, err)
	require.True(t, ok)

	// 변형 후 lock 검증 실패해야
	fs.Goal.OneLiner = "tampered"
	ok, err = VerifyLock(fs)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSpec_RequiredSlots_AllFilled(t *testing.T) {
	require.True(t, AllRequiredSlotsFilled(validSpec()))

	missing := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "x"}}
	require.False(t, AllRequiredSlotsFilled(missing))
}

func TestSpec_Freeze_NilSpec_ReturnsError(t *testing.T) {
	_, err := Freeze(nil)
	require.Error(t, err)
}

func TestSpec_VerifyLock_NilSpec_ReturnsError(t *testing.T) {
	_, err := VerifyLock(nil)
	require.Error(t, err)
}
