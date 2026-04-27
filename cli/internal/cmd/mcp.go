package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jedutools/gil/core/cliutil"
	"github.com/jedutools/gil/core/mcpregistry"
	"github.com/jedutools/gil/core/workspace"
)

// mcpCmd returns the `gil mcp` subcommand group.
//
// Like `gil auth`, this is a local-only file operation that never talks to
// gild — registry edits land in either the global Config/mcp.toml or the
// project-scoped <workspace>/.gil/mcp.toml. The daemon picks them up the
// next time RunService.Start loads the registry.
//
// Reference lift:
//   - codex `cli/src/mcp_cmd.rs` — `mcp add/list/remove` shape with the
//     `-- COMMAND [ARGS...]` separator for stdio servers.
//   - opencode `cli/cmd/mcp.ts` — project + global scope flags on the same
//     CLI surface.
func mcpCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP server registry (stdio + http servers)",
		Long: `Manage the MCP (Model Context Protocol) server registry.

Servers live in two layers:
  - Global   ($XDG_CONFIG_HOME/gil/mcp.toml) — available in every project.
  - Project  (<workspace>/.gil/mcp.toml)     — scoped to one repo, overrides global.

Edits are written atomically with mode 0600 (bearer tokens are stored inline).
The gild daemon reads the registry at run start; restarting it is not required
for new MCP servers to appear in the next run.`,
	}
	c.AddCommand(mcpListCmd())
	c.AddCommand(mcpAddCmd())
	c.AddCommand(mcpRemoveCmd())
	c.AddCommand(mcpLoginCmd())
	c.AddCommand(mcpLogoutCmd())
	return c
}

// newRegistry resolves the global + project registry paths using the same
// rules as the rest of the CLI: XDG-derived layout for global, workspace
// discovery for project. ProjectPath is set unconditionally — callers that
// only operate on global scope can ignore it; callers that need project
// scope check IsConfigured separately so they can emit a "run gil init"
// hint with a stable error message.
func newRegistry(cwd string) (*mcpregistry.Registry, string /*projectRoot*/, error) {
	layout := defaultLayout()
	root, err := workspace.Discover(cwd)
	if err != nil {
		return nil, "", fmt.Errorf("workspace discover: %w", err)
	}
	return &mcpregistry.Registry{
		GlobalPath:  layout.MCPConfigFile(),
		ProjectPath: workspace.LocalMCPFile(root),
	}, root, nil
}

// mcpListCmd implements `gil mcp list`. Output is a tabwriter table with
// SCOPE so the user can tell which file an entry came from. URLs and
// commands are abbreviated only by tabwriter padding; bearer tokens in the
// auth column are masked unconditionally so a copy/paste of the terminal
// never leaks one.
func mcpListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List configured MCP servers (global + project)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return cliutil.Wrap(err, "could not resolve current directory", "")
			}
			reg, _, err := newRegistry(cwd)
			if err != nil {
				return cliutil.Wrap(err, "could not resolve MCP registry", "")
			}

			global, err := reg.LoadScope(mcpregistry.ScopeGlobal)
			if err != nil {
				return cliutil.Wrap(err, "could not read global MCP registry", "")
			}
			project, err := reg.LoadScope(mcpregistry.ScopeProject)
			if err != nil {
				return cliutil.Wrap(err, "could not read project MCP registry", "")
			}

			out := cmd.OutOrStdout()
			if len(global) == 0 && len(project) == 0 {
				fmt.Fprintln(out, "No MCP servers configured. Run \"gil mcp add <name> ...\" to add one.")
				fmt.Fprintf(out, "Global file:  %s\n", reg.GlobalPath)
				fmt.Fprintf(out, "Project file: %s\n", reg.ProjectPath)
				return nil
			}

			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSCOPE\tTYPE\tCOMMAND/URL\tDESCRIPTION")
			// Stable order: global first, then project, both sorted by name.
			for _, row := range orderedRows(global, mcpregistry.ScopeGlobal) {
				fmt.Fprintln(tw, row)
			}
			for _, row := range orderedRows(project, mcpregistry.ScopeProject) {
				fmt.Fprintln(tw, row)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			return nil
		},
	}
	return c
}

// orderedRows returns tabwriter-formatted rows for one scope, sorted by
// server name. Bearer tokens never appear here — only Type, Command/URL,
// and Description, with the auth credential rendered as "+bearer" when
// present so the user knows it exists without seeing the value.
func orderedRows(servers map[string]mcpregistry.Server, scope string) []string {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	rows := make([]string, 0, len(names))
	for _, n := range names {
		s := servers[n]
		rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			n, scope, s.Type, displayTarget(s), displayDescription(s)))
	}
	return rows
}

