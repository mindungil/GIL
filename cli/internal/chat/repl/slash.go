// Package repl is the chat surface REPL — single prompt loop, slash
// commands, and an EventStream consumer that drives the Renderer.
package repl

import "strings"

type InputKind int

const (
	InputBlank InputKind = iota
	InputPrompt
	InputSlash
)

// ParseInput classifies a single line of user input.
//
// Bare text → InputPrompt with args=line.
// "/cmd args..." → InputSlash with cmd=cmd, args=joined remainder.
// Whitespace-only → InputBlank.
func ParseInput(line string) (kind InputKind, cmd string, args string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return InputBlank, "", ""
	}
	if !strings.HasPrefix(trimmed, "/") {
		return InputPrompt, "", trimmed
	}
	rest := strings.TrimPrefix(trimmed, "/")
	parts := strings.SplitN(rest, " ", 2)
	cmd = parts[0]
	if len(parts) == 2 {
		args = strings.TrimSpace(parts[1])
	}
	return InputSlash, cmd, args
}

// V1 slash command set. Update when adding V1.1 (interrupt/stop).
var v1Slash = map[string]bool{
	"sessions": true, "switch": true, "new": true, "quit": true,
	"spec": true, "status": true, "diff": true, "merge": true,
	"run": true, "help": true,
}

func IsKnownSlash(name string) bool { return v1Slash[name] }

// SlashRequiresSession reports whether a known slash command needs an
// active session in client state.
func SlashRequiresSession(name string) bool {
	switch name {
	case "spec", "status", "diff", "merge", "run":
		return true
	}
	return false
}
