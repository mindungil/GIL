package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/core/cliutil"
	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/permission"
)

// permissionsCmd is the parent of `gil permissions list / remove / clear`.
//
// All three are LOCAL operations — they read and rewrite the
// $XDG_STATE_HOME/gil/permissions.toml file directly via
// permission.PersistentStore. There is intentionally no daemon
// roundtrip: the store is the single source of truth, and the
// daemon re-reads it on the next permission_ask, so any change here
// takes effect immediately for both running and future sessions.
//
// Reference: the Phase-12 PersistentStore in core/permission/store.go.
func permissionsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "permissions",
		Short: "Manage persistent allow / deny rules",
		Long: `Inspect and edit the persistent permission store.

The store lives at $XDG_STATE_HOME/gil/permissions.toml (under
$GIL_HOME/state/permissions.toml when GIL_HOME is set). Rules are
keyed by absolute project path; a rule granted in one project never
carries over to another.`,
	}
	c.AddCommand(permissionsListCmd())
	c.AddCommand(permissionsRemoveCmd())
	c.AddCommand(permissionsClearCmd())
	return c
}

// permissionsStorePath resolves the on-disk path the same way gild
// does (paths.FromEnv → Layout.State + "permissions.toml"). Centralised
// so all three subcommands stay in lock-step with the daemon's view.
func permissionsStorePath() string {
	layout, _ := paths.FromEnv()
	return filepath.Join(layout.State, "permissions.toml")
}

func permissionsListCmd() *cobra.Command {
	var project string
	c := &cobra.Command{
		Use:   "list",
		Short: "List persisted allow / deny rules (per project)",
		Long: `Print every project entry in the persistent store.

By default, every project is shown. Pass --project <abs-path> to
filter to one project.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store := &permission.PersistentStore{Path: permissionsStorePath()}
			projects, err := store.Projects()
			if err != nil {
				return cliutil.Wrap(err,
					"could not read the permissions store",
					`check that $XDG_STATE_HOME/gil/permissions.toml is readable, or run "gil doctor" for the resolved layout`)
			}
			if project != "" {
				if !filepath.IsAbs(project) {
					return cliutil.New(
						fmt.Sprintf("--project must be an absolute path, got %q", project),
						`use the absolute workspace path the daemon recorded (see "gil doctor")`)
				}
				projects = []string{project}
			}
			rows := make([]permissionsListRow, 0, len(projects))
			for _, p := range projects {
				rules, err := store.Load(p)
				if err != nil {
					return cliutil.Wrap(err, "load store: "+err.Error(), "")
				}
				if rules == nil {
					rules = &permission.ProjectRules{}
				}
				rows = append(rows, permissionsListRow{
					Project: p,
					Allow:   append([]string(nil), rules.AlwaysAllow...),
					Deny:    append([]string(nil), rules.AlwaysDeny...),
				})
			}
			if outputJSON() {
				return writePermissionsListJSON(cmd.OutOrStdout(), rows)
			}
			writePermissionsList(cmd.OutOrStdout(), rows, asciiMode)
			return nil
		},
	}
	c.Flags().StringVar(&project, "project", "", "filter to one project (absolute path)")
	return c
}

func permissionsRemoveCmd() *cobra.Command {
	var project string
	var allow, deny bool
	c := &cobra.Command{
		Use:   "remove <pattern>",
		Short: "Remove an allow / deny rule from the persistent store",
		Long: `Remove a single rule by pattern from one project's persistent list.

Either --allow or --deny must be supplied so the pattern is
unambiguous (the same shape can legally appear on both lists for the
same project — symmetric to cline's CommandPermissionController).

When --project is omitted, the current working directory is used. The
project must be an absolute path; resolving via os.Getwd makes the
common case ("I am in the repo I want to edit") work without typing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := args[0]
			if pattern == "" {
				return cliutil.New("pattern is empty", `pass the literal command shape, e.g. "git status"`)
			}
			if allow == deny {
				return cliutil.New(
					"pass exactly one of --allow or --deny",
					`the pattern can legally appear on both lists for the same project, so we refuse to guess`)
			}
			list := "always_allow"
			label := "allow"
			if deny {
				list = "always_deny"
				label = "deny"
			}
			projectPath, err := resolveProjectPath(project)
			if err != nil {
				return err
			}
			store := &permission.PersistentStore{Path: permissionsStorePath()}
			// Pre-check so we can report the "no such rule" case
			// distinctly from the success case (the underlying Remove
			// is idempotent — useful for scripts, but a user typing
			// the wrong pattern deserves an error, not silent OK).
			rules, err := store.Load(projectPath)
			if err != nil {
				return cliutil.Wrap(err, "could not read the permissions store", "")
			}
			if rules == nil || !containsPattern(rules, list, pattern) {
				return cliutil.New(
					fmt.Sprintf("no %s rule %q under %s", label, pattern, projectPath),
					`run "gil permissions list" to see the rules currently in effect`)
			}
			if err := store.Remove(projectPath, list, pattern); err != nil {
				return cliutil.Wrap(err, "could not remove rule", "")
			}
			g := uistyle.NewGlyphs(asciiMode)
			p := uistyle.NewPalette(false)
			fmt.Fprintf(cmd.OutOrStdout(), "   %s removed %s rule %s from %s\n",
				p.Success(g.Done), p.Primary(label), p.Primary(strconv.Quote(pattern)), p.Dim(projectPath))
			return nil
		},
	}
	c.Flags().StringVar(&project, "project", "", "project root (absolute path); defaults to current working directory")
	c.Flags().BoolVar(&allow, "allow", false, "remove from the allow list")
	c.Flags().BoolVar(&deny, "deny", false, "remove from the deny list")
	return c
}