// displayTarget renders the "what does this server connect to" column.
// For stdio: "<command> <arg1> <arg2>...". For http: "<URL> [+bearer]".
// We never echo the bearer token itself.
func displayTarget(s mcpregistry.Server) string {
	switch s.Type {
	case "stdio":
		if len(s.Args) == 0 {
			return s.Command
		}
		return s.Command + " " + strings.Join(s.Args, " ")
	case "http":
		if strings.HasPrefix(s.Auth, "bearer:") {
			return s.URL + " (+bearer)"
		}
		return s.URL
	default:
		return ""
	}
}

func displayDescription(s mcpregistry.Server) string {
	if s.Description == "" {
		return "-"
	}
	return s.Description
}

// mcpAddCmd implements `gil mcp add <name> ...`. The command shape mirrors
// codex's `mcp add` — `--type stdio` consumes everything after `--` as the
// command + argv tail, while `--type http` takes a `--url` and optional
// `--bearer`. We refuse to add a server that already exists in the same
// scope so typos do not silently overwrite a working entry.
func mcpAddCmd() *cobra.Command {
	var (
		typ         string
		project     bool
		urlFlag     string
		bearer      string
		envPairs    []string
		description string
	)
	c := &cobra.Command{
		Use:   "add <name> [flags] [-- COMMAND [ARGS...]]",
		Short: "Add an MCP server to the registry",
		Long: `Add an MCP server to the registry.

For stdio servers, pass the command after a `+"`--`"+` separator:
  gil mcp add fs --type stdio -- npx -y @modelcontextprotocol/server-filesystem /tmp

For http servers, pass --url (and optionally --bearer):
  gil mcp add issues --type http --url https://issues.example.com/mcp --bearer SECRET

Bearer tokens are stored inline in mcp.toml (chmod 0600). Pass --bearer to
provide a token non-interactively, or omit it to be prompted (terminal-only;
piped stdin returns an error so scripts fail loudly).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return cliutil.New("MCP server name is empty", `usage: gil mcp add <name> --type stdio -- COMMAND [ARGS...]`)
			}

			cwd, err := os.Getwd()
			if err != nil {
				return cliutil.Wrap(err, "could not resolve current directory", "")
			}
			reg, root, err := newRegistry(cwd)
			if err != nil {
				return cliutil.Wrap(err, "could not resolve MCP registry", "")
			}

			// Build the Server record from flags. The dispatcher trusts
			// mcpregistry.Validate to enforce shape, but we pre-check
			// type-specific required pieces so the error message is
			// CLI-vocabulary rather than registry-vocabulary.
			s := mcpregistry.Server{
				Name:        name,
				Type:        typ,
				Description: description,
			}

			switch typ {
			case "stdio":
				// Cobra strips the leading "--"; everything after is in args[1:].
				if len(args) < 2 {
					return cliutil.New(
						"stdio MCP servers require a command",
						`usage: gil mcp add <name> --type stdio -- COMMAND [ARGS...]`)
				}
				s.Command = args[1]
				if len(args) > 2 {
					s.Args = append([]string{}, args[2:]...)
				}
				if urlFlag != "" {
					return cliutil.New(
						"--url is not valid for stdio MCP servers",
						`omit --url, or set --type http`)
				}
				if env, eerr := parseEnvPairs(envPairs); eerr != nil {
					return cliutil.Wrap(eerr, "invalid --env pair", `format: --env KEY=VALUE`)
				} else {
					s.Env = env
				}
			case "http":
				if strings.TrimSpace(urlFlag) == "" {
					return cliutil.New(
						"http MCP servers require --url",
						`pass --url https://...`)
				}
				if len(args) > 1 {
					return cliutil.New(
						"http MCP servers do not take a positional command",
						`drop the trailing command/args, or set --type stdio`)
				}
				s.URL = urlFlag
				token, terr := resolveBearer(cmd, bearer)
				if terr != nil {
					return terr
				}
				if token != "" {
					s.Auth = "bearer:" + token
				}
			case "":
				return cliutil.New(
					"--type is required (one of: stdio, http)",
					`pass --type stdio or --type http`)
			default:
				return cliutil.New(
					fmt.Sprintf("unknown --type %q", typ),
					`use --type stdio or --type http`)
			}

			scope := mcpregistry.ScopeGlobal
			if project {
				scope = mcpregistry.ScopeProject
			}

			if scope == mcpregistry.ScopeProject {
				if !workspace.IsConfigured(root) {
					return cliutil.New(
						fmt.Sprintf("project workspace %s has no .gil directory", root),
						`run "gil init" first, or omit --project to write the global registry`)
				}
				if err := reg.AddProject(s); err != nil {
					return cliutil.Wrap(err, "could not add MCP server to project registry", "")
				}
			} else {
				if err := reg.AddGlobal(s); err != nil {
					return cliutil.Wrap(err, "could not add MCP server to global registry", "")
				}
			}

			path := reg.GlobalPath
			if scope == mcpregistry.ScopeProject {
				path = reg.ProjectPath
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added MCP server %q to %s registry (%s).\n", name, scope, path)
			return nil
		},
	}
	c.Flags().StringVar(&typ, "type", "", "transport type: stdio | http")
	c.Flags().BoolVar(&project, "project", false, "write to project (<workspace>/.gil/mcp.toml) instead of global")
	c.Flags().StringVar(&urlFlag, "url", "", "endpoint URL (http only)")
	c.Flags().StringVar(&bearer, "bearer", "", "bearer token (http only); omit to be prompted")
	c.Flags().StringSliceVar(&envPairs, "env", nil, "extra env vars for stdio servers (KEY=VALUE; repeatable)")
	c.Flags().StringVar(&description, "description", "", "free-form one-line label shown in `gil mcp list`")
	return c
}

