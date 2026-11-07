package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/SandwichLabs/slaw/internal/connections"
	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var connCmd = &cobra.Command{
	Use:   "conn",
	Short: "Add, list, test and remove a workspace's connections",
	Long: `A connection is a place data lives: a local DuckDB or SQLite file, Postgres,
MySQL, MotherDuck, a DuckLake, an Iceberg or Delta table, or an S3, Google
Cloud Storage, Cloudflare R2 or Azure prefix. The
definition (host, database, ...) is a plain setting; the credentials never are:
they go to your OS keychain, or are read from environment variables you name
(--sign-in env:PREFIX), and are never printed back.

Scopes: --scope local (default; this computer, this workspace), shared (in the
committed slaw.yaml, so teammates get the definition and enter their own
credentials) or user (every workspace you open). See ` + docRef("connections"),
}

func addConnWorkspaceFlag(c *cobra.Command) { addWorkspaceFlag(c) }

// openWS opens just the workspace (no DuckDB hub, no lock): connection
// management is configuration and the keychain.
func openWS(cmd *cobra.Command) (workspace.Workspace, error) {
	dir, _ := cmd.Flags().GetString("workspace")
	if isSlawFile(dir) {
		return nil, errLegacyFileArg
	}
	root, err := hub.ResolveRoot(dir, "")
	if err != nil {
		return nil, err
	}
	ws, err := workspace.Open(root, workspace.WithLogger(logger), workspace.WithResolver(cliResolver(ssoTestOptions(cmdCtx(cmd))...)))
	if err != nil {
		return nil, fmt.Errorf("failed to open the workspace: %w", err)
	}
	for _, w := range ws.Warnings() {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
	}
	if !isEnvCmd(cmd) {
		maybePromptDotEnv(ws, cmd.ErrOrStderr())
	}
	return ws, nil
}

