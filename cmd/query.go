package cmd

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/SandwichLabs/slaw/internal/editor"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/usersql"
)

var queryCmd = &cobra.Command{
	Use:   `query ["<sql>"]`,
	Short: "Run one SQL statement against a workspace",
	Long: `Run one SQL statement through the workspace's DuckDB session and print the
result. Unqualified names resolve against the workspace's default connection;
-c names another one, and any connection can also be named in the SQL
(sales_pg.public.orders).

By default this process takes ownership of the workspace's database (the hub
lock), so a statement that writes is allowed on a connection that is not
read-only. If Slaw or "slaw mcp serve" already has the workspace open,
this says who. The owner keeps the workspace's local DuckDB files locked for
writing and DuckDB cannot read a file a writer holds, so --read-only (read
without taking ownership, every connection attached read-only, no COPY ... TO)
only reaches the workspace's other connections, such as MotherDuck or Postgres.

This is you acting directly, not the Slaw assistant, so SQL runs as written. As a
courtesy, ATTACH, DETACH, INSTALL, LOAD, SET and CREATE SECRET are refused with
an explanation (connections come from the workspace, credentials from your
keychain). That is a check for common slips, not a security boundary.

--saved ID runs a saved query (queries/ID.sql) instead of a statement you give:
its SQL runs as saved, on its connection unless -c names another. A saved query
may declare $name parameters; -p name=value (repeatable) sets one, typed by its
declaration, and a parameter you leave out takes its default. A value is bound,
never written into the SQL. Without --saved, a $name in the statement is an
error, as before. A saved query is a plain file whose first line is a header
comment, and the SQL follows it:

  -- slaw: {"title":"Top customers","description":"...","connection":"prod_pg","parameters":[{"name":"n","type":"integer","default":"10"}]}
  SELECT * FROM orders ORDER BY total DESC LIMIT $n

Run it with: slaw query --saved top_customers -p n=5

Pass "-" as the statement to read it from standard input. --edit (-e) opens
the statement (or an empty buffer) in $VISUAL or $EDITOR first, as psql's \e
does, and runs what you save; quitting the editor with an error (":cq") runs
nothing. The buffer lives under .slaw-local/edit/ so an editor running
"slaw lsp" completes connections, tables and columns in it. Results are capped
at --limit rows (default 1000, 0 for no cap) and the cap is reported on
standard error.`,
	Args:         cobra.MaximumNArgs(1),
	RunE:         runQuery,
	SilenceUsage: true,
}

func init() {
	queryCmd.Flags().StringP("connection", "c", "", "connection whose catalog unqualified names use (default: the workspace's default connection)")
	queryCmd.Flags().String("format", "table", "output format: table, csv, json")
	queryCmd.Flags().Int("limit", defaultQueryLimit, "maximum rows to print (0 = no cap)")
	queryCmd.Flags().BoolP("edit", "e", false, "edit the statement in $VISUAL or $EDITOR before running it")
	queryCmd.Flags().String("saved", "", "run the saved query with this id instead of a statement")
	queryCmd.Flags().StringArrayP("param", "p", nil, "a saved query's parameter, name=value (repeatable; needs --saved)")
	queryCmd.Flags().Bool("read-only", false, "read without taking ownership of the workspace; every connection is attached read-only")
	addWorkspaceFlag(queryCmd)
	rootCmd.AddCommand(queryCmd)
}

const defaultQueryLimit = 1000

