package app

import (
	"github.com/jedutools/gil/core/paths"
)

// DefaultSocket returns the default UDS path used by gild, derived from
// the XDG layout (or the GIL_HOME single-tree override when set). Falls
// back to /tmp/gil/gild.sock only when HOME cannot be resolved at all
// — same rationale as the gilmcp / gild fallback.
func DefaultSocket() string {
	l, err := paths.FromEnv()
	if err != nil {
		return "/tmp/gil/gild.sock"
	}
	return l.Sock()
}
