// Package version exposes build-time identifying information for every
// gil binary (gil, gild, giltui, gilmcp).
//
// Three string vars (Version, Commit, BuildDate) are stamped at link time
// via -ldflags; see Makefile and .goreleaser.yaml for the canonical
// invocations:
//
//	-ldflags "-X 'github.com/mindungil/gil/core/version.Version=v0.1.0-alpha' \
//	          -X 'github.com/mindungil/gil/core/version.Commit=<sha>'        \
//	          -X 'github.com/mindungil/gil/core/version.BuildDate=<rfc3339>'"
//
// When the binary is built without ldflags (e.g. `go run`, `go test`,
// `go build` without the Makefile target), the constants stay at their
// "0.0.0-dev" / "unknown" defaults. To make `gil --version` still useful
// in that case we consult runtime/debug.ReadBuildInfo and prefer the
// module version + VCS info it surfaces (Go 1.18+ stamps these into any
// `go install`-built binary automatically). The fallback is purely
// additive — a release build (where the ldflags are present) is never
// affected.
//
// Why a dedicated package instead of stamping into each main: the four
// gil binaries live in four different modules (cli, server, tui, mcp),
// so a single -X target reaches all of them only if it's a shared
// import. core is already that shared import for everything else.
package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Build-time injection points. The exported names match the convention
// in Makefile / .goreleaser.yaml. All three are package-level vars (not
// consts) because Go's linker can only -X-set vars.
//
// We initialise them to non-empty sentinels so a binary built without
// ldflags still produces a sensible string; tests rely on that.
var (
	// Version is the semantic version string (with leading "v" when set
	// from a git tag, e.g. "v0.1.0-alpha"). Default "0.0.0-dev" — the
	// unambiguous "I have no idea" fallback.
	Version = "0.0.0-dev"

	// Commit is the short git SHA the build was produced from.
	Commit = "unknown"

	// BuildDate is the UTC RFC3339 timestamp the binary was linked.
	BuildDate = "unknown"
)

// String returns the canonical version line:
//
//	"<version> (<commit>, <date>)"
//
// Examples:
//
//	"v0.1.0-alpha (a1b2c3d, 2026-04-27T12:34:56Z)"
//	"v0.2.0 (devel)"  -- when Commit/BuildDate fall back to BuildInfo
//	"0.0.0-dev"       -- bare dev build with no BuildInfo either
//
// Cobra's RootCommand.Version field is set to this value so `gil
// --version` prints it as-is.
func String() string {
	v, c, d := resolved()
	if c == "unknown" && d == "unknown" {
		return v
	}
	if c == "unknown" {
		return fmt.Sprintf("%s (%s)", v, d)
	}
	if d == "unknown" {
		return fmt.Sprintf("%s (%s)", v, c)
	}
	return fmt.Sprintf("%s (%s, %s)", v, c, d)
}

// Short returns just the version component ("vX.Y.Z" or "0.0.0-dev"),
// suitable for places that want a compact identifier (e.g. the
// gil_build_info Prometheus gauge).
func Short() string {
	v, _, _ := resolved()
	return v
}

// Commit_ returns the short git SHA, falling back to BuildInfo's vcs.revision
// (truncated to 12 chars) when -ldflags wasn't used. Returns "unknown" when
// no information is available.
func Commit_() string {
	_, c, _ := resolved()
	return c
}

// BuildDate_ returns the build timestamp, falling back to BuildInfo's
// vcs.time when -ldflags wasn't used. Returns "unknown" when no
// information is available.
func BuildDate_() string {
	_, _, d := resolved()
	return d
}

// resolved returns the (version, commit, date) triple after applying the
// runtime/debug fallback. It is the single point where ldflags-set
// values are reconciled with BuildInfo-derived values, so callers never
// have to repeat the precedence rules.
//
// Precedence per field:
//  1. -ldflags-set value (i.e. != the default sentinel).
//  2. runtime/debug.BuildInfo equivalent (Main.Version for Version,
//     vcs.revision for Commit, vcs.time for BuildDate).
//  3. The default sentinel ("0.0.0-dev" / "unknown").
//
// We only call debug.ReadBuildInfo when at least one field is at its
// sentinel — this avoids the (cheap but non-zero) cost in the common
// release-build case where everything is already filled in.
func resolved() (version, commit, date string) {
	version, commit, date = Version, Commit, BuildDate

	if version != "0.0.0-dev" && commit != "unknown" && date != "unknown" {
		return
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	if version == "0.0.0-dev" {
		// BuildInfo.Main.Version is "(devel)" for `go build` from a
		// working tree, or the module version (e.g. "v0.1.0") for a
		// `go install module@version` build. Prefer the module
		// version, but fall back to "(devel)" rather than masking it,
		// so users debugging a `go build`-produced binary see "(devel)"
		// rather than "0.0.0-dev" (the latter implies dev-mode but
		// hides that the user did, in fact, build it themselves).
		if mv := bi.Main.Version; mv != "" {
			version = mv
		}
	}

	if commit == "unknown" || date == "unknown" {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if commit == "unknown" && s.Value != "" {
					commit = shortCommit(s.Value)
				}
			case "vcs.time":
				if date == "unknown" && s.Value != "" {
					date = s.Value
				}
			}
		}
	}

	return
}

// shortCommit truncates a git SHA to 12 characters, the same length git
// log --oneline shows. We don't use 7 because BuildInfo's vcs.revision
// is the full 40-char SHA and 7 has been shown to collide on large
// repositories.
func shortCommit(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
