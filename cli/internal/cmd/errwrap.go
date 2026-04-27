package cmd

import (
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mindungil/gil/core/cliutil"
)

// wrapRPCError converts a gRPC error returned from gild into a *cliutil.UserError
// with a remediation Hint when the message matches a known case. For everything
// else, it returns the original error unchanged so existing callers and the
// gRPC error chain remain intact.
//
// We dispatch on the gRPC status message (and sometimes its code) rather than
// constructing typed errors at the server, because:
//   - the server-side code stays a thin gRPC layer (no CLI vocabulary leaking
//     into it),
//   - the CLI is where presentation lives, and
//   - the message strings are stable contract: the server commits to them
//     in the same way it commits to the proto.
//
// Add a new branch here when you adopt a new user-facing server message.
func wrapRPCError(err error) error {
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

	// Unknown provider name passed to --provider.
	case strings.Contains(msg, "unknown provider"):
		return cliutil.Wrap(err,
			extractAfter(msg, "provider: ") /* keep server's quoted name */,
			`pick one of: anthropic, mock — or run "gil auth login <provider>"`)

	// Spec must be frozen before "gil run". Keep the server's full sentence
	// (including "current status: X") rather than slicing it — the user
	// benefits from knowing which state blocked the run.
	case strings.Contains(msg, "must be frozen before run"):
		// Drop only the gRPC-added "rpc error: code = ... desc = " prefix if
		// present. status.Message() already does this for us, so msg here is
		// the raw server sentence: "session \"X\" must be frozen before run
		// (current status: created)". Use it verbatim.
		return cliutil.Wrap(err, msg,
			`run "gil interview <id>" to finish, then "gil spec freeze <id>"`)

	// No active interview — usually means daemon was restarted mid-flow.
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
			extractAfter(msg, "backend: ") /* keep server's specific reason */,
			`install the listed dependency, or change spec.workspace.backend`)
	}

	return err
}

// extractAfter returns msg[idx+len(sep):] when sep occurs in msg, otherwise msg.
// Used to strip wrapper prefixes that the gRPC status code injected (e.g.
// "session ...: my real message") so the user sees just the core sentence.
func extractAfter(msg, sep string) string {
	if i := strings.Index(msg, sep); i >= 0 {
		s := msg[i+len(sep):]
		if s != "" {
			return s
		}
	}
	return msg
}
