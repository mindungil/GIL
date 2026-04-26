// Package slash parses leading-slash command lines (e.g. "/help", "/model gpt-4o")
// into a structured Command and dispatches them through a Registry of Handlers.
//
// Two key design properties:
//
//  1. Slash commands are a SURFACE for observation and queued hints, never a
//     mid-tool-call interrupt mechanism. Handlers run on the next turn boundary
//     of the agent loop; nothing in this package issues CancelRun or otherwise
//     terminates an in-flight tool call.
//
//  2. Parsing is intentionally tiny and dependency-free so it can be reused
//     by the TUI (Bubbletea) and the headless `gil run --interactive` mode
//     without dragging in either's input stack.
//
// Reference lifts:
//   - codex-rs/tui/src/slash_command.rs — enum + per-command capability flags
//     (we replace the enum with a Registry of dynamically-registered Specs so
//     the TUI and CLI can register the same set without macro tricks).
//   - cline/src/shared/slashCommands.ts — flat list of {name, description}
//     records; we adopt the same shape for `/help` rendering.
package slash

import (
	"context"
	"strings"
	"unicode"
)

// Command is a parsed slash invocation. Name has no leading slash.
type Command struct {
	Name string   // canonical-ish name as typed (lowercased by Lookup)
	Args []string // shell-split args after the name
	Raw  string   // the full original line including the leading slash
}

// Handler executes a command and returns text to display + optional error.
// The context carries cancellation; handlers MUST honour it (gRPC calls
// inherit it directly).
type Handler func(ctx context.Context, cmd Command) (output string, err error)

// Spec describes a registered command.
type Spec struct {
	Name      string   // canonical name (no leading slash, no spaces)
	Aliases   []string // alternate names that resolve to the same Handler
	Summary   string   // one-line description for `/help`
	Handler   Handler
	NoSession bool // true if the command does not require an active session
}

// ParseLine inspects a raw input line and returns (Command, true) when the
// line begins with `/` followed by a non-whitespace character.
//
// Whitespace-only lines, lines without a leading `/`, and a bare `/` all
// return (zero, false). The leading slash MUST be the first non-whitespace
// rune — embedded slashes ("hello /help") do NOT match, matching how every
// reference harness scopes the trigger.
//
// Args are split on ASCII whitespace (no shell quoting). This mirrors the
// lightweight parsing in cline/src/services/slash-commands so users don't
// hit surprising quote semantics inside an autonomous-agent surface.
func ParseLine(line string) (Command, bool) {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	if !strings.HasPrefix(trimmed, "/") {
		return Command{}, false
	}
	body := strings.TrimPrefix(trimmed, "/")
	if body == "" {
		return Command{}, false
	}
	// First whitespace-delimited token is the name.
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return Command{}, false
	}
	name := fields[0]
	if name == "" {
		return Command{}, false
	}
	var args []string
	if len(fields) > 1 {
		args = append(args, fields[1:]...)
	}
	return Command{
		Name: name,
		Args: args,
		Raw:  trimmed,
	}, true
}
