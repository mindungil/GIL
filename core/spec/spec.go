package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"google.golang.org/protobuf/proto"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// Freeze deterministically marshals the FrozenSpec, computes its SHA-256,
// stores the hex digest in fs.ContentSha256, and returns the digest.
// Mutates fs.ContentSha256 as a side effect (the hash includes itself,
// so we clear-then-set; this is intentional and idempotent — calling Freeze
// multiple times on the same content produces the same digest).
// Returns an error if fs is nil.
func Freeze(fs *gilv1.FrozenSpec) (string, error) {
	if fs == nil {
		return "", fmt.Errorf("spec.Freeze: nil spec")
	}
	fs.ContentSha256 = ""
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(fs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	hex := hex.EncodeToString(sum[:])
	fs.ContentSha256 = hex
	return hex, nil
}

// VerifyLock validates that the spec's ContentSha256 matches the actual hash of its content.
func VerifyLock(fs *gilv1.FrozenSpec) (bool, error) {
	if fs == nil {
		return false, fmt.Errorf("spec.VerifyLock: nil spec")
	}
	stored := fs.ContentSha256
	fs.ContentSha256 = ""
	defer func() { fs.ContentSha256 = stored }()

	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(fs)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) == stored, nil
}

// AllRequiredSlotsFilled checks whether all required slots defined in design.md 5.3 are filled.
func AllRequiredSlotsFilled(fs *gilv1.FrozenSpec) bool {
	if fs == nil {
		return false
	}
	if fs.Goal == nil || fs.Goal.OneLiner == "" || len(fs.Goal.SuccessCriteriaNatural) < 3 {
		return false
	}
	if fs.Constraints == nil || len(fs.Constraints.TechStack) == 0 {
		return false
	}
	if fs.Verification == nil || len(fs.Verification.Checks) == 0 {
		return false
	}
	if fs.Workspace == nil || fs.Workspace.Backend == gilv1.WorkspaceBackend_BACKEND_UNSPECIFIED {
		return false
	}
	if fs.Models == nil || fs.Models.Main == nil || fs.Models.Main.Provider == "" || fs.Models.Main.ModelId == "" {
		return false
	}
	if fs.Risk == nil || fs.Risk.Autonomy == gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED {
		return false
	}
	return true
}

// requiredSlotChecks is the ordered list of required slot predicates.
// Each entry returns true when that slot is filled. The list mirrors
// AllRequiredSlotsFilled exactly so counts stay in sync.
var requiredSlotChecks = []func(*gilv1.FrozenSpec) bool{
	func(fs *gilv1.FrozenSpec) bool {
		return fs.Goal != nil && fs.Goal.OneLiner != ""
	},
	func(fs *gilv1.FrozenSpec) bool {
		return fs.Goal != nil && len(fs.Goal.SuccessCriteriaNatural) >= 3
	},
	func(fs *gilv1.FrozenSpec) bool {
		return fs.Constraints != nil && len(fs.Constraints.TechStack) > 0
	},
	func(fs *gilv1.FrozenSpec) bool {
		return fs.Verification != nil && len(fs.Verification.Checks) > 0
	},
	func(fs *gilv1.FrozenSpec) bool {
		return fs.Workspace != nil && fs.Workspace.Backend != gilv1.WorkspaceBackend_BACKEND_UNSPECIFIED
	},
	func(fs *gilv1.FrozenSpec) bool {
		return fs.Models != nil && fs.Models.Main != nil &&
			fs.Models.Main.Provider != "" && fs.Models.Main.ModelId != ""
	},
	func(fs *gilv1.FrozenSpec) bool {
		return fs.Risk != nil && fs.Risk.Autonomy != gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED
	},
}

// RequiredSlotTotal returns the total number of required slot checks.
func RequiredSlotTotal() int { return len(requiredSlotChecks) }

// FilledSlotCount returns how many of the required slots currently have content.
func FilledSlotCount(fs *gilv1.FrozenSpec) int {
	if fs == nil {
		return 0
	}
	n := 0
	for _, check := range requiredSlotChecks {
		if check(fs) {
			n++
		}
	}
	return n
}
