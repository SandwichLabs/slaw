package cmd

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

var receiptsShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Print one receipt: what was asked and what it touched",
	Long: `Print a receipt in the sections Studio's Evidence drawer shows: a summary, the
query (as written and as run, with its parameters), the connections and the
snapshot each was read at, the result's shape and fingerprint, and when, by
whom and from where it ran. A receipt never holds a result value. With --json,
print the receipt record itself. See ` + docRef("receipts"),
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runReceiptsShow,
}

var receiptsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the newest receipts",
	Long: `List receipts, newest first, one line each. --surface keeps only the runs from
one place (query_editor, console, cli_query, render, notebook_cell, a2ui,
artifact, metric, assertion, watch, agent_run_query, agent_saved_query,
agent_preview); --limit says how many (default 50, at most 500).`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runReceiptsList,
}

func init() {
	receiptsShowCmd.Flags().Bool("json", false, "print the receipt as JSON")
	receiptsListCmd.Flags().Bool("json", false, "output as JSON")
	receiptsListCmd.Flags().String("surface", "", "only receipts from this surface")
	receiptsListCmd.Flags().Int("limit", receipts.DefaultListLimit, "how many receipts to list")
	for _, c := range []*cobra.Command{receiptsShowCmd, receiptsListCmd} {
		addWorkspaceFlag(c)
	}
	receiptsCmd.AddCommand(receiptsShowCmd, receiptsListCmd)
}

// receiptStoreOf is the open workspace's receipts store, or an error that says
// none are kept.
func receiptStoreOf(ws workspace.Workspace) (receipts.Store, error) {
	rec := ws.Receipts()
	if rec == nil {
		return nil, errors.New("receipts are not being kept for this workspace (see the warning above)")
	}
	return rec.Store(), nil
}

func runReceiptsShow(cmd *cobra.Command, args []string) error {
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close()
	st, err := receiptStoreOf(ws)
	if err != nil {
		return err
	}
	r, err := st.Get(strings.TrimSpace(args[0]))
	if err != nil {
		return err
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return encodeJSON(cmd.OutOrStdout(), r)
	}
	printReceipt(cmd.OutOrStdout(), r)
	return nil
}

func runReceiptsList(cmd *cobra.Command, _ []string) error {
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close()
	st, err := receiptStoreOf(ws)
	if err != nil {
		return err
	}
	surface, _ := cmd.Flags().GetString("surface")
	if s := receipts.Surface(surface); s != "" && !s.Valid() {
		return fmt.Errorf("unknown surface %q", surface)
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit <= 0 {
		return fmt.Errorf("--limit must be positive, not %d", limit)
	}
	rows, err := st.List(receipts.ListFilter{Surface: receipts.Surface(surface), Limit: limit})
	if err != nil {
		return fmt.Errorf("failed to list receipts: %w", err)
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		if rows == nil {
			rows = []receipts.Summary{}
		}
		return encodeJSON(cmd.OutOrStdout(), rows)
	}
	printReceiptList(cmd.OutOrStdout(), rows)
	return nil
}

func printReceiptList(w io.Writer, rows []receipts.Summary) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No receipts.")
		return
	}
	for _, s := range rows {
		sql := s.SQL
		if s.SQLWithheld {
			sql = "(statement withheld)"
		}
		fmt.Fprintf(w, "%s  %s  %-17s %s  %d rows  %s\n", s.ID, s.At.Local().Format("2006-01-02 15:04:05"), string(s.Surface), refOrDash(s.Source.Ref), s.Rows.Returned, sql)
	}
}

func refOrDash(ref string) string {
	if ref == "" {
		return "-"
	}
	return ref
}

