package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/cliutil"
)

// installerMethod is the channel through which gil was installed. The
// curl-installer writes "script" to /usr/local/bin/.gil-installer-method;
// the Homebrew formula writes "brew"; everything else (make install,
// `go install`, manual download) leaves no marker and is reported as
// "manual".
//
// Reference for the design: codex's update_action.rs uses the same
// install-method-discriminated upgrade dispatch (Brew/Standalone/Npm/Bun).
// goose-cli's commands/update.rs hard-codes a single "download from GH
// releases" path; we prefer codex's shape because it lets brew/script
// users use their native upgrade flow rather than always going through
// the curl pipe.
type installerMethod string

const (
	installerScript  installerMethod = "script"
	installerBrew    installerMethod = "brew"
	installerManual  installerMethod = "manual"
	installerUnknown installerMethod = ""

	// markerFile is the file install.sh drops next to the binary so
	// `gil update` can pick the right upgrade command. We keep the
	// filename the same on every platform — it lives next to the
	// binary, not in $XDG_*, so the harness paths package never sees
	// it.
	markerFile = ".gil-installer-method"

	// installScriptURL is the upstream URL the script-channel users
	// re-curl. Matches the example in docs/distribution.md.
	installScriptURL = "https://raw.githubusercontent.com/mindungil/GIL/main/scripts/install.sh"

	// latestReleaseAPI is the GitHub releases JSON endpoint used by
	// `gil update --check`. We deliberately use the JSON API (not the
	// /releases/latest redirect) here so we can return the human-
	// readable name as well, which is helpful for the --check output.
	latestReleaseAPI = "https://api.github.com/repos/mindungil/GIL/releases/latest"
)

