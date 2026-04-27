package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/credstore"
	"github.com/mindungil/gil/core/paths"
)

// Status discriminates the four outcomes of a single check. INFO is the
// "this is fine, here's some context" tier — it is reported but does not
// affect the exit code. WARN signals a potential issue with a remediation
// hint; FAIL is a hard error that flips the process exit code to 1.
//
// We deliberately keep the set small (4 levels) so the output stays
// scannable. goose's doctor uses the same OK/WARN/FAIL trichotomy plus
// a separate "Hint" line, which matches what we surface here.
type Status string

const (
	StatusOK   Status = "OK"
	StatusInfo Status = "INFO"
	StatusWarn Status = "WARN"
	StatusFail Status = "FAIL"
)

// Check is the unit of doctor output. Group bundles related checks
// (Layout, Daemon, Credentials, Sandboxes, Tools) under a single header
// so the human-readable output is easy to skim. Hint is only set when
// Status != OK, and is intended to be a single line ≤80 chars with a
// runnable command when possible.
type Check struct {
	Group   string `json:"group"`
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// doctorCmd returns the `gil doctor` subcommand.
//
// doctor is the read-only counterpart of `gil init`: it inspects the
// installation and prints what's working / what's not, with remediation
// hints. It MUST work without the daemon running (we never call
// ensureDaemon — the whole point is diagnosing setup, including a dead
// daemon).
//
// Reference lift:
//   - goose's commands/info.rs — the four-section layout (Version,
//     Paths, Provider, Configuration), aligned label/value rendering,
//     and "missing (can create)" / "missing (read-only parent)"
//     phrasing.
//   - The same crate's check_path_status helper informs how we phrase
//     directory-existence WARNs.
func doctorCmd() *cobra.Command {
	var (
		legacyJSON bool
		verbose    bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "diagnose gil installation + environment",
		Long: `Inspect gil's installation and environment.

doctor is read-only: it never modifies anything on disk. It walks through
the directory layout, daemon presence, credential store, sandbox helpers,
and external tools, surfacing OK / INFO / WARN / FAIL for each.

Exit code is 0 when no FAIL is reported (WARNs do not fail the command),
1 otherwise — so CI/install scripts can use "gil doctor" as a pre-flight
verifier.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			layout, layoutErr := paths.FromEnv()
			checks := runDoctorChecks(ctx, layout, layoutErr)

			out := cmd.OutOrStdout()
			// Legacy --json wins for back-compat; otherwise the persistent
			// --output flag drives format selection.
			if legacyJSON || outputJSON() {
				if err := renderDoctorJSON(out, checks); err != nil {
					return err
				}
			} else {
				renderDoctorText(out, checks, verbose)
			}

			// Exit code: any FAIL flips us to 1, WARN/INFO do not. We
			// route through doctorExitFn (a package-level seam) so
			// tests can swap it for a recorder; production keeps the
			// default os.Exit so cliutil.Exit's "Error: ..." line never
			// shows up after the human-readable report — the report
			// itself already explained what's wrong.
			for _, c := range checks {
				if c.Status == StatusFail {
					doctorExitFn(1)
					return nil
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&legacyJSON, "json", false, "emit JSON instead of human-readable output (alias for --output json)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "include extra environmental detail")
	return cmd
}

// runDoctorChecks executes every check in fixed order and returns the
// flat list. Order matters for the rendered output: layout precedes
// daemon precedes credentials, so a user with broken paths sees the
// upstream cause first.
//
// layoutErr is non-nil only when paths.FromEnv() itself failed (no HOME,
// for example). In that pathological case we still emit a partial
// report — the user gets one FAIL describing the layout failure, plus
// whatever environment checks don't depend on the layout.
func runDoctorChecks(ctx context.Context, layout paths.Layout, layoutErr error) []Check {
	var out []Check

	// Layout group.
	if layoutErr != nil {
		out = append(out, Check{
			Group:   "Layout",
			Name:    "resolve",
			Status:  StatusFail,
			Message: layoutErr.Error(),
			Hint:    "set GIL_HOME=<dir> to override, or check that HOME is set",
		})
	} else {
		out = append(out, checkDir("Layout", "config", layout.Config, true)...)
		out = append(out, checkDir("Layout", "data", layout.Data, false)...)
		out = append(out, checkDir("Layout", "state", layout.State, true)...)
		out = append(out, checkDir("Layout", "cache", layout.Cache, false)...)
		out = append(out, checkLegacyTilde(layout))
	}

	// Daemon group.
	out = append(out, checkGildBinary())
	if layoutErr == nil {
		out = append(out, checkDaemonRunning(layout.Sock()))
	}

	// Credentials group.
	if layoutErr == nil {
		out = append(out, checkCredentials(ctx, layout.AuthFile())...)
	}
	out = append(out, checkEnvFallbacks()...)

	// Sandboxes group.
	out = append(out, checkSandboxes()...)

	// Tools group.
	out = append(out, checkTools()...)

	return out
}

// checkDir returns one Check per directory: existence + (when required)
// writability. The two checks live in one function because they share
// the same error-formatting and remediation hint.
//
// requireWritable trips a writability test by attempting to create + remove
// a sentinel file. We don't trust os.Stat permission bits alone because
// they ignore filesystem-level mounts (read-only bind mounts, full disks,
// etc.) that a write-then-unlink probe does catch.
func checkDir(group, name, path string, requireWritable bool) []Check {
	out := []Check{}
	info, err := os.Stat(path)
	if err != nil {
		out = append(out, Check{
			Group:   group,
			Name:    name,
			Status:  StatusWarn,
			Message: fmt.Sprintf("%s missing", path),
			Hint:    `run "gil init" to create it`,
		})
		return out
	}
	if !info.IsDir() {
		out = append(out, Check{
			Group:   group,
			Name:    name,
			Status:  StatusFail,
			Message: fmt.Sprintf("%s exists but is not a directory", path),
			Hint:    "remove or rename it, then run \"gil init\"",
		})
		return out
	}
	if requireWritable {
		if err := probeWritable(path); err != nil {
			out = append(out, Check{
				Group:   group,
				Name:    name,
				Status:  StatusWarn,
				Message: fmt.Sprintf("%s exists but is not writable", path),
				Hint:    "check filesystem permissions / read-only mounts",
			})
			return out
		}
	}
	out = append(out, Check{
		Group:   group,
		Name:    name,
		Status:  StatusOK,
		Message: path,
	})
	return out
}

// probeWritable creates and removes a uniquely-named sentinel under dir.
// Returns nil when both succeed, the underlying error otherwise. The
// sentinel name embeds the PID so concurrent doctor runs don't collide.
func probeWritable(dir string) error {
	name := filepath.Join(dir, fmt.Sprintf(".doctor-%d-%d", os.Getpid(), time.Now().UnixNano()))
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_ = f.Close()
	return os.Remove(name)
}

// checkLegacyTilde returns INFO when ~/.gil/ has already been migrated
// (stamp present), WARN when it exists without the stamp (migration
// pending), or OK when there's no legacy tree at all.
func checkLegacyTilde(layout paths.Layout) Check {
	home, err := os.UserHomeDir()
	if err != nil {
		return Check{Group: "Layout", Name: "legacy ~/.gil", Status: StatusInfo,
			Message: "could not resolve $HOME", Hint: ""}
	}
	legacy := filepath.Join(home, ".gil")
	if _, err := os.Stat(legacy); err != nil {
		if os.IsNotExist(err) {
			return Check{Group: "Layout", Name: "legacy ~/.gil", Status: StatusOK,
				Message: "absent (XDG-only install)"}
		}
		return Check{Group: "Layout", Name: "legacy ~/.gil", Status: StatusInfo,
			Message: fmt.Sprintf("stat %s: %v", legacy, err)}
	}
	if _, err := os.Stat(filepath.Join(legacy, "MIGRATED")); err == nil {
		return Check{Group: "Layout", Name: "legacy ~/.gil", Status: StatusInfo,
			Message: "present, already migrated (safe to remove manually)"}
	}
	return Check{Group: "Layout", Name: "legacy ~/.gil", Status: StatusWarn,
		Message: "present and not migrated",
		Hint:    `run "gil init" to migrate it into the XDG layout`}
}

// checkGildBinary looks for `gild` on PATH. We use an injectable lookPath
// so tests can swap in a temp PATH without affecting the rest of the
// process. Missing `gild` is a FAIL because the daemon is essential —
// every non-trivial gil command shells out to it.
//
// Dev fallback: when PATH lookup fails, try `./bin/gild` relative to cwd
// and `<exe-dir>/gild` (a sibling of the running gil binary). This makes
// `./bin/gil doctor` work straight after `make build` without first
// running `make install`. The fallback path is annotated " (dev)" so
// the operator sees it isn't a globally-installed copy.
var (
	lookPath          = exec.LookPath
	gildExecutableFn  = os.Executable
	gildWorkingDirFn  = os.Getwd
	gildStatFn        = os.Stat
)

func checkGildBinary() Check {
	if p, err := lookPath("gild"); err == nil {
		return Check{
			Group:   "Daemon",
			Name:    "gild binary",
			Status:  StatusOK,
			Message: p,
		}
	}
	// Dev fallbacks. Prefer ./bin/gild (the make-build artifact) over
	// the sibling-of-gil path because it's the location the project
	// docs and Makefile both reference; the sibling probe catches the
	// case where the gil binary itself was placed elsewhere (e.g.
	// `go install` into GOBIN).
	if p, ok := devGildPath(); ok {
		return Check{
			Group:   "Daemon",
			Name:    "gild binary",
			Status:  StatusOK,
			Message: p + " (dev)",
		}
	}
	return Check{
		Group:   "Daemon",
		Name:    "gild binary",
		Status:  StatusFail,
		Message: "not found on PATH",
		Hint:    `install with "make build install" or download a release bundle`,
	}
}

// devGildPath returns a path to a `gild` binary discovered via the dev
// fallbacks (cwd/bin/gild or sibling-of-exe), and ok=true when one was
// found. The probes use the injectable seams so tests can drive them
// hermetically.
func devGildPath() (string, bool) {
	candidates := []string{}
	if cwd, err := gildWorkingDirFn(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "bin", "gild"))
	}
	if exe, err := gildExecutableFn(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "gild"))
	}
	for _, c := range candidates {
		info, err := gildStatFn(c)
		if err != nil || info.IsDir() {
			continue
		}
		// Executable bit check: at least one of u/g/o-x must be set.
		if info.Mode()&0o111 == 0 {
			continue
		}
		return c, true
	}
	return "", false
}

// checkDaemonRunning dials the gild Unix socket; OK when it answers,
// INFO ("auto-spawned on first command") when it doesn't. We deliberately
// avoid WARN here: a stopped daemon is the normal state on a fresh
// machine, and ensureDaemon will start it on demand.
func checkDaemonRunning(sock string) Check {
	c, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return Check{
			Group:   "Daemon",
			Name:    "gild daemon",
			Status:  StatusInfo,
			Message: fmt.Sprintf("not running at %s (auto-spawned on first run command)", sock),
		}
	}
	_ = c.Close()
	return Check{
		Group:   "Daemon",
		Name:    "gild daemon",
		Status:  StatusOK,
		Message: fmt.Sprintf("running at %s", sock),
	}
}

// checkCredentials walks the credstore and emits one OK Check per
// configured provider (with masked key + last-updated timestamp), or a
// single WARN when nothing is configured. We never emit FAIL here
// because a not-yet-configured store is the legitimate state right
// after install — `gil doctor` post-install should not exit non-zero.
func checkCredentials(ctx context.Context, authFile string) []Check {
	store := credstore.NewFileStore(authFile)
	names, err := store.List(ctx)
	if err != nil {
		return []Check{{
			Group:   "Credentials",
			Name:    "auth.json",
			Status:  StatusWarn,
			Message: fmt.Sprintf("could not read %s: %v", authFile, err),
			Hint:    "check filesystem permissions, or remove the file and run \"gil auth login\"",
		}}
	}
	if len(names) == 0 {
		return []Check{{
			Group:   "Credentials",
			Name:    "providers",
			Status:  StatusWarn,
			Message: "no credentials configured",
			Hint:    `run "gil auth login" to add a provider`,
		}}
	}
	out := make([]Check, 0, len(names))
	for _, n := range names {
		cred, err := store.Get(ctx, n)
		if err != nil || cred == nil {
			continue
		}
		updated := "-"
		if !cred.Updated.IsZero() {
			updated = cred.Updated.Local().Format("2006-01-02")
		}
		out = append(out, Check{
			Group:   "Credentials",
			Name:    string(n),
			Status:  StatusOK,
			Message: fmt.Sprintf("%s   (last updated %s)", cred.MaskedKey(), updated),
		})
	}
	return out
}

// checkEnvFallbacks reports which provider env vars are set in the
// current environment. These are informational only — gild reads them
// as a fallback after the credstore — but a user wondering "why is gil
// using key X" benefits from seeing the env-var picture too.
func checkEnvFallbacks() []Check {
	envs := []struct {
		key   string
		label string
	}{
		{"ANTHROPIC_API_KEY", "anthropic"},
		{"OPENAI_API_KEY", "openai"},
		{"OPENROUTER_API_KEY", "openrouter"},
		{"VLLM_API_KEY", "vllm"},
		{"VLLM_BASE_URL", "vllm (base url)"},
	}
	anySet := false
	out := []Check{}
	for _, e := range envs {
		if v := os.Getenv(e.key); v != "" {
			anySet = true
			out = append(out, Check{
				Group:   "Credentials",
				Name:    "env: " + e.key,
				Status:  StatusInfo,
				Message: fmt.Sprintf("set (provider: %s)", e.label),
			})
		}
	}
	if !anySet {
		out = append(out, Check{
			Group:   "Credentials",
			Name:    "env fallbacks",
			Status:  StatusInfo,
			Message: "no provider env vars set",
		})
	}
	return out
}

// checkSandboxes inspects the OS-specific sandbox helpers gil supports.
// Missing helpers are INFO — the LOCAL_SANDBOX backend is one of several
// (LOCAL_NATIVE, DOCKER, SSH, MODAL, DAYTONA), so absence isn't a hard
// failure. The verbose path lists every backend; the default keeps it
// brief.
func checkSandboxes() []Check {
	out := []Check{}

	// bwrap (Linux LOCAL_SANDBOX).
	if runtime.GOOS == "linux" {
		if p, err := lookPath("bwrap"); err != nil {
			out = append(out, Check{
				Group:   "Sandboxes",
				Name:    "bwrap",
				Status:  StatusInfo,
				Message: "not found (needed for LOCAL_SANDBOX backend on Linux)",
			})
		} else {
			out = append(out, Check{
				Group:   "Sandboxes",
				Name:    "bwrap",
				Status:  StatusOK,
				Message: p + " (LOCAL_SANDBOX on Linux)",
			})
		}
	}

	// sandbox-exec (macOS LOCAL_SANDBOX).
	if runtime.GOOS == "darwin" {
		const seaPath = "/usr/bin/sandbox-exec"
		if _, err := os.Stat(seaPath); err != nil {
			out = append(out, Check{
				Group:   "Sandboxes",
				Name:    "sandbox-exec",
				Status:  StatusInfo,
				Message: "not found at " + seaPath,
			})
		} else {
			out = append(out, Check{
				Group:   "Sandboxes",
				Name:    "sandbox-exec",
				Status:  StatusOK,
				Message: seaPath + " (LOCAL_SANDBOX on macOS)",
			})
		}
	}

	// docker (DOCKER backend).
	if p, err := lookPath("docker"); err != nil {
		out = append(out, Check{
			Group:   "Sandboxes",
			Name:    "docker",
			Status:  StatusInfo,
			Message: "not found (needed for DOCKER backend)",
		})
	} else {
		out = append(out, Check{
			Group:   "Sandboxes",
			Name:    "docker",
			Status:  StatusOK,
			Message: p,
		})
	}

	// ssh + rsync (SSH backend).
	sshPath, sshErr := lookPath("ssh")
	rsyncPath, rsyncErr := lookPath("rsync")
	switch {
	case sshErr == nil && rsyncErr == nil:
		out = append(out, Check{
			Group:   "Sandboxes",
			Name:    "ssh + rsync",
			Status:  StatusOK,
			Message: fmt.Sprintf("ssh=%s rsync=%s (SSH backend)", sshPath, rsyncPath),
		})
	case sshErr == nil:
		out = append(out, Check{
			Group:   "Sandboxes",
			Name:    "ssh + rsync",
			Status:  StatusInfo,
			Message: "ssh present, rsync missing",
			Hint:    "install rsync (e.g. apt install rsync) for the SSH backend",
		})
	case rsyncErr == nil:
		out = append(out, Check{
			Group:   "Sandboxes",
			Name:    "ssh + rsync",
			Status:  StatusInfo,
			Message: "rsync present, ssh missing",
			Hint:    "install openssh-client for the SSH backend",
		})
	default:
		out = append(out, Check{
			Group:   "Sandboxes",
			Name:    "ssh + rsync",
			Status:  StatusInfo,
			Message: "neither ssh nor rsync found (needed for SSH backend)",
		})
	}

	return out
}

// checkTools verifies the small set of "must have" external tools. git
// is the only hard FAIL: gil's checkpoint subsystem uses a shadow git
// tree to record edits, and without it nothing else works.
func checkTools() []Check {
	out := []Check{}
	if p, err := lookPath("git"); err != nil {
		out = append(out, Check{
			Group:   "Tools",
			Name:    "git",
			Status:  StatusFail,
			Message: "not found on PATH",
			Hint:    "install git — gil uses a shadow git tree to checkpoint edits",
		})
	} else {
		out = append(out, Check{
			Group:   "Tools",
			Name:    "git",
			Status:  StatusOK,
			Message: p,
		})
	}
	return out
}

// renderDoctorText prints the full report in human-readable form. We
// group checks by Check.Group, preserving first-seen order so the layout
// of the output is deterministic. Status is rendered as a leading mark
// (✓ / • / ! / ✗) to keep lines scannable; verbose adds the build/info
// preamble.
func renderDoctorText(w io.Writer, checks []Check, verbose bool) {
	// Header — always emitted, with a one-line build identification.
	fmt.Fprintln(w, doctorHeader())
	fmt.Fprintln(w)

	groups := []string{}
	seen := map[string]bool{}
	byGroup := map[string][]Check{}
	for _, c := range checks {
		if !seen[c.Group] {
			seen[c.Group] = true
			groups = append(groups, c.Group)
		}
		byGroup[c.Group] = append(byGroup[c.Group], c)
	}

	for _, g := range groups {
		fmt.Fprintf(w, "%s:\n", g)
		for _, c := range byGroup[g] {
			fmt.Fprintf(w, "  %s %-14s %s\n", statusGlyph(c.Status), c.Name, c.Message)
			if c.Hint != "" {
				fmt.Fprintf(w, "    hint: %s\n", c.Hint)
			}
		}
		fmt.Fprintln(w)
	}

	if verbose {
		fmt.Fprintf(w, "Runtime: %s/%s, go%s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
		if bi, ok := debug.ReadBuildInfo(); ok {
			fmt.Fprintf(w, "Module:  %s (%s)\n", bi.Main.Path, bi.Main.Version)
		}
		fmt.Fprintln(w)
	}

	// Summary line — counts of each status, mirroring goose's "N
	// passed, M warning, K failed" footer.
	var ok, info, warn, fail int
	for _, c := range checks {
		switch c.Status {
		case StatusOK:
			ok++
		case StatusInfo:
			info++
		case StatusWarn:
			warn++
		case StatusFail:
			fail++
		}
	}
	fmt.Fprintf(w, "%d OK, %d INFO, %d WARN, %d FAIL.\n", ok, info, warn, fail)
}

// renderDoctorJSON emits a machine-readable summary: the build/version
// header plus the raw check list. JSON is single-document (not JSONL)
// so callers can pipe directly into jq without splitting lines.
func renderDoctorJSON(w io.Writer, checks []Check) error {
	type payload struct {
		Version string  `json:"version"`
		GOOS    string  `json:"goos"`
		GOARCH  string  `json:"goarch"`
		GoVer   string  `json:"go_version"`
		Checks  []Check `json:"checks"`
	}
	p := payload{
		Version: doctorVersion(),
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		GoVer:   runtime.Version(),
		Checks:  checks,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

// statusGlyph picks a one-char marker for each Status. UTF-8 always —
// the harness's target platforms (Linux, macOS) ship UTF-8 terminals by
// default. We avoid lipgloss because cli/go.mod has no styling
// dependency yet and adding one for two glyphs is not worth the build
// graph churn.
func statusGlyph(s Status) string {
	switch s {
	case StatusOK:
		return "✓"
	case StatusInfo:
		return "•"
	case StatusWarn:
		return "!"
	case StatusFail:
		return "✗"
	default:
		return "?"
	}
}

// doctorHeader returns the one-line "gil <version> (<commit>)" banner.
// We pull the version from the standard runtime/debug.BuildInfo so we
// don't have to plumb -ldflags through the cmd package: any tagged
// release inherits the right version, dev builds say "(devel)".
func doctorHeader() string {
	return "gil " + doctorVersion()
}

// doctorVersion returns the best-available version string. Order:
//  1. main.version baked in via -ldflags (release builds).
//  2. runtime/debug.BuildInfo.Main.Version (Go module versioning).
//  3. Literal "0.0.0-dev" — the unambiguous "I have no idea" fallback.
func doctorVersion() string {
	if v := strings.TrimSpace(injectedVersion); v != "" {
		return v
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "0.0.0-dev"
}

// doctorExitFn is the package-level seam for the FAIL→exit-code-1
// translation. Production code uses os.Exit; tests override it with a
// recorder so they can assert "would have exited with code N" without
// killing the test binary.
var doctorExitFn = os.Exit

// injectedVersion is the package-level seam through which a future
// `cmd.SetVersion(main.version)` plumbing can stamp release builds. It
// stays empty by default so debug.ReadBuildInfo wins for both `go run`
// and `go install` builds; only if and when we wire main.version through
// will the literal take precedence.
var injectedVersion = ""

// SetVersion lets the binary's main package inject build-time version
// strings without a circular import. Called from cli/cmd/gil/main.go in
// a follow-up commit if/when we wire main.version through. Today it's a
// no-op — kept here so the future plumbing doesn't require a new
// public API on the package.
func SetVersion(v string) {
	injectedVersion = v
}

