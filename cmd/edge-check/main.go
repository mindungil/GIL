package main

import (
	"fmt"
	"github.com/mindungil/gil/core/spec"
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

func main() {
	fmt.Println("=== Testing Freeze idempotency ===")
	fs := validSpec()
	lock1, err1 := spec.Freeze(fs)
	if err1 != nil {
		panic(err1)
	}
	lock2, err2 := spec.Freeze(fs)
	if err2 != nil {
		panic(err2)
	}
	if lock1 == lock2 {
		fmt.Printf("PASS: Freeze is idempotent: %s\n", lock1)
	} else {
		fmt.Printf("FAIL: Freeze not idempotent: %s != %s\n", lock1, lock2)
	}
	
	fmt.Println("\n=== Testing VerifyLock doesn't mutate ===")
	fs = validSpec()
	spec.Freeze(fs)
	original := fs.ContentSha256
	ok, err := spec.VerifyLock(fs)
	if err != nil {
		panic(err)
	}
	if fs.ContentSha256 == original && ok {
		fmt.Printf("PASS: VerifyLock preserved ContentSha256 and returned true: %s\n", fs.ContentSha256)
	} else {
		fmt.Printf("FAIL: VerifyLock mutated or returned false: original=%s, now=%s, ok=%v\n", original, fs.ContentSha256, ok)
	}
	
	fmt.Println("\n=== Testing VerifyLock with tampered spec ===")
	fs = validSpec()
	spec.Freeze(fs)
	fs.Goal.OneLiner = "tampered"
	ok, err = spec.VerifyLock(fs)
	if err != nil {
		panic(err)
	}
	if !ok && fs.ContentSha256 != "" {
		fmt.Printf("PASS: VerifyLock detected tamper and restored field: ok=%v, ContentSha256=%s\n", ok, fs.ContentSha256)
	} else {
		fmt.Printf("FAIL: VerifyLock should return false for tampered spec: ok=%v\n", ok)
	}
	
	fmt.Println("\n=== Testing AllRequiredSlotsFilled edge case (exactly 3 criteria) ===")
	fs = validSpec()
	fs.Goal.SuccessCriteriaNatural = []string{"a", "b", "c"}
	if spec.AllRequiredSlotsFilled(fs) {
		fmt.Println("PASS: Exactly 3 criteria passed")
	} else {
		fmt.Println("FAIL: Exactly 3 criteria should pass")
	}
	
	fs.Goal.SuccessCriteriaNatural = []string{"a", "b"}
	if !spec.AllRequiredSlotsFilled(fs) {
		fmt.Println("PASS: 2 criteria correctly failed")
	} else {
		fmt.Println("FAIL: 2 criteria should fail")
	}
	
	fmt.Println("\n=== Testing AllRequiredSlotsFilled with nil spec ===")
	if !spec.AllRequiredSlotsFilled(nil) {
		fmt.Println("PASS: nil spec correctly returned false")
	} else {
		fmt.Println("FAIL: nil spec should return false")
	}
	
	fmt.Println("\n=== Testing VerifyLock with nil spec ===")
	_, err = spec.VerifyLock(nil)
	if err != nil {
		fmt.Printf("ERROR returned: %v (Good - error handling exists)\n", err)
	} else {
		fmt.Println("No error returned - VerifyLock might crash on nil input")
	}
}