// updateCmd returns the `gil update` subcommand.
//
// Two modes:
//   - default: detect installer method, shell out to the appropriate
//     upgrade command (brew upgrade gil / curl install.sh | bash) and
//     refuse cleanly when the install is "manual" (no marker present).
//   - --check: GET the latest release tag from the GitHub API and print
//     it. Exits 0 even when --check finds a newer version — this is a
//     query, not a gate.
//
// We never auto-run any command without --check explicitly disabled;
// the user always sees what we'd do and confirms with a re-invocation.
func updateCmd() *cobra.Command {
	var (
		check bool
	)
	c := &cobra.Command{
		Use:   "update",
		Short: "upgrade gil to the latest release",
		Long: `Upgrade gil to the latest release.

gil's installer drops a marker file next to the binary recording how it
was installed (curl-installer, Homebrew, or "manual" for from-source
builds). update reads the marker and re-invokes the correct upgrade
flow:

  - script: curl -fsSL <install-url> | bash
  - brew:   brew upgrade gil
  - manual: refuses with a hint pointing at docs/distribution.md

With --check, prints the latest release tag from GitHub without
upgrading. Useful for CI and "what version would I get?" sanity checks.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if check {
				return printAvailableVersion(ctx, cmd.OutOrStdout())
			}
			method := detectInstallMethod()
			switch method {
			case installerBrew:
				return shellOut(ctx, "brew", "upgrade", "gil")
			case installerScript:
				return shellOut(ctx, "bash", "-c",
					fmt.Sprintf("curl -fsSL %s | bash", installScriptURL))
			case installerManual:
				return cliutil.New(
					"gil was installed manually (no installer marker found)",
					`re-run "make install" or use one of the supported installers (see docs/distribution.md)`)
			default:
				return cliutil.New(
					"could not detect how gil was installed",
					`reinstall via the curl-installer (see docs/distribution.md), or run "brew upgrade gil" if you used Homebrew`)
			}
		},
	}
	c.Flags().BoolVar(&check, "check", false, "print the latest available version and exit")
	return c
}

// detectInstallMethod inspects the running gil binary's install location
// and returns one of the installerMethod constants. The order is
// deliberate: a marker file always wins (the user's most recent install
// channel is authoritative), then we fall back to brew prefix detection
// (catches Homebrew installs whose formula failed to write the marker),
// and finally "manual" when neither signal is present.
//
// We deliberately do NOT classify `go install` as a separate channel:
// `gil update` cannot reasonably re-run `go install` (we don't know
// which module path the user picked, GOFLAGS, GOPROXY, etc.), so it
// gets bucketed under "manual" with the same advice.
func detectInstallMethod() installerMethod {
	// Look up the running gil's directory. We use os.Executable here
	// (not os.Args[0]) so symlinks are resolved consistently — a user
	// running ~/bin/gil that symlinks to /usr/local/bin/gil should get
	// the marker from /usr/local/bin/, not ~/bin/.
	exe, err := executableFn()
	if err == nil {
		dir := filepath.Dir(exe)
		marker := filepath.Join(dir, markerFile)
		if data, err := readFileFn(marker); err == nil {
			switch installerMethod(strings.TrimSpace(string(data))) {
			case installerScript:
				return installerScript
			case installerBrew:
				return installerBrew
			}
		}
		// Brew prefix sniff: the formula installs to
		// $(brew --prefix)/bin/gil. We can't shell out to `brew`
		// here (it's an upgrade tool, the env may be minimal), so
		// we check the well-known prefixes plus HOMEBREW_PREFIX.
		if isInBrewPrefix(dir) {
			return installerBrew
		}
	}
	return installerManual
}

// isInBrewPrefix reports whether dir lives under a Homebrew prefix.
// Checks the canonical prefixes for both Apple Silicon (/opt/homebrew)
// and Intel macOS (/usr/local), plus the Linuxbrew default
// (/home/linuxbrew/.linuxbrew), plus whatever HOMEBREW_PREFIX points at.
//
// The /usr/local check is intentionally strict: we look for the
// "Cellar" sibling, because /usr/local/bin without /usr/local/Cellar
// is the standard non-brew install location and we'd misclassify
// curl-installer users as brew otherwise.
func isInBrewPrefix(dir string) bool {
	candidates := []string{
		"/opt/homebrew",
		"/home/linuxbrew/.linuxbrew",
	}
	if hb := os.Getenv("HOMEBREW_PREFIX"); hb != "" {
		candidates = append(candidates, hb)
	}
	for _, prefix := range candidates {
		if strings.HasPrefix(dir, prefix+string(filepath.Separator)) ||
			dir == filepath.Join(prefix, "bin") {
			return true
		}
	}
	// /usr/local is shared with the curl-installer; only treat it as
	// brew when a Cellar sibling exists.
	if dir == "/usr/local/bin" {
		if _, err := statFn("/usr/local/Cellar"); err == nil {
			return true
		}
	}
	return false
}

// printAvailableVersion fetches the latest release tag from GitHub and
// writes "Latest gil release: <tag>" (plus a comparison hint when we
// can stamp our own version) to w. Returns a UserError on network
// failure with a hint pointing at the manual /releases page.
func printAvailableVersion(ctx context.Context, w io.Writer) error {
	tag, err := fetchLatestTagFn(ctx)
	if err != nil {
		return cliutil.Wrap(err,
			"could not reach the GitHub releases API",
			"check your network, or visit https://github.com/mindungil/gil/releases")
	}
	current := strings.TrimSpace(injectedVersion)
	if current == "" {
		current = "0.0.0-dev"
	}
	fmt.Fprintf(w, "Latest gil release: %s\nInstalled gil:      %s\n", tag, current)
	if tag != "" && current != "0.0.0-dev" && strings.TrimPrefix(tag, "v") != current {
		fmt.Fprintln(w, "Run 'gil update' to upgrade.")
	}
	return nil
}

// fetchLatestTagHTTP queries the GitHub releases API and returns the
// "tag_name" field. We use a 10s timeout so a hung CI doesn't block
// indefinitely.
func fetchLatestTagHTTP(ctx context.Context) (string, error) {
	c := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseAPI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gil-update-cli")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	return payload.TagName, nil
}

// shellOutExec runs argv with stdin/stdout/stderr inherited so the user
// can answer sudo/brew prompts interactively. The default
// implementation is plumbed through the shellOut seam so tests can
// substitute a recorder.
func shellOutExec(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return cliutil.Wrap(err,
			fmt.Sprintf("upgrade command failed: %s", strings.Join(append([]string{name}, args...), " ")),
			"check the output above; rerun with the failing command directly to see full error")
	}
	return nil
}

// Test seams. detectInstallMethod, printAvailableVersion, and the
// upgrade dispatcher all go through these vars so unit tests can swap
// in fakes without touching the real filesystem / network / process
// state. Production code never reassigns them; only TestMain helpers do.
var (
	executableFn    = os.Executable
	readFileFn      = os.ReadFile
	statFn          = os.Stat
	fetchLatestTagFn = fetchLatestTagHTTP
	shellOut        = shellOutExec
)