func cmdCtx(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// scopeFlag reads a --scope style flag value; the wording is the CLI's.
func scopeFlag(s string) (workspace.Scope, error) {
	sc, ok := workspace.ParseScope(s)
	if !ok {
		return "", fmt.Errorf("unknown scope %q (want user, shared or local)", s)
	}
	return sc, nil
}

// --- add ---

var connAddCmd = &cobra.Command{
	Use:   "add <name> --type <type>",
	Short: "Add a connection (credentials are prompted for and kept in the keychain)",
	Long: `Add a connection definition and, on a terminal, prompt for its credentials
(no echo) and store them in the keychain. Types: ` + "duckdb, sqlite, postgres, mysql, motherduck, ducklake, s3, gcs, r2, azure, iceberg, delta" + `.

  slaw conn add pg --type postgres --host db.internal --database analytics
  slaw conn add lake --type s3 --prefix s3://acme-lake/raw/ --region us-east-1
  slaw conn add md --type motherduck --database my_db --sign-in env:MOTHERDUCK
  slaw conn add dev --type s3 --prefix s3://acme-dev/ --region us-east-1 --sign-in aws-sso:dev
  slaw conn add b2 --type s3 --prefix s3://bucket/ --preset b2 --region us-west-004
  slaw conn add legacy --type sqlite --path data/legacy.sqlite
  slaw conn add shop --type mysql --host db.internal --database shop --option ssl_mode=required
  slaw conn add events --type azure --prefix az://events/2024/ --option sign_in=account_key --option account_name=acmelake
  slaw conn add wh --type ducklake --path data/wh.ducklake --option data_path=data/wh_files --read-only=false
  slaw conn add sales --type delta --path data/sales

Connections are read-only unless --read-only=false (MotherDuck is always
writable: it has no read-only attach). Without a terminal no
credentials are asked for; use "slaw conn credentials set <name>" (reads
stdin), --sign-in env:PREFIX or, for S3, --sign-in aws-sso:PROFILE (your AWS
IAM Identity Center profile; nothing is stored). Nothing secret ever goes on the
command line.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runConnAdd,
	SilenceUsage: true,
}

func runConnAdd(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()
	typ, _ := f.GetString("type")
	scopeS, _ := f.GetString("scope")
	scope, err := scopeFlag(scopeS)
	if err != nil {
		return err
	}
	if typ == "" {
		return fmt.Errorf("--type is required (one of %s)", strings.Join(connections.AllTypeNames(), ", "))
	}
	if _, err := connections.TypeInfoFor(typ); err != nil {
		return err
	}
	def := workspace.ConnectionDef{Name: args[0], Type: typ}
	def.Path, _ = f.GetString("path")
	def.Host, _ = f.GetString("host")
	def.Port, _ = f.GetInt("port")
	def.Database, _ = f.GetString("database")
	def.Region, _ = f.GetString("region")
	def.Scope, _ = f.GetString("prefix")
	ro, _ := f.GetBool("read-only")
	switch {
	case !ro:
		def.ReadOnly = workspace.Bool(false)
	case typ == "motherduck" && f.Changed("read-only"):
		// MotherDuck has no read-only attach: refuse instead of saving a
		// definition that can never attach. Left unset it defaults to writable.
		return errors.New(connections.ErrMotherDuckReadOnly)
	case f.Changed("read-only"):
		// Read-only is the default, so an unset flag leaves the line out; said
		// out loud, it is written, so a teammate reading slaw.yaml can tell.
		def.ReadOnly = workspace.Bool(true)
	}
	signIn, _ := f.GetString("sign-in")
	if def.Options, err = connOptions(f, typ, def.Region); err != nil {
		return err
	}

	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close() //nolint:errcheck
	ctx := cmdCtx(cmd)
	if signIn != "" {
		// Checked against the definition before anything is saved, so a refused
		// sign-in never leaves a connection behind.
		if err := checkSignInFor(ws, scope, def, signIn); err != nil {
			return err
		}
	}
	if err := ws.AddConnection(scope, def); err != nil {
		return err
	}
	if signIn != "" {
		if err := ws.SetSignIn(def.Name, signIn); err != nil {
			return err
		}
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Added %s connection %s (scope: %s).\n", typ, def.Name, scope)
	if w := workspace.SharedMetadataWarning(def, scope); w != "" {
		fmt.Fprintf(out, "Warning: %s\n", w)
	}
	printStorageHint(out, ws, def, scope)
	c, _ := findConn(ws, def.Name)
	if !c.ConnectionDef.NeedsCredential() {
		return nil
	}
	method := c.SignIn
	if kind, _, _ := secrets.ParseMethod(method); kind == "env" {
		names := secrets.EnvVarNames(strings.TrimPrefix(method, "env:"))
		if typ == "motherduck" {
			names = []string{strings.TrimPrefix(method, "env:") + "_TOKEN"} // a token is all MotherDuck takes
		}
		fmt.Fprintf(out, "Credentials come from the environment variables %s.\n", strings.Join(names, ", "))
		return nil
	} else if kind == secrets.MethodAWSSSO {
		printSSONote(out, def.Name, strings.TrimPrefix(method, secrets.MethodAWSSSOPref))
		return nil
	}
	if !stdinIsTerminal() {
		fmt.Fprintf(out, "No terminal, so no credentials were asked for. Run: slaw conn credentials set %s\n", def.Name)
		return nil
	}
	cred, err := promptCredentials(cmd.InOrStdin(), cmd.ErrOrStderr(), typ, secrets.Credential{})
	if err != nil {
		return err
	}
	if err := ws.SetCredentials(ctx, def.Name, cred, false); err != nil {
		return errors.New(cliRedactor.Redact(err.Error()))
	}
	fmt.Fprintf(out, "Credentials saved (%s). Try: slaw conn test %s\n", method, def.Name)
	return nil
}

// connOptions builds the definition's options from --preset (s3 only) and
// --option; an explicit --option wins over what the preset fills.
func connOptions(f *pflag.FlagSet, typ, region string) (map[string]string, error) {
	var opts map[string]string
	if id, _ := f.GetString("preset"); id != "" {
		if typ != "s3" {
			return nil, fmt.Errorf("--preset is for s3 connections (got --type %s)", typ)
		}
		var err error
		if opts, err = connections.PresetOptions(id, region); err != nil {
			return nil, err
		}
	}
	kvs, _ := f.GetStringArray("option")
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--option %q: want key=value", kv)
		}
		if opts == nil {
			opts = map[string]string{}
		}
		opts[k] = v
	}
	return opts, nil
}

func findConn(ws workspace.Workspace, name string) (workspace.Connection, bool) {
	return ws.Connection(name)
}

// --- list ---

var connListCmd = &cobra.Command{
	Use:   "list",
	Short: "List connections with scope, status and sign-in method",
	Long: `List the workspace's merged connections: where each is defined (a user entry
is hidden by a shared one, which a local one hides in turn), its read-only
flag, its sign-in method and its status:

  ok  not-connected  needs-credentials  extension-missing  locked-by  error

Nothing remote is dialled; a connection reads not-connected until it is used.
list reads local DuckDB files read-only and never takes the workspace.`,
	Args:         cobra.NoArgs,
	RunE:         runConnList,
	SilenceUsage: true,
}

type connRow struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Scope    string   `json:"scope"`
	Shadows  []string `json:"shadows,omitempty"`
	Status   string   `json:"status"`
	ReadOnly bool     `json:"read_only"`
	// ReadOnlyNote says when read-only is advisory (not enforced by attach).
	ReadOnlyNote string `json:"read_only_note,omitempty"`
	SignIn       string `json:"sign_in,omitempty"`
	Default      bool   `json:"default,omitempty"`
	Message      string `json:"message,omitempty"`
	// Warning is set on a shared lake or table definition (not an error).
	Warning string `json:"warning,omitempty"`
}

// showsSignIn reports whether a connection's list row names its sign-in: only
// a connection that needs a credential has one, judged on the definition when
// it is found (an iceberg_catalog's kind decides) and on the type otherwise.
func showsSignIn(ws workspace.Workspace, name, typ string) bool {
	if c, ok := findConn(ws, name); ok {
		return c.ConnectionDef.NeedsCredential()
	}
	return connections.NeedsCredential(typ)
}

func runConnList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	o, err := openWorkspace(cmdCtx(cmd), flagString(cmd, "workspace"), true, false, "slaw conn", io.Discard)
	if err != nil {
		return err
	}
	defer o.Close()
	var rows []connRow
	for _, st := range o.States() {
		r := connRow{Name: st.Name, Type: st.Type, Scope: string(st.Scope), Default: st.Default, ReadOnly: st.ReadOnly, Status: string(st.Status), Message: st.Detail, Warning: st.Warning}
		for _, s := range st.Shadows {
			r.Shadows = append(r.Shadows, string(s))
		}
		if showsSignIn(o.WS, st.Name, st.Type) {
			r.SignIn = st.SignIn
		}
		if info, err := connections.TypeInfoFor(st.Type); err == nil && r.ReadOnly && !info.Capabilities.ReadOnlyAttach && info.Capabilities.Catalog {
			r.ReadOnlyNote = "advisory"
			if st.Type == "postgres" || st.Type == "mysql" {
				r.ReadOnlyNote = "client-side; use a read-only role"
			}
			if st.Type == "motherduck" {
				r.ReadOnlyNote = "not enforced; use a read-scaling token"
			}
		}
		rows = append(rows, r)
	}
	if ok, why := o.Sess.RelaunchRequired(); ok {
		fmt.Fprintln(cmd.ErrOrStderr(), "note:", why)
	}
	printRowWarnings(cmd.ErrOrStderr(), rows)
	out := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "No connections. Add one with: slaw conn add <name> --type <type>")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSCOPE\tSTATUS\tREAD-ONLY\tSIGN-IN\tNOTE")
	for _, r := range rows {
		name := r.Name
		if r.Default {
			name += " *"
		}
		note := r.Message
		if len(r.Shadows) > 0 {
			note = strings.TrimSpace("shadows " + strings.Join(r.Shadows, ", ") + "; " + note)
		}
		ro := "no"
		if r.ReadOnly {
			ro = "yes"
			if r.ReadOnlyNote != "" {
				ro += " (" + r.ReadOnlyNote + ")"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, r.Type, r.Scope, r.Status, ro, dash(r.SignIn), oneLine(note))
	}
	return tw.Flush()
}

// printRowWarnings says, once per shared lake or table definition, why it needs care.
func printRowWarnings(w io.Writer, rows []connRow) {
	for _, r := range rows {
		if r.Warning != "" {
			fmt.Fprintf(w, "warning: %s is shared in slaw.yaml. %s\n", r.Name, r.Warning)
		}
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 100 {
		s = s[:97] + "..."
	}
	return s
}

func flagString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// --- test ---

var connTestCmd = &cobra.Command{
	Use:   "test <name>",
	Short: "Try a connection in a throwaway session (10 s limit)",
	Long: `Attach the connection in a throwaway in-memory DuckDB (never the workspace's
own session), run a cheap probe and report ok with the latency and a table or
object count, or a redacted error. MotherDuck is tested in a subprocess.

A DuckDB extension the connection needs is only downloaded with --install; without it
a missing extension is reported and nothing is fetched.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runConnTest,
	SilenceUsage: true,
}