func permissionsClearCmd() *cobra.Command {
	var project string
	var yes bool
	c := &cobra.Command{
		Use:   "clear",
		Short: "Remove ALL rules for a project",
		Long: `Clear every persistent rule (allow + deny) for one project.

By default the current working directory is used. Confirms before
deleting; pass --yes to skip the prompt.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectPath, err := resolveProjectPath(project)
			if err != nil {
				return err
			}
			store := &permission.PersistentStore{Path: permissionsStorePath()}
			rules, err := store.Load(projectPath)
			if err != nil {
				return cliutil.Wrap(err, "could not read the permissions store", "")
			}
			if rules == nil || (len(rules.AlwaysAllow) == 0 && len(rules.AlwaysDeny) == 0) {
				fmt.Fprintln(cmd.OutOrStdout(), "   no rules — nothing to clear")
				return nil
			}
			if !yes {
				if err := promptClearConfirm(cmd, projectPath, rules); err != nil {
					return err
				}
			}
			for _, pat := range rules.AlwaysAllow {
				if err := store.Remove(projectPath, "always_allow", pat); err != nil {
					return cliutil.Wrap(err, "remove allow rule "+pat, "")
				}
			}
			for _, pat := range rules.AlwaysDeny {
				if err := store.Remove(projectPath, "always_deny", pat); err != nil {
					return cliutil.Wrap(err, "remove deny rule "+pat, "")
				}
			}
			g := uistyle.NewGlyphs(asciiMode)
			p := uistyle.NewPalette(false)
			fmt.Fprintf(cmd.OutOrStdout(),
				"   %s cleared %d rules (%d allow, %d deny) from %s\n",
				p.Success(g.Done), len(rules.AlwaysAllow)+len(rules.AlwaysDeny),
				len(rules.AlwaysAllow), len(rules.AlwaysDeny), p.Dim(projectPath))
			return nil
		},
	}
	c.Flags().StringVar(&project, "project", "", "project root (absolute path); defaults to current working directory")
	c.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return c
}

// resolveProjectPath turns the optional --project flag into a clean
// absolute path. Empty → os.Getwd; relative → Abs against cwd.
func resolveProjectPath(p string) (string, error) {
	if p == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", cliutil.Wrap(err,
				"could not resolve current working directory",
				`pass --project <abs-path> explicitly`)
		}
		return cwd, nil
	}
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", cliutil.Wrap(err,
				"could not resolve project path",
				`pass an absolute path to --project`)
		}
		p = abs
	}
	return filepath.Clean(p), nil
}

// containsPattern reports whether the named list (always_allow|always_deny)
// already contains pattern. The store package treats Remove as idempotent,
// so we do the lookup at the CLI to surface a "no such rule" error cleanly.
func containsPattern(r *permission.ProjectRules, list, pattern string) bool {
	target := r.AlwaysAllow
	if list == "always_deny" {
		target = r.AlwaysDeny
	}
	for _, p := range target {
		if p == pattern {
			return true
		}
	}
	return false
}

// permissionsListRow is the per-project shape both renderers consume.
type permissionsListRow struct {
	Project string   `json:"project"`
	Allow   []string `json:"allow"`
	Deny    []string `json:"deny"`
}

// writePermissionsList renders rows in the spec layout —
//
//	/abs/path/to/proj
//	  allow:
//	     »  git status
//	  deny:
//	     »  rm -rf *
func writePermissionsList(w io.Writer, rows []permissionsListRow, ascii bool) {
	g := uistyle.NewGlyphs(ascii)
	p := uistyle.NewPalette(false)
	if len(rows) == 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "   %s\n", p.Dim("No persisted rules."))
		fmt.Fprintf(w, "   %s  approve a command in a session and pick \"always allow\" to record one\n",
			p.Info(g.Arrow))
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintln(w)
	for i, r := range rows {
		// Sort within each list so output is deterministic for tests.
		sort.Strings(r.Allow)
		sort.Strings(r.Deny)
		fmt.Fprintf(w, "   %s\n", p.Primary(r.Project))
		if len(r.Allow) > 0 {
			fmt.Fprintf(w, "     %s\n", p.Dim("allow:"))
			for _, pat := range r.Allow {
				fmt.Fprintf(w, "        %s  %s\n", p.Dim(g.Bullet), p.Success(pat))
			}
		}
		if len(r.Deny) > 0 {
			fmt.Fprintf(w, "     %s\n", p.Dim("deny:"))
			for _, pat := range r.Deny {
				fmt.Fprintf(w, "        %s  %s\n", p.Dim(g.Bullet), p.Alert(pat))
			}
		}
		if len(r.Allow) == 0 && len(r.Deny) == 0 {
			fmt.Fprintf(w, "     %s\n", p.Dim("(no rules)"))
		}
		if i != len(rows)-1 {
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintln(w)
}

func writePermissionsListJSON(w io.Writer, rows []permissionsListRow) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if rows == nil {
		rows = []permissionsListRow{}
	}
	return enc.Encode(struct {
		Projects []permissionsListRow `json:"projects"`
	}{Projects: rows})
}

// promptClearConfirm prints the y/N gate for `permissions clear`.
// Default-deny — same shape as promptDeleteConfirm in session.go.
func promptClearConfirm(cmd *cobra.Command, project string, r *permission.ProjectRules) error {
	g := uistyle.NewGlyphs(asciiMode)
	p := uistyle.NewPalette(false)
	out := cmd.ErrOrStderr()
	fmt.Fprintf(out, "   %s This will clear %d allow + %d deny rules from %s.\n",
		p.Caution(g.Warn), len(r.AlwaysAllow), len(r.AlwaysDeny), p.Dim(project))
	fmt.Fprintf(out, "   Continue? %s ", p.Dim("[y/N]"))
	rd := bufio.NewReader(cmd.InOrStdin())
	line, _ := rd.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return cliutil.New("cancelled", `pass --yes to skip the prompt`)
}

