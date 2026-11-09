package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/semantics/assert"
	"github.com/SandwichLabs/slaw/internal/strictyaml"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// Exit statuses of `slaw semantics check` and `profile`: 0 ok, 1 a check
// failed (the data is wrong), 2 anything that stopped the run from saying
// whether it is: a check that could not run, a typo (unknown kind or name, a
// bad flag), no workspace, a join file that changed under a profile. A script
// can tell "fix the data" (1) from "fix the command or the setup" (2).
const (
	checkExitFail  = 1
	checkExitError = 2
)

// couldNotRun marks err as a status-2 failure, keeping the message and chain.
func couldNotRun(err error) error {
	var coded *exitCodeError
	if err == nil || errors.As(err, &coded) {
		return err
	}
	return &exitCodeError{code: checkExitError, msg: err.Error(), err: err}
}

// runsAs2 gives a command's usage, flag and run errors status 2.
func runsAs2(c *cobra.Command, args cobra.PositionalArgs, run func(*cobra.Command, []string) error) {
	c.Args = func(cmd *cobra.Command, a []string) error { return couldNotRun(args(cmd, a)) }
	c.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return couldNotRun(err) })
	c.RunE = func(cmd *cobra.Command, a []string) error { return couldNotRun(run(cmd, a)) }
}

var semanticsCheckCmd = &cobra.Command{
	Use:   "check [entity|metric [name]]",
	Short: "Run the assertions the definitions declare",
	Long: `Run the assertions declared in semantics/: an entity's unique, not_null,
accepted_values, range, freshness and row_count; a metric's non_negative,
not_null and range (checked at the metric's full dimension set) and unique
(its saved query has one row per dimension set). With no argument every entity and metric is checked;
"entity" or "metric" alone checks all of that kind, and a name checks one.

Each entity and metric starts with a compile check: its SQL (an entity's table
and declared columns, a metric's measure and dimensions against its saved
query) is bound against the real connection and never run. Binding may read
file headers or list object names, but no rows are scanned. A definition that
does not compile fails with the database's own message and its assertions are
not run. --compile runs only these checks; it is quick enough for every edit.

Every other check is a read-only count of violating rows, run through the agent
policy's read-only path whatever the sandbox setting, so a definition from a
cloned repository cannot do more than read. Values travel as bound parameters.
A check that cannot run (a connection down, a failed read while binding) is
reported as an error and the others still run. A definition the policy or the
database's binder refuses fails to compile (exit 1), not an error.

Exit status: 0 when every check passes (or none is declared), 1 when any check
fails, 2 when none failed but some could not run, and 2 for a usage or open
error (an unknown name or kind, a bad flag, no workspace). Names and messages
printed here are shown with control characters as <U+XXXX>. See
` + docRef("semantics"),
	SilenceUsage: true,
}

var semanticsProfileCmd = &cobra.Command{
	Use:   "profile <join>",
	Short: "Measure a join against its declared cardinality and record it",
	Long: `Measure how the join's two entities really relate: row counts, the most right
rows sharing a left key (fan-out), and the rows with no partner on either side.
The result is written into the join's file as a measured: block and compared
with the declared cardinality; a contradiction is printed as a warning.

This reads the two tables with a few full scans, so it can take a while on a
large table. It is your action only: no agent tool or MCP method runs it. If the
join file changes while the profile runs, nothing is written. See
` + docRef("semantics"),
	SilenceUsage: true,
}

func init() {
	runsAs2(semanticsCheckCmd, cobra.MaximumNArgs(2), runSemanticsCheck)
	runsAs2(semanticsProfileCmd, cobra.ExactArgs(1), runSemanticsProfile)
	semanticsCheckCmd.Flags().Bool("json", false, "output as JSON")
	semanticsCheckCmd.Flags().Bool("compile", false, "only check that each definition compiles against its connection; no rows are scanned")
	semanticsProfileCmd.Flags().Bool("json", false, "output as JSON")
	addWorkspaceFlag(semanticsCheckCmd)
	addWorkspaceFlag(semanticsProfileCmd)
	semanticsCmd.AddCommand(semanticsCheckCmd, semanticsProfileCmd)
}

// openReadOnlyRun opens the workspace for a run of what its definitions or
// watches declare: read-only (no hub lock, every connection attached read-only), with
// the agent policy's read-only runner. Every statement goes through that
// runner, so the open is policy-gated (hub.OpenOptions.PolicyGatedRuns): a
// connection that cannot be attached read-only at all (MotherDuck) is attached
// as defined instead of being unreachable (#302).
func openReadOnlyRun(cmd *cobra.Command, app string) (*openedWorkspace, workspace.ReadOnlyRunner, error) {
	dir, _ := cmd.Flags().GetString("workspace")
	o, err := cliOpenWith(cmdCtx(cmd), dir, semanticsRunOpenOptions(app), false, cmd.ErrOrStderr())
	if err != nil {
		return nil, nil, err
	}
	r, err := hub.ReadOnlyRunner(o.WS, o.Sess, logger)
	if err != nil {
		o.Close()
		return nil, nil, err
	}
	return o, r, nil
}

// semanticsRunOpenOptions is the open openReadOnlyRun asks for.
func semanticsRunOpenOptions(app string) hub.OpenOptions {
	return hub.OpenOptions{App: app, Mode: hub.ModeReadOnly, PolicyGatedRuns: true}
}