func runConnTest(cmd *cobra.Command, args []string) error {
	install, _ := cmd.Flags().GetBool("install")
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close() //nolint:errcheck
	ctx := cmdCtx(cmd)
	c, ok := ws.Connection(args[0])
	if !ok {
		return fmt.Errorf("unknown connection %q", args[0])
	}
	rc, err := workspace.ResolveConnection(ws, c, nil)
	if err != nil {
		return errors.New(cliRedactor.Redact(err.Error()))
	}
	out := cmd.OutOrStdout()
	res, err := workspace.TestWithRequirements(ctx, ws, rc, "", secrets.Credential{}, connections.WithInstall(install), connections.WithTestRedactor(cliRedactor))
	if err != nil {
		var nc *secrets.ErrNeedsCredentials
		var fp *secrets.ErrFingerprintChanged
		if errors.As(err, &nc) || errors.As(err, &fp) || errors.Is(err, secrets.ErrUnavailable) {
			msg := cliRedactor.Redact(err.Error())
			if nc != nil && nc.SignIn != nil {
				msg += " Or run: slaw conn credentials sign-in " + args[0]
			}
			return fmt.Errorf("%s: needs credentials: %s", args[0], msg)
		}
		return errors.New(cliRedactor.Redact(err.Error()))
	}
	if !res.OK {
		return errors.New(connTestFailure(args[0], res))
	}
	for _, req := range rc.Requires {
		// The storage part is tried first, so it is reported first.
		fmt.Fprintf(out, "%s: ok (storage for %s)\n", req, args[0])
	}
	fmt.Fprintf(out, "%s: ok, %s (%d ms)\n", args[0], res.Detail, res.Latency.Milliseconds())
	return nil
}