// parseEnvPairs splits a --env slice into a map. We accept multiple --env
// flags (StringSliceVar) and KEY=VALUE syntax — refusing the bare KEY form
// because empty strings would silently shadow an inherited env var, which
// is rarely the intent.
func parseEnvPairs(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		idx := strings.IndexByte(p, '=')
		if idx <= 0 {
			return nil, fmt.Errorf("env pair %q must be KEY=VALUE", p)
		}
		k := strings.TrimSpace(p[:idx])
		v := p[idx+1:]
		if k == "" {
			return nil, fmt.Errorf("env pair %q has empty key", p)
		}
		out[k] = v
	}
	return out, nil
}

// resolveBearer returns the bearer token to write into the registry. Order:
// explicit --bearer flag wins; otherwise prompt with term.ReadPassword IFF
// stdin is a TTY; piped stdin returns a structured error so scripts fail
// loudly rather than recording an empty token.
func resolveBearer(cmd *cobra.Command, flagValue string) (string, error) {
	if flagValue != "" {
		return strings.TrimSpace(flagValue), nil
	}
	in := cmd.InOrStdin()
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", cliutil.New(
			"bearer token not provided and stdin is not a terminal",
			`pass --bearer <token>, or run interactively`)
	}
	fmt.Fprint(cmd.OutOrStdout(), "Enter bearer token: ")
	raw, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(cmd.OutOrStdout())
	if err != nil {
		return "", cliutil.Wrap(err, "could not read bearer token", "try again, or pass --bearer")
	}
	return strings.TrimSpace(string(raw)), nil
}

// mcpRemoveCmd implements `gil mcp remove <name> [--global|--project|--auto]`.
// --auto (the default) tries global first, then project, matching the
// "least-surprising" remove semantics: most users edit the global registry,
// so reaching for project scope only happens when the name actually lives
// there.
func mcpRemoveCmd() *cobra.Command {
	var (
		global  bool
		project bool
		auto    bool
	)
	c := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove an MCP server from the registry",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return cliutil.New("MCP server name is empty", `usage: gil mcp remove <name>`)
			}
			scope, err := pickRemoveScope(global, project, auto)
			if err != nil {
				return err
			}

			cwd, err := os.Getwd()
			if err != nil {
				return cliutil.Wrap(err, "could not resolve current directory", "")
			}
			reg, _, err := newRegistry(cwd)
			if err != nil {
				return cliutil.Wrap(err, "could not resolve MCP registry", "")
			}

			if err := reg.Remove(name, scope); err != nil {
				return cliutil.Wrap(err, fmt.Sprintf("could not remove MCP server %q", name), "")
			}
			scopeLabel := scope
			if scope == mcpregistry.ScopeAuto {
				scopeLabel = "registry (auto-scope)"
			} else {
				scopeLabel = scope + " registry"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed MCP server %q from %s.\n", name, scopeLabel)
			return nil
		},
	}
	c.Flags().BoolVar(&global, "global", false, "remove from global registry only")
	c.Flags().BoolVar(&project, "project", false, "remove from project registry only")
	c.Flags().BoolVar(&auto, "auto", false, "try project then global (default when no scope flag is set)")
	return c
}

// pickRemoveScope normalises the trio of --global/--project/--auto into a
// single registry scope string. At most one may be set; the absence of any
// flag is treated as --auto.
func pickRemoveScope(global, project, auto bool) (string, error) {
	count := 0
	for _, b := range []bool{global, project, auto} {
		if b {
			count++
		}
	}
	if count > 1 {
		return "", cliutil.New(
			"--global, --project, and --auto are mutually exclusive",
			`pass at most one scope flag`)
	}
	switch {
	case global:
		return mcpregistry.ScopeGlobal, nil
	case project:
		return mcpregistry.ScopeProject, nil
	default:
		return mcpregistry.ScopeAuto, nil
	}
}

// mcpLoginCmd is a placeholder for OAuth flow integration with MCP servers
// that advertise authorize/token endpoints. Phase 13 will wire this to the
// credstore; for now it returns a polite "not yet" so users discovering it
// in --help know the feature is on the roadmap.
func mcpLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login <name>",
		Short: "OAuth login to an http MCP server (Phase 13)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "OAuth coming in Phase 13.")
			return nil
		},
	}
}

func mcpLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <name>",
		Short: "Drop OAuth tokens for an MCP server (Phase 13)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "OAuth coming in Phase 13.")
			return nil
		},
	}
}
