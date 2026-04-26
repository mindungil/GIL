// Package cliutil contains small helpers shared by gil's CLI binaries.
//
// The error helpers in this file solve a single problem: error messages that
// reach the terminal should speak the user's vocabulary and tell the user how
// to recover. Internal phrases ("socket did not appear", "ANTHROPIC_API_KEY
// not set", "session must be frozen before run") leak implementation details
// and leave the user guessing.
//
// UserError is a structured error with two presentational fields — Msg (what
// went wrong, in user vocabulary) and Hint (a one-line suggestion for how to
// fix it). The CLI's top-level handler uses Exit to print both lines, while
// errors.Is / errors.As keep working through Unwrap so wrapping does not break
// the chain.
//
// Reference: opencode's cli/error.ts shows the same shape — a typed error
// layer that the CLI surface formats, while the underlying transport (gRPC
// status, in our case) is preserved as the wrapped cause.
package cliutil

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// UserError is an error meant to be presented directly to the human user.
//
// Msg is the user-facing description of what went wrong. It MUST NOT contain
// internal vocabulary (file paths, internal states, env-var names) unless that
// vocabulary is meaningful to the user.
//
// Hint, when non-empty, is a single-line suggestion of how to recover. It
// should be ≤80 characters and ideally contain a runnable command (e.g.
// "run 'gil init'", "set ANTHROPIC_API_KEY"). Errors with no clear
// remediation should leave Hint empty rather than padding with vague advice.
//
// Code is the process exit code Exit will use; zero is normalised to 1 so
// callers can leave it unset for the common case.
//
// Cause is the underlying error and is exposed via Unwrap so errors.Is and
// errors.As continue to work through the user-facing layer.
type UserError struct {
	Msg   string
	Hint  string
	Code  int
	Cause error
}

// Error returns the user-facing message. It deliberately does NOT include the
// hint — Error() is consumed by other errors.Wrap chains and log sinks where
// the hint would be noise. Use Print for the formatted two-line presentation.
func (e *UserError) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

// Unwrap exposes the underlying cause so errors.Is / errors.As traverse the
// chain. Returning nil when Cause is nil is the standard idiom.
func (e *UserError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Print writes the user-facing presentation of the error to w:
//
//	Error: <msg>
//	Hint: <hint>
//
// Hint is only written when non-empty. Trailing newlines are included so
// callers can pass os.Stderr directly without extra formatting.
func (e *UserError) Print(w io.Writer) {
	if e == nil {
		return
	}
	fmt.Fprintf(w, "Error: %s\n", e.Msg)
	if e.Hint != "" {
		fmt.Fprintf(w, "Hint: %s\n", e.Hint)
	}
}

// Wrap returns a UserError that wraps cause, attaching a user-facing message
// and (optional) hint. cause may be nil for synthesised errors that did not
// originate from a downstream call.
//
// The returned error always has Code=1; callers wanting a different exit code
// should construct UserError directly.
func Wrap(cause error, msg, hint string) *UserError {
	return &UserError{Msg: msg, Hint: hint, Code: 1, Cause: cause}
}

// New returns a UserError with no underlying cause. Convenience wrapper for
// errors that originate inside the CLI itself (flag validation, etc).
func New(msg, hint string) *UserError {
	return &UserError{Msg: msg, Hint: hint, Code: 1}
}

// Exit prints err to stderr and terminates the process. nil is a no-op so
// callers can pass through whatever Cobra returns from Execute().
//
// For *UserError, Exit prints "Error: ..." followed by "Hint: ..." (when set)
// and exits with the embedded code (default 1). For any other error type,
// Exit prints "Error: <msg>" with no hint and exits 1 — this preserves
// existing behaviour for errors that have not yet been converted.
func Exit(err error) {
	if err == nil {
		return
	}
	exitFn := os.Exit
	stderr := io.Writer(os.Stderr)
	exit(stderr, exitFn, err)
}

// exit is the test seam for Exit: it takes the writer and exit function as
// parameters so tests can verify behaviour without actually killing the
// process. Production callers go through Exit which wires up os.Stderr +
// os.Exit.
func exit(w io.Writer, exitFn func(int), err error) {
	var ue *UserError
	if errors.As(err, &ue) && ue != nil {
		ue.Print(w)
		code := ue.Code
		if code == 0 {
			code = 1
		}
		exitFn(code)
		return
	}
	fmt.Fprintf(w, "Error: %s\n", err.Error())
	exitFn(1)
}