// connTestFailure words a failed test. A failure of a storage connection the
// connection reads with says so first, and names the connection to fix.
func connTestFailure(name string, res connections.TestResult) string {
	msg := fmt.Sprintf("%s: %s: %s", name, res.Status, res.Error)
	if res.Requirement != "" {
		why := res.Error
		if why == "" {
			why = res.Message
		}
		msg = fmt.Sprintf("%s: storage connection %q failed (%s): %s\n(%s was not tried; fix %q with `slaw conn test %s`)", name, res.Requirement, res.Status, why, name, res.Requirement, res.Requirement)
	}
	if res.InstallExtension != "" {
		msg += fmt.Sprintf("\n(re-run with --install to download the %q extension)", res.InstallExtension)
	}
	return msg
}

// --- connect ---

var connConnectCmd = &cobra.Command{
	Use:   "connect <name>",
	Short: "Connect in a workspace session and show what is there",
	Long: `Open the workspace without taking ownership, connect the named connection in
that session (this is the same attach a query triggers on first use) and list
what it reached. Without --install a missing extension is reported and nothing
is downloaded.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runConnConnect,
	SilenceUsage: true,
}

func runConnConnect(cmd *cobra.Command, args []string) error {
	install, _ := cmd.Flags().GetBool("install")
	ctx := cmdCtx(cmd)
	name := args[0]
	var o *openedWorkspace
	var err error
	if isMotherDuck(cmd, name) {
		o, err = openWorkspaceRemoteOnly(ctx, flagString(cmd, "workspace"), io.Discard)
	} else {
		o, err = openWorkspace(ctx, flagString(cmd, "workspace"), true, false, "slaw conn", io.Discard)
	}
	if err != nil {
		return err
	}
	defer o.Close()
	c, ok := o.knownConnection(name)
	if !ok {
		return fmt.Errorf("unknown connection %q; this workspace has: %s", name, connectionNames(o))
	}
	if i, ok := o.issueFor(name); ok {
		return fmt.Errorf("connection %s is not available: %s", name, i.Message)
	}
	out := cmd.OutOrStdout()
	if install {
		if info, err := connections.TypeInfoFor(c.Type); err == nil {
			for _, e := range info.Extensions {
				fmt.Fprintf(cmd.ErrOrStderr(), "installing the DuckDB %q extension...\n", e)
				if err := o.Sess.InstallExtension(ctx, e); err != nil {
					return errors.New(cliRedactor.Redact(err.Error()))
				}
			}
		}
		if err := installMissing(ctx, cmd, o.Sess, name); err != nil {
			return err
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = o.Sess.Connect(cctx, name)
	if relaunch, reason := o.Sess.RelaunchRequired(); relaunch {
		return fmt.Errorf("%s: %s", name, reason)
	}
	if err != nil {
		st := connStatus(o.Sess, name)
		msg := cliRedactor.Redact(err.Error())
		if st.Kind == session.StatusExtensionMissing {
			msg += "\n(re-run with --install to download it)"
		}
		return errors.New(msg)
	}
	if info, _ := connections.TypeInfoFor(c.Type); !info.Capabilities.Catalog && c.Type != "duckdb" {
		switch c.Type {
		case "iceberg":
			fmt.Fprintf(out, "%s: ready (read the table with iceberg_scan('%s'))\n", name, c.ConnectionDef.Path)
		case "delta":
			fmt.Fprintf(out, "%s: ready (read the table with delta_scan('%s'))\n", name, c.ConnectionDef.Path)
		default:
			fmt.Fprintf(out, "%s: connected (storage scope %s; read it with read_parquet('%s...'))\n", name, c.Scope, c.ConnectionDef.Scope)
		}
		return nil
	}
	tables, terr := o.Sess.ListTables(cctx, name, "")
	if terr != nil {
		fmt.Fprintf(out, "%s: connected\n", name)
		return nil
	}
	fmt.Fprintf(out, "%s: connected, %d tables in the default schema\n", name, len(tables))
	for i, t := range tables {
		if i >= 20 {
			fmt.Fprintf(out, "  ... and %d more\n", len(tables)-20)
			break
		}
		fmt.Fprintf(out, "  %s\n", t.Ref().String())
	}
	return nil
}

// installMissing installs, one at a time, the extensions the connection's own
// status asks for (a lake needs more than its type's base list: sqlite for a
// SQLite catalog, httpfs for a URL), until none is missing.
func installMissing(ctx context.Context, cmd *cobra.Command, sess session.Session, name string) error {
	const maxExtensions = 8
	for i := 0; i < maxExtensions; i++ {
		sess.Preflight(ctx, name)
		st := connStatus(sess, name)
		if st.Kind != session.StatusExtensionMissing || st.Extension == "" {
			return nil
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "installing the DuckDB %q extension...\n", st.Extension)
		if err := sess.InstallExtension(ctx, st.Extension); err != nil {
			return errors.New(cliRedactor.Redact(err.Error()))
		}
	}
	return nil
}

// isMotherDuck reports whether the named connection is a MotherDuck one, which
// cannot be attached by a read-only open.
func isMotherDuck(cmd *cobra.Command, name string) bool {
	ws, err := openWS(cmd)
	if err != nil {
		return false
	}
	defer ws.Close() //nolint:errcheck
	c, ok := findConn(ws, name)
	return ok && c.Type == "motherduck"
}

func connStatus(s session.Session, name string) session.Status {
	st, _ := s.Status(name)
	return st
}

// --- rm / move ---

var connRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a connection and its saved credentials",
	Long: `Remove the definition from its scope (--scope, default: the one that is in
effect) and, when no other definition still uses them, clear its keychain
entry and sign-in choice. Data in the connection itself is untouched.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runConnRm,
	SilenceUsage: true,
}

