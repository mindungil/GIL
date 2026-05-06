package errmap_test

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mindungil/gil/cli/internal/errmap"
	"github.com/mindungil/gil/core/cliutil"
)

func TestWrapRPCError_NilPassesThrough(t *testing.T) {
	if got := errmap.WrapRPCError(nil); got != nil {
		t.Fatalf("nil input should map to nil, got %v", got)
	}
}

func TestWrapRPCError_NonStatusErrorPassesThrough(t *testing.T) {
	plain := errors.New("not a grpc status")
	got := errmap.WrapRPCError(plain)
	if got != plain {
		t.Fatalf("non-status error should pass through unchanged, got %v", got)
	}
}

func TestWrapRPCError_KnownMessageReturnsUserError(t *testing.T) {
	src := status.Error(codes.FailedPrecondition, "no credentials for anthropic")
	wrapped := errmap.WrapRPCError(src)
	var ue *cliutil.UserError
	if !errors.As(wrapped, &ue) {
		t.Fatalf("expected UserError, got %T (%v)", wrapped, wrapped)
	}
	if ue.Hint == "" {
		t.Fatalf("hint should be non-empty for known message")
	}
	if !errors.Is(wrapped, src) {
		t.Fatalf("UserError should preserve underlying gRPC error in chain")
	}
}

func TestWrapRPCError_UnknownStatusPassesThrough(t *testing.T) {
	src := status.Error(codes.Internal, "some unknown server message we never matched")
	got := errmap.WrapRPCError(src)
	// Unknown-but-status errors return original error unchanged so the
	// gRPC chain (and code) survives for callers that do errors.As(...).
	if got.Error() != src.Error() {
		t.Fatalf("unknown status should pass through, got %q want %q", got.Error(), src.Error())
	}
}

func TestFormatForChat_UserErrorRendersMsgAndHint(t *testing.T) {
	src := status.Error(codes.FailedPrecondition, "no credentials for openai")
	wrapped := errmap.WrapRPCError(src)
	got := errmap.FormatForChat(wrapped)
	if !strings.Contains(got, "no credentials for openai") {
		t.Fatalf("should include user message, got %q", got)
	}
	if !strings.Contains(got, "OPENAI_API_KEY") {
		t.Fatalf("should include hint, got %q", got)
	}
	if !strings.Contains(got, " — ") {
		t.Fatalf("should join msg/hint with em-dash, got %q", got)
	}
}

func TestFormatForChat_PlainErrorJustMessage(t *testing.T) {
	plain := errors.New("plain error")
	if got := errmap.FormatForChat(plain); got != "plain error" {
		t.Fatalf("plain error should pass through, got %q", got)
	}
}

func TestFormatForChat_NilEmpty(t *testing.T) {
	if got := errmap.FormatForChat(nil); got != "" {
		t.Fatalf("nil should map to empty string, got %q", got)
	}
}
