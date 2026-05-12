package cmd

import "github.com/mindungil/gil/cli/internal/errmap"

// wrapRPCError forwards to errmap.WrapRPCError. The dispatch table now
// lives in cli/internal/errmap so the chat REPL surface can share it
// (see followup #18). Keeping this thin alias here means existing
// cobra-command call sites stay readable without sprawling import
// churn — they keep calling wrapRPCError(err).
var wrapRPCError = errmap.WrapRPCError