func runQuery(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("workspace")
	conn, _ := cmd.Flags().GetString("connection")
	format, _ := cmd.Flags().GetString("format")
	limit, _ := cmd.Flags().GetInt("limit")
	readOnly, _ := cmd.Flags().GetBool("read-only")
	errW := cmd.ErrOrStderr()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if err := validateFormat(format); err != nil {
		return err
	}
	if limit < 0 {
		return fmt.Errorf("--limit must be 0 or more")
	}
	saved, _ := cmd.Flags().GetString("saved")
	pairs, _ := cmd.Flags().GetStringArray("param")
	if err := checkSavedFlags(cmd, saved, pairs, args); err != nil {
		return err
	}
	var sqlText string
	if saved == "" {
		var err error
		sqlText, err = queryText(cmd, dir, args)
		if errors.Is(err, errEditUnchanged) {
			fmt.Fprintln(errW, "the statement was not changed; nothing was run")
			return nil
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(sqlText) == "" {
			return fmt.Errorf("no SQL given")
		}
		if err := usersql.Check(sqlText, readOnly); err != nil {
			return err
		}
	}

	o, err := openWorkspace(ctx, dir, readOnly, true, "slaw query", errW)
	if err != nil {
		return err
	}
	defer o.Close()

	var params map[string]any
	if saved != "" {
		var q *queries.Query
		if q, params, err = bindSaved(ctx, o, saved, pairs); err != nil {
			return err
		}
		sqlText = q.SQL
		if conn == "" {
			conn = q.Connection
		}
		if err := usersql.Check(sqlText, readOnly); err != nil {
			return err
		}
	}

	written := sqlText
	if sqlText, params, err = expandMetrics(ctx, o, sqlText, conn, params, readOnly, errW); err != nil {
		return err
	}

	target := conn
	if target == "" {
		target = o.WS.DefaultConnection()
	} else if _, ok := o.knownConnection(target); !ok {
		return fmt.Errorf("unknown connection %q; this workspace has: %s", target, connectionNames(o))
	}
	if err := o.requireConnection(target, errW); err != nil {
		return err
	}

	rctx := receipts.WithIntent(ctx, cliIntent(written, saved, params))
	rs, total, err := o.Eng.ExecuteCappedIn(rctx, conn, sqlText, params, limit)
	if err != nil {
		return friendlyQueryError(err, readOnly)
	}
	// The receipt covers the rows the run kept (--limit), the true count known.
	_, _ = receipts.Finish(rctx, o.WS.Receipts(), rs.Rows, total > rs.RowCount(), total)
	if err := writeResult(cmd.OutOrStdout(), rs, format); err != nil {
		return err
	}
	if limit > 0 && total > rs.RowCount() {
		fmt.Fprintf(errW, "showing the first %d of %d rows (--limit 0 for all)\n", rs.RowCount(), total)
	}
	return nil
}

// cliIntent is the receipt intent of a `slaw query` run: the statement as
// written (or the saved query it ran), the values bound, and no policy verdict
// (the CLI's own checks are a courtesy, not the agent policy).
func cliIntent(written, saved string, params map[string]any) receipts.Intent {
	src := receipts.Source{Kind: receipts.SourceSQL}
	if saved != "" {
		src = receipts.Source{Kind: receipts.SourceSavedQuery, Ref: "queries/" + saved + ".sql"}
	}
	return receipts.Intent{
		Surface: receipts.SurfaceCLIQuery, Actor: receipts.ActorPerson, Source: src,
		SQL: written, Params: receipts.ParamStrings(params), Verdict: receipts.VerdictUnchecked,
	}
}

// expandMetrics prepares the statement as a person runs it
// (hub.PrepareForPerson: metric('name', by => ...) calls replaced by the
// metric's saved query, then the same courtesy check on the expanded text as on
// the typed one). It says what it expanded on standard error, so the SQL that
// ran is never a surprise.
func expandMetrics(ctx context.Context, o *openedWorkspace, sqlText, conn string, params map[string]any, readOnly bool, errW io.Writer) (string, map[string]any, error) {
	exp, err := hub.PrepareForPerson(ctx, o.WS, o.Sess, nil, hub.PersonRun{
		SQL: sqlText, Connection: conn, Params: params, Guard: cliGuard(readOnly),
	})
	if err != nil {
		return "", nil, err
	}
	if len(exp.Uses) == 0 {
		return sqlText, params, nil
	}
	names := make([]string, len(exp.Uses))
	for i, u := range exp.Uses {
		names[i] = u.Name
	}
	fmt.Fprintf(errW, "expanded %d metric%s: %s\n", len(names), plural(len(names)), strings.Join(names, ", "))
	return exp.SQL, exp.Params, nil
}

// cliGuard is the CLI's courtesy check (usersql.Check) as a hub.PersonGuard.
func cliGuard(readOnly bool) hub.PersonGuard {
	return func(_ context.Context, text string) error { return usersql.Check(text, readOnly) }
}

// checkSavedFlags refuses the combinations --saved and -p do not allow, before
// the workspace is opened.
func checkSavedFlags(cmd *cobra.Command, saved string, pairs, args []string) error {
	if saved == "" {
		if len(pairs) > 0 {
			return fmt.Errorf("-p/--param sets a saved query's parameter: give --saved ID")
		}
		return nil
	}
	if edit, _ := cmd.Flags().GetBool("edit"); edit || len(args) > 0 {
		return fmt.Errorf("--saved runs the saved query as it is: give no statement and no --edit")
	}
	return nil
}

// bindSaved loads a saved query and binds the -p values to its declared
// parameters (queries.Bind, the binder the agent and Studio use). The statement
// text comes back unchanged; the values only ever travel as bound params.
func bindSaved(ctx context.Context, o *openedWorkspace, id string, pairs []string) (*queries.Query, map[string]any, error) {
	q, err := o.WS.Queries().Get(ctx, id)
	if err != nil {
		if errors.Is(err, queries.ErrNotFound) {
			return nil, nil, fmt.Errorf("there is no saved query %q (see the queries/ folder)", id)
		}
		return nil, nil, fmt.Errorf("failed to read saved query %q: %w", id, err)
	}
	if q.Error != "" {
		return nil, nil, fmt.Errorf("saved query %q cannot be run: %s", id, q.Error)
	}
	raw, err := splitParamPairs(pairs)
	if err != nil {
		return nil, nil, err
	}
	values, err := queries.ParseValues(q, raw)
	if err != nil {
		return nil, nil, err
	}
	params, err := queries.Bind(q, values, queries.BindOptions{Now: time.Now()})
	if err != nil {
		return nil, nil, fmt.Errorf("saved query %q: %w", id, err)
	}
	return q, params, nil
}

// splitParamPairs reads name=value pairs; the value may itself contain "=".
func splitParamPairs(pairs []string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		name, val, ok := strings.Cut(p, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("-p expects name=value (got %q)", p)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("parameter %q is given twice", name)
		}
		out[name] = val
	}
	return out, nil
}