// printReceipt writes a receipt as the drawer's sections: Summary, Query,
// Connections, Result, Run, then the id.
func printReceipt(w io.Writer, r receipts.Receipt) {
	section := func(name string) { fmt.Fprintf(w, "\n%s\n", strings.ToUpper(name)) }
	fmt.Fprintf(w, "RECEIPT %s\n", r.ID)

	section("Summary")
	fmt.Fprintln(w, receiptSummary(r))
	fmt.Fprintf(w, "%s; %s\n", replayWord(r), verdictWord(r.Verdict))

	section("Query")
	printReceiptQuery(w, r)

	section("Connections")
	if len(r.Connections) == 0 {
		fmt.Fprintln(w, "none recorded")
	}
	for _, c := range r.Connections {
		printReceiptConnection(w, c)
	}

	section("Result")
	printReceiptResult(w, r)

	section("Run")
	fmt.Fprintf(w, "When  %s (%s)\n", r.At.Local().Format("2006-01-02 15:04:05 MST"), durationText(r.DurationMS))
	fmt.Fprintf(w, "By    %s\n", receiptBy(r))
	fmt.Fprintf(w, "From  %s\n", receiptFrom(r))

	fmt.Fprintln(w, "\nKept on this computer, never in the folder.")
}

// receiptSummary is the one sentence of the drawer's Summary section.
func receiptSummary(r receipts.Receipt) string {
	if len(r.Connections) == 0 {
		return "One query, on no connection that was recorded."
	}
	names := make([]string, 0, len(r.Connections))
	for _, c := range r.Connections {
		names = append(names, c.Name)
	}
	s := "One query on " + strings.Join(names, ", ")
	if len(r.Connections) == 1 {
		if sn := r.Connections[0].Snapshot; sn != nil && sn.Label == receipts.LabelReplayable {
			return s + ", read at snapshot " + sn.Value + ". Run it at that snapshot and you read the same data."
		}
	}
	return s + "."
}

// replayWord says whether the same data can be read again: "Replayable" only
// when every connection has a snapshot that can be read at, "Live only" when any
// snapshot is a live fact (where the source was when it answered), otherwise
// "Not recorded". It is the drawer's rule (lib/receiptText.ts replayState).
func replayWord(r receipts.Receipt) string {
	if len(r.Connections) == 0 {
		return "Not recorded"
	}
	replayable := true
	for _, c := range r.Connections {
		switch {
		case c.Snapshot != nil && c.Snapshot.Label == receipts.LabelLiveOnly:
			return "Live only"
		case c.Snapshot == nil || c.Snapshot.Label != receipts.LabelReplayable:
			replayable = false
		}
	}
	if replayable {
		return "Replayable"
	}
	return "Not recorded"
}

func verdictWord(v receipts.Verdict) string {
	if v == receipts.VerdictRead {
		return "Read-only check passed"
	}
	return "Your own query"
}

func printReceiptQuery(w io.Writer, r receipts.Receipt) {
	fmt.Fprintln(w, "As written:")
	fmt.Fprintln(w, indent(textOrWithheld(r.SQL, r.SQLWithheld, r.SQL == "")))
	fmt.Fprintln(w, "As run:")
	fmt.Fprintln(w, indent(textOrWithheld(r.RunSQL, r.RunSQLWithheld, r.RunSQL == "")))
	switch {
	case r.ParamsWithheld:
		fmt.Fprintf(w, "Parameters: %s (values not kept; hash %s)\n", paramNames(r), short(r.ParamsHash))
	case len(r.Params) > 0:
		fmt.Fprintln(w, "Parameters:")
		names := make([]string, 0, len(r.Params))
		for n := range r.Params {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := r.Params[n]
			v := p.Value
			switch {
			case p.Withheld:
				v = "(withheld)"
			case p.Truncated:
				v += "…"
			}
			fmt.Fprintf(w, "  $%s %s\n", n, v)
		}
	}
	if r.Source.Ref != "" {
		fmt.Fprintf(w, "From %s\n", r.Source.Ref)
	}
}