func runSemanticsCheck(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	kind, name := "", ""
	if len(args) > 0 {
		kind = args[0]
	}
	if len(args) > 1 {
		name = args[1]
	}
	compileOnly, _ := cmd.Flags().GetBool("compile")
	o, r, err := openReadOnlyRun(cmd, "slaw semantics check")
	if err != nil {
		return err
	}
	defer o.Close()
	results, err := workspace.RunAssertions(cmdCtx(cmd), o.WS, kind, name, workspace.AssertionRunner(r), time.Now(),
		workspace.AssertionOptions{CompileOnly: compileOnly})
	if err != nil {
		return err
	}
	rows := assert.Rows(results)
	out := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return err
		}
	} else {
		printCheckReport(out, rows)
	}
	return checkExit(rows)
}

func printCheckReport(out io.Writer, rows []assert.Row) {
	if len(rows) == 0 {
		fmt.Fprintln(out, "No entities or metrics defined. Add them under semantics/ (see "+docRef("semantics")+").")
		return
	}
	var pass, fail, errs, dataPass, dataFail int
	hasData := false
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSUBJECT\tCHECK\tDETAIL")
	for _, r := range rows {
		detail := ""
		switch r.Status {
		case assert.StatusPass:
			pass++
			detail = r.Detail
			if r.Assertion == assert.KindCompile {
				detail = "compiles"
			}
		case assert.StatusFail:
			fail++
			if r.Assertion == assert.KindCompile {
				detail = "does not compile: " + strings.Join(strings.Fields(r.Error), " ")
			} else {
				detail = failDetail(r)
			}
		default:
			errs++
			detail = "could not run: " + oneLine(r.Error)
		}
		if r.Assertion != assert.KindCompile {
			hasData = true
			switch r.Status {
			case assert.StatusPass:
				dataPass++
			case assert.StatusFail:
				dataFail++
			}
		}
		check := r.Assertion
		if r.Column != "" {
			check += " " + r.Column
		}
		fmt.Fprintf(tw, "%s\t%s %s\t%s\t%s\n", r.Status, r.SubjectKind, strictyaml.EscapeHidden(r.Subject),
			strictyaml.EscapeHidden(check), strictyaml.EscapeHidden(detail))
	}
	_ = tw.Flush()
	fmt.Fprintf(out, "\n%d passed, %d failed, %d could not run\n", pass, fail, errs)
	switch {
	case errs > 0 && pass+fail == 0:
		fmt.Fprintln(out, "Nothing was verified: no check could run. Fix the errors above and run it again.")
	case errs > 0 && dataPass+dataFail == 0 && hasData:
		fmt.Fprintln(out, "No data check could run, so nothing was checked against the data. Fix the errors above and run it again.")
	}
}

// checkExit is the exit status for a set of results.
func checkExit(rows []assert.Row) error {
	var fail, errs int
	for _, r := range rows {
		switch r.Status {
		case assert.StatusFail:
			fail++
		case assert.StatusError:
			errs++
		}
	}
	switch {
	case fail > 0:
		return &exitCodeError{code: checkExitFail, msg: fmt.Sprintf("%d of %d checks failed", fail, len(rows))}
	case errs > 0:
		return &exitCodeError{code: checkExitError, msg: fmt.Sprintf("%d of %d checks could not run", errs, len(rows))}
	}
	return nil
}

func runSemanticsProfile(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	o, r, err := openReadOnlyRun(cmd, "slaw semantics profile")
	if err != nil {
		return err
	}
	defer o.Close()
	p, err := workspace.ProfileJoin(cmdCtx(cmd), o.WS, args[0], workspace.ReadOnlyRunFunc(r), time.Now())
	if err != nil {
		if errors.Is(err, semantics.ErrConflict) {
			return fmt.Errorf("the join file changed while it was being profiled, so nothing was written; run the profile again: %w", err)
		}
		return err
	}
	out := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}
	m := p.Measured
	name := strictyaml.EscapeHidden(p.Join)
	fmt.Fprintf(out, "join %s (declared %s)\n", name, p.Declared)
	fmt.Fprintf(out, "  left rows            %d\n  right rows           %d\n", m.LeftRows, m.RightRows)
	fmt.Fprintf(out, "  max right per left   %d\n  left orphans         %d\n  right orphans        %d\n", m.MaxFanout, m.LeftOrphans, m.RightOrphans)
	fmt.Fprintf(out, "  verdict              %s\n", m.Verdict)
	for _, w := range p.Warnings {
		fmt.Fprintf(out, "warning: %s\n", strictyaml.EscapeHidden(w))
	}
	fmt.Fprintf(out, "recorded in semantics/joins/%s.yaml (measured:)\n", name)
	return nil
}

// failDetail is the DETAIL of a failed data check: a freshness or row_count
// check names what it saw; every other kind counts its violating rows.
func failDetail(r assert.Row) string {
	switch r.Assertion {
	case semantics.AssertFreshness:
		return "stale: " + r.Detail
	case semantics.AssertRowCount:
		return "too few rows: " + r.Detail
	}
	return fmt.Sprintf("%d violating row%s", r.Violations, plural(int(r.Violations)))
}