// queryText is the statement to run: the argument, standard input for "-",
// then, with --edit, whatever the person saves in their editor.
func queryText(cmd *cobra.Command, dir string, args []string) (string, error) {
	edit, _ := cmd.Flags().GetBool("edit")
	text := ""
	if len(args) == 1 {
		text = args[0]
	} else if !edit {
		return "", fmt.Errorf("give the statement to run, \"-\" to read it from standard input, or --edit")
	}
	if text == "-" {
		if edit {
			return "", fmt.Errorf("--edit reads the statement from your editor, not standard input")
		}
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("failed to read the statement from standard input: %w", err)
		}
		return string(b), nil
	}
	if !edit {
		return text, nil
	}
	edited, err := editQuery(dir, text)
	if err == nil && strings.TrimSpace(text) != "" && strings.TrimSpace(edited) == strings.TrimSpace(text) {
		return "", errEditUnchanged
	}
	return edited, err
}

// errEditUnchanged says the editor saved the statement as it was given: like
// the TUI, an unchanged buffer runs nothing.
var errEditUnchanged = errors.New("statement unchanged")

// editQuery opens text in the person's editor on a scratch buffer under the
// workspace, so the editor's language server finds slaw.yaml.
func editQuery(dir, text string) (string, error) {
	argv, err := editor.Resolve(os.Getenv)
	if err != nil {
		return "", err
	}
	root, _ := hub.ResolveRoot(dir, "") // no workspace: the open below says so
	edited, ok, err := editor.Edit(argv, root, text, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("editor exited with an error; nothing was run")
	}
	return edited, nil
}

