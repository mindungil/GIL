// Package errmap converts gRPC errors returned from gild into the
// CLI-facing UserError shape (cliutil.UserError). It is shared by the
// cobra command surface (cli/internal/cmd) and the chat REPL surface
// (cli/internal/chat/repl) so both speak the same vocabulary.
//
// The dispatch table lives here, in one place, because:
//   - the server stays a thin gRPC layer (no CLI vocabulary leaks),
//   - presentation is the CLI's job, and
//   - the message strings are stable contract: the server commits to
//     them in the same way it commits to the proto.
//
// Add a branch to WrapRPCError when you adopt a new user-facing server
// message. Unmatched errors pass through unchanged so the gRPC chain
// stays intact for any caller that relies on errors.Is / errors.As.
package errmap

import (
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mindungil/gil/core/cliutil"
)

// WrapRPCError converts a gRPC error into a *cliutil.UserError with a
// remediation Hint when the message matches a known case. Returns nil
// for nil, the original error for non-status errors, and the original
// error unchanged for unmatched server messages.
func WrapRPCError(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	msg := st.Message()

	switch {
	// Provider credentials missing (gild factory).
	case strings.Contains(msg, "no credentials for anthropic"):
		return cliutil.Wrap(err,
			"no credentials for anthropic",
			`set ANTHROPIC_API_KEY, or run "gil auth login anthropic"`)
	case strings.Contains(msg, "no credentials for openai"):
		return cliutil.Wrap(err,
			"no credentials for openai",
			`set OPENAI_API_KEY, or run "gil auth login openai"`)
	case strings.Contains(msg, "no credentials for openrouter"):
		return cliutil.Wrap(err,
			"no credentials for openrouter",
			`set OPENROUTER_API_KEY, or run "gil auth login openrouter"`)
	case strings.Contains(msg, "no credentials for vllm: base URL required"):
		return cliutil.Wrap(err,
			"vllm requires a base URL",
			`run "gil auth login vllm --base-url http://host:port/v1"`)
	case strings.Contains(msg, "no credentials for vllm"):
		return cliutil.Wrap(err,
			"no credentials for vllm",
			`run "gil auth login vllm --base-url http://host:port/v1"`)

	// Unknown provider name passed to --provider.
	case strings.Contains(msg, "unknown provider"):
		return cliutil.Wrap(err,
			extractAfter(msg, "provider: "),
			`pick one of: anthropic, openai, openrouter, vllm, mock — or run "gil auth login <provider>"`)

	// Spec must be frozen before "gil run". Server's full sentence
	// (including "current status: X") is informative — keep verbatim.
	case strings.Contains(msg, "must be frozen before run"):
		return cliutil.Wrap(err, msg,
			`run "gil interview <id>" to finish, then "gil spec freeze <id>"`)

	// No active interview — daemon was usually restarted mid-flow.
	case strings.Contains(msg, "no active interview for session"):
		return cliutil.Wrap(err,
			"no active interview for this session",
			`start a new interview with "gil interview <id>"`)

	// Session not in interview state but resume was requested.
	case strings.Contains(msg, "interviewing status but no in-memory state"):
		return cliutil.Wrap(err,
			"the interview was lost when the daemon restarted",
			`start over with "gil interview <id>"`)

	// Required slots not filled at confirm/freeze time.
	case strings.Contains(msg, "spec missing required slots"):
		return cliutil.Wrap(err,
			"spec is missing required answers",
			`return to the interview with "gil interview <id>" and finish all questions`)

	// Restore against running session.
	case strings.Contains(msg, "cannot restore session") && strings.Contains(msg, "while running"):
		return cliutil.Wrap(err,
			"cannot restore a session that is currently running",
			`wait for the run to finish, then retry "gil restore"`)

	// Restore but no checkpoints exist.
	case strings.Contains(msg, "has no checkpoints"):
		return cliutil.Wrap(err,
			"no checkpoints to restore from",
			`run the agent at least once with "gil run <id>" to create checkpoints`)

	// Tail before the run started.
	case strings.Contains(msg, "no active run for session"):
		return cliutil.Wrap(err,
			"no active run for this session",
			`start one with "gil run <id>", then "gil events <id> --tail"`)

	// Workspace backend not available on this host.
	case strings.Contains(msg, "workspace backend") && (st.Code() == codes.FailedPrecondition || strings.Contains(msg, "requires")):
		return cliutil.Wrap(err,
			extractAfter(msg, "backend: "),
			`install the listed dependency, or change spec.workspace.backend`)
	}

	return err
}

// FormatForChat renders an error for one-line chat display. UserError
// is shown as "msg — hint" so the recovery suggestion stays visible
// even though chat surfaces don't have the two-line cobra layout.
// Falls back to err.Error() when no hint or not a UserError.
func FormatForChat(err error) string {
	if err == nil {
		return ""
	}
	if ue, ok := err.(*cliutil.UserError); ok && ue != nil {
		if ue.Hint != "" {
			return ue.Msg + " — " + ue.Hint
		}
		return ue.Msg
	}
	return err.Error()
}

// extractAfter returns msg[idx+len(sep):] when sep occurs in msg,
// otherwise msg. Used to strip wrapper prefixes the gRPC status
// injected so the user sees just the core sentence.
func extractAfter(msg, sep string) string {
	if i := strings.Index(msg, sep); i >= 0 {
		s := msg[i+len(sep):]
		if s != "" {
			return s
		}
	}
	return msg
}
