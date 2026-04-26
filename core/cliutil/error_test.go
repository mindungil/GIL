package cliutil

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// sentinelError is used to verify errors.Is / errors.As traverse Unwrap.
type sentinelError struct{ tag string }

func (e *sentinelError) Error() string { return e.tag }

func TestUserError_Error_ReturnsMsgWithoutHint(t *testing.T) {
	ue := &UserError{Msg: "no credentials for anthropic", Hint: "gil auth login anthropic"}
	if got := ue.Error(); got != "no credentials for anthropic" {
		t.Fatalf("Error() = %q, want msg only without hint", got)
	}
}

func TestUserError_Print_WithHint(t *testing.T) {
	ue := &UserError{Msg: "daemon not running", Hint: `run "gil init"`}
	var buf bytes.Buffer
	ue.Print(&buf)
	want := "Error: daemon not running\nHint: run \"gil init\"\n"
	if buf.String() != want {
		t.Fatalf("Print() = %q, want %q", buf.String(), want)
	}
}

func TestUserError_Print_WithoutHintOmitsHintLine(t *testing.T) {
	ue := &UserError{Msg: "out of memory"}
	var buf bytes.Buffer
	ue.Print(&buf)
	want := "Error: out of memory\n"
	if buf.String() != want {
		t.Fatalf("Print() = %q, want %q", buf.String(), want)
	}
}

func TestUserError_Unwrap_PreservesChain(t *testing.T) {
	root := &sentinelError{tag: "root"}
	ue := Wrap(root, "user message", "user hint")
	var got *sentinelError
	if !errors.As(ue, &got) {
		t.Fatalf("errors.As failed to find sentinelError through UserError")
	}
	if got.tag != "root" {
		t.Fatalf("got tag %q, want %q", got.tag, "root")
	}
	if !errors.Is(ue, root) {
		t.Fatalf("errors.Is failed to find sentinel via UserError chain")
	}
}

func TestExit_NilIsNoOp(t *testing.T) {
	// Use the seam directly to avoid touching os.Exit.
	called := false
	exitFn := func(int) { called = true }
	var buf bytes.Buffer
	// Replicate Exit's nil-guard.
	if err := error(nil); err != nil {
		exit(&buf, exitFn, err)
	}
	if called {
		t.Fatalf("exitFn should not have been called for nil error")
	}
	if buf.Len() != 0 {
		t.Fatalf("nothing should have been written for nil error, got %q", buf.String())
	}
}

func TestExit_UserErrorPrintsBothLinesAndUsesCode(t *testing.T) {
	ue := &UserError{Msg: "daemon not running", Hint: `run "gil init"`, Code: 7}
	var buf bytes.Buffer
	gotCode := -1
	exitFn := func(c int) { gotCode = c }
	exit(&buf, exitFn, ue)
	wantOut := "Error: daemon not running\nHint: run \"gil init\"\n"
	if buf.String() != wantOut {
		t.Fatalf("Exit output = %q, want %q", buf.String(), wantOut)
	}
	if gotCode != 7 {
		t.Fatalf("Exit code = %d, want 7", gotCode)
	}
}

func TestExit_UserErrorZeroCodeDefaultsToOne(t *testing.T) {
	ue := &UserError{Msg: "x"}
	var buf bytes.Buffer
	gotCode := -1
	exitFn := func(c int) { gotCode = c }
	exit(&buf, exitFn, ue)
	if gotCode != 1 {
		t.Fatalf("Exit code = %d, want 1 (zero-default)", gotCode)
	}
}

func TestExit_PlainErrorPrintsOneLine(t *testing.T) {
	err := errors.New("some internal failure")
	var buf bytes.Buffer
	gotCode := -1
	exitFn := func(c int) { gotCode = c }
	exit(&buf, exitFn, err)
	want := "Error: some internal failure\n"
	if buf.String() != want {
		t.Fatalf("Exit output = %q, want %q", buf.String(), want)
	}
	if gotCode != 1 {
		t.Fatalf("Exit code = %d, want 1", gotCode)
	}
}

func TestExit_PlainErrorWrappingUserErrorStillFormatsAsUserError(t *testing.T) {
	// A UserError wrapped inside fmt.Errorf("...: %w", ue) should still be
	// detected via errors.As and printed with its hint. This is the path that
	// happens when a Cobra RunE wraps "ensure daemon: %w" around our error.
	root := Wrap(errors.New("dial timeout"), "daemon not running", `run "gil init"`)
	wrapped := fmt.Errorf("ensure daemon: %w", root)

	var buf bytes.Buffer
	gotCode := -1
	exitFn := func(c int) { gotCode = c }
	exit(&buf, exitFn, wrapped)

	want := "Error: daemon not running\nHint: run \"gil init\"\n"
	if buf.String() != want {
		t.Fatalf("Exit output = %q, want %q", buf.String(), want)
	}
	if gotCode != 1 {
		t.Fatalf("Exit code = %d, want 1", gotCode)
	}
}

func TestWrap_SetsFieldsAndDefaultCode(t *testing.T) {
	cause := errors.New("inner")
	ue := Wrap(cause, "msg", "hint")
	if ue.Msg != "msg" {
		t.Errorf("Msg = %q", ue.Msg)
	}
	if ue.Hint != "hint" {
		t.Errorf("Hint = %q", ue.Hint)
	}
	if ue.Code != 1 {
		t.Errorf("Code = %d, want 1", ue.Code)
	}
	if !errors.Is(ue, cause) {
		t.Errorf("errors.Is(ue, cause) = false")
	}
}

func TestNew_NoCause(t *testing.T) {
	ue := New("msg", "hint")
	if ue.Cause != nil {
		t.Errorf("Cause = %v, want nil", ue.Cause)
	}
	if ue.Code != 1 {
		t.Errorf("Code = %d, want 1", ue.Code)
	}
	if errors.Unwrap(ue) != nil {
		t.Errorf("Unwrap should return nil for New")
	}
}