func paramNames(r receipts.Receipt) string {
	names := make([]string, 0, len(r.Params))
	for n := range r.Params {
		names = append(names, "$"+n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func textOrWithheld(text string, withheld, empty bool) string {
	switch {
	case withheld:
		return "(not kept: it looked like it held a credential)"
	case empty:
		return "(none)"
	}
	return text
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ")
}

func printReceiptConnection(w io.Writer, c receipts.Connection) {
	ro := ""
	if c.ReadOnly {
		ro = ", read-only"
	}
	fmt.Fprintf(w, "%s  %s, %s%s\n", c.Name, c.Type, strings.ReplaceAll(string(c.Status), "_", " "), ro)
	sn := c.Snapshot
	if sn == nil {
		fmt.Fprintln(w, "  Snapshot: none (the connection was not attached)")
		return
	}
	fmt.Fprintf(w, "  Snapshot: %s %s\n", sn.Fact, sn.Value)
	switch sn.Label {
	case receipts.LabelReplayable:
		fmt.Fprintln(w, "  Replayable: recorded right after the run, current when the run finished.")
	case receipts.LabelLiveOnly:
		fmt.Fprintln(w, "  Live only. Where the source was when it answered; you can't read that moment again.")
	default:
		fmt.Fprintf(w, "  Not recorded: %s.\n", sn.Value)
	}
}

func printReceiptResult(w io.Writer, r receipts.Receipt) {
	of := ""
	if r.Rows.Total != r.Rows.Returned {
		of = fmt.Sprintf(" of %d", r.Rows.Total)
	}
	trunc := ""
	if r.Rows.Truncated {
		trunc = " (truncated)"
	}
	fmt.Fprintf(w, "Rows: %d returned%s%s\n", r.Rows.Returned, of, trunc)
	fmt.Fprintf(w, "Columns: %d\n", len(r.Columns))
	for _, c := range r.Columns {
		name := c.Name
		if c.Withheld {
			name = "(name withheld)"
		}
		fmt.Fprintf(w, "  %s  %s\n", name, c.Type)
	}
	fmt.Fprintf(w, "Fingerprint: %s\n", short(r.ResultHMAC))
	fmt.Fprintln(w, "The receipt keeps a fingerprint of the rows, never the rows.")
}

// short is a hash as 6 hex digits, an ellipsis and the last 4.
func short(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:6] + "…" + h[len(h)-4:]
}

func durationText(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.2f s", float64(ms)/1000)
}

// receiptBy says who ran a statement, in the drawer's words (lib/receiptText.ts).
func receiptBy(r receipts.Receipt) string {
	if r.Actor == receipts.ActorAgent {
		return "The agent (" + strings.TrimPrefix(string(r.Surface), "agent_") + ")"
	}
	switch r.Surface {
	case receipts.SurfaceQueryEditor:
		return "You, in the Query Explorer"
	case receipts.SurfaceConsole:
		return "You, in the console"
	case receipts.SurfaceCLIQuery:
		return "You, with slaw query"
	case receipts.SurfaceRender:
		return "You, opening a page"
	case receipts.SurfaceNotebookCell:
		return "You, opening the notebook"
	case receipts.SurfaceA2UI:
		return "You, opening the page"
	case receipts.SurfaceArtifact:
		return "You, opening the artifact"
	case receipts.SurfaceMetric:
		return "You, trying a metric"
	case receipts.SurfaceAssertion:
		return "You, checking a definition"
	case receipts.SurfaceWatch:
		return "You, checking a watch"
	}
	return "You, from " + strings.ReplaceAll(string(r.Surface), "_", " ")
}

func receiptFrom(r receipts.Receipt) string {
	if r.Source.Ref != "" {
		return r.Source.Ref
	}
	switch r.Source.Kind {
	case receipts.SourceSQL:
		return "SQL typed in place"
	case "":
		return "no file (" + string(r.Surface) + ")"
	}
	return string(r.Source.Kind)
}