func connectionNames(o *openedWorkspace) string {
	conns := o.WS.Connections()
	names := make([]string, 0, len(conns))
	for _, c := range conns {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// friendlyQueryError adds the one hint that is always right: a write against a
// read-only attach.
func friendlyQueryError(err error, readOnly bool) error {
	if engine.IsReadOnlyViolation(err) {
		if readOnly {
			return fmt.Errorf("%w\n(--read-only attaches every connection read-only)", err)
		}
		return fmt.Errorf("%w\n(that connection is read-only; set `read_only: false` on it in slaw.yaml or your connections file to allow writes)", err)
	}
	return err
}

func validateFormat(f string) error {
	switch f {
	case "table", "csv", "json":
		return nil
	}
	return fmt.Errorf("unsupported format %q: use table, csv or json", f)
}

// writeResult prints a result set as an aligned table, CSV or a JSON array of
// objects (column order preserved).
func writeResult(out io.Writer, rs *engine.ResultSet, format string) error {
	switch format {
	case "csv":
		return writeCSV(out, rs)
	case "json":
		return writeJSON(out, rs)
	default:
		return writeTable(out, rs)
	}
}

func writeCSV(out io.Writer, rs *engine.ResultSet) error {
	w := csv.NewWriter(out)
	names := make([]string, len(rs.Columns))
	for i, c := range rs.Columns {
		names[i] = c.Name
	}
	if err := w.Write(names); err != nil {
		return fmt.Errorf("failed to write CSV: %w", err)
	}
	for _, row := range rs.Rows {
		rec := make([]string, len(rs.Columns))
		for i := range rec {
			if i < len(row) && row[i] != nil {
				rec[i] = cellText(row[i])
			}
		}
		if err := w.Write(rec); err != nil {
			return fmt.Errorf("failed to write CSV: %w", err)
		}
	}
	w.Flush()
	return w.Error()
}

// writeJSON prints the rows as an array of objects with the columns in order.
func writeJSON(out io.Writer, rs *engine.ResultSet) error {
	var b bytes.Buffer
	b.WriteString("[")
	for ri, row := range rs.Rows {
		if ri > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n  ")
		writeJSONRow(&b, rs.Columns, row)
	}
	if len(rs.Rows) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	_, err := out.Write(b.Bytes())
	return err
}

func writeJSONRow(b *bytes.Buffer, cols []engine.Column, row []any) {
	b.WriteString("{")
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		k, _ := json.Marshal(c.Name)
		var v any
		if i < len(row) {
			v = row[i]
		}
		vb, err := json.Marshal(v)
		if err != nil {
			vb, _ = json.Marshal(cellText(v))
		}
		b.Write(k)
		b.WriteString(": ")
		b.Write(vb)
	}
	b.WriteString("}")
}

func cellText(v any) string { return engine.CellText(v, "") }

func writeTable(out io.Writer, rs *engine.ResultSet) error {
	if len(rs.Columns) == 0 {
		fmt.Fprintln(out, "OK")
		return nil
	}
	widths := make([]int, len(rs.Columns))
	cells := make([][]string, len(rs.Rows))
	for i, c := range rs.Columns {
		widths[i] = len([]rune(cleanCell(c.Name)))
	}
	for ri, row := range rs.Rows {
		cells[ri] = make([]string, len(rs.Columns))
		for i := range rs.Columns {
			s := "NULL"
			if i < len(row) && row[i] != nil {
				s = cleanCell(cellText(row[i]))
			}
			cells[ri][i] = s
			if n := len([]rune(s)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	line := func(vals []string) {
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = v + strings.Repeat(" ", widths[i]-len([]rune(v)))
		}
		fmt.Fprintln(out, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	names := make([]string, len(rs.Columns))
	seps := make([]string, len(rs.Columns))
	for i, c := range rs.Columns {
		names[i] = cleanCell(c.Name)
		seps[i] = strings.Repeat("-", widths[i])
	}
	line(names)
	line(seps)
	for _, r := range cells {
		line(r)
	}
	fmt.Fprintf(out, "(%d row%s)\n", len(rs.Rows), plural(len(rs.Rows)))
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// cleanCell keeps a value on one line and strips terminal control characters
// so data in a table cannot move the cursor or recolor the screen.
func cleanCell(s string) string {
	return engine.TerminalText(s, engine.TerminalOpts{StripC1: true})
}