func runConnRm(cmd *cobra.Command, args []string) error {
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close() //nolint:errcheck
	c, ok := findConn(ws, args[0])
	if !ok {
		return fmt.Errorf("unknown connection %q", args[0])
	}
	scope := c.Scope
	if s := flagString(cmd, "scope"); s != "" {
		if scope, err = scopeFlag(s); err != nil {
			return err
		}
	}
	if err := ws.RemoveConnection(cmdCtx(cmd), scope, args[0]); err != nil {
		return errors.New(cliRedactor.Redact(err.Error()))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Removed %s connection %s.\n", scope, args[0])
	return nil
}

var connMoveCmd = &cobra.Command{
	Use:   "move <name> --to <scope>",
	Short: "Move a connection between scopes (user, shared, local)",
	Long: `Change where a definition lives. The saved credential stays bound to the
workspace id, the name and the fingerprint, so nothing needs re-entering; the
destination's rules apply (a shared connection may not use an absolute path or
${ENV}).`,
	Args:         cobra.ExactArgs(1),
	RunE:         runConnMove,
	SilenceUsage: true,
}

func runConnMove(cmd *cobra.Command, args []string) error {
	to, err := scopeFlag(flagString(cmd, "to"))
	if err != nil {
		return err
	}
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close() //nolint:errcheck
	c, ok := findConn(ws, args[0])
	if !ok {
		return fmt.Errorf("unknown connection %q", args[0])
	}
	from := c.Scope
	if s := flagString(cmd, "from"); s != "" {
		if from, err = scopeFlag(s); err != nil {
			return err
		}
	}
	if err := ws.MoveConnection(cmdCtx(cmd), args[0], from, to); err != nil {
		return errors.New(cliRedactor.Redact(err.Error()))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Moved %s from %s to %s.\n", args[0], from, to)
	if w := workspace.SharedMetadataWarning(c.ConnectionDef, to); w != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Warning: %s\n", w)
	}
	return nil
}

// --- credentials ---
// --- hidden subprocess entry for the MotherDuck test ---

var connChildCmd = &cobra.Command{
	Use:    connections.ChildCommand,
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return connections.RunChild(cmdCtx(cmd), cmd.InOrStdin(), cmd.OutOrStdout())
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	f := connAddCmd.Flags()
	f.String("type", "", "connection type: "+strings.Join(connections.AllTypeNames(), ", "))
	f.String("scope", "local", "where to keep the definition: local (this computer), shared (slaw.yaml) or user (all workspaces)")
	f.String("path", "", "file path (duckdb, sqlite; ducklake: catalog file or storage URL; iceberg, delta: the table folder or storage URL)")
	f.String("host", "", "host (postgres, mysql)")
	f.Int("port", 0, "port (postgres, mysql; default 5432 / 3306)")
	f.String("database", "", "database (postgres, mysql, motherduck)")
	f.String("region", "", "region (s3, gcs, r2)")
	f.String("preset", "", "fill an s3 connection's endpoint options for a store: "+strings.Join(connections.PresetIDs(), ", ")+" (--option overrides)")
	f.String("prefix", "", "storage scope: a URL prefix such as s3://bucket/path/ (s3, gcs, r2, azure)")
	f.StringArray("option", nil, "type option key=value (postgres: sslmode; mysql: ssl_mode; s3: endpoint, url_style, use_ssl; r2: account_id; azure: sign_in, account_name, tenant_id; ducklake: data_path, catalog [duckdb|sqlite]); repeatable")
	f.Bool("read-only", true, "attach read-only (--read-only=false to allow writes)")
	f.String("sign-in", "", "how you sign in: keychain (default), prompt, env:PREFIX or (s3) aws-sso:PROFILE")
	addConnWorkspaceFlag(connAddCmd)

	addConnWorkspaceFlag(connListCmd)
	connListCmd.Flags().Bool("json", false, "output as JSON")

	connTestCmd.Flags().Bool("install", false, "download a missing DuckDB extension first")
	addConnWorkspaceFlag(connTestCmd)
	connConnectCmd.Flags().Bool("install", false, "download a missing DuckDB extension first")
	addConnWorkspaceFlag(connConnectCmd)

	connRmCmd.Flags().String("scope", "", "scope to remove from (default: the one in effect)")
	addConnWorkspaceFlag(connRmCmd)
	connMoveCmd.Flags().String("to", "", "destination scope: user, shared or local")
	connMoveCmd.Flags().String("from", "", "source scope (default: the one in effect)")
	addConnWorkspaceFlag(connMoveCmd)

	connCredsSetCmd.Flags().String("sign-in", "", "sign-in method: keychain, prompt, env:PREFIX or (s3) aws-sso:PROFILE")
	connCredsSetCmd.Flags().Bool("confirm-rebind", false, "use an env: or aws-sso: sign-in for a server other than the one it was chosen for")
	addConnWorkspaceFlag(connCredsSetCmd)
	addConnWorkspaceFlag(connCredsClearCmd)
	addConnWorkspaceFlag(connCredsStatusCmd)
	addConnWorkspaceFlag(connCredsSignInCmd)
	connCredentialsCmd.AddCommand(connCredsSetCmd, connCredsClearCmd, connCredsStatusCmd, connCredsSignInCmd)

	connCmd.AddCommand(connAddCmd, connListCmd, connTestCmd, connConnectCmd, connRmCmd, connMoveCmd, connCredentialsCmd)
	rootCmd.AddCommand(connCmd, connChildCmd)
}

// isEnvCmd is true for `workspace env ...`, which manages the decision itself.
func isEnvCmd(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c == workspaceEnvCmd {
			return true
		}
	}
	return false
}

// checkSignInFor is workspace.CheckSignIn for a definition that is not saved
// yet: the sign-in must be well formed, one the type takes, and (aws-sso) the
// endpoint must be AWS over TLS.
func checkSignInFor(ws workspace.Workspace, scope workspace.Scope, def workspace.ConnectionDef, signIn string) error {
	resolved, err := def.Resolve(nil, scope)
	if err != nil {
		return err
	}
	return workspace.CheckSignIn(ws.Resolver(), resolved, signIn)
}
