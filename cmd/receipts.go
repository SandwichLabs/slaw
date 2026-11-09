package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/SandwichLabs/slaw/internal/workspace"
)

var receiptsCmd = &cobra.Command{
	Use:   "receipts",
	Short: "Read run receipts, set how long they are kept, delete them",
	Long: `A receipt records that a question was asked and what it touched: the SQL, the
connections, the shape of the result and a fingerprint of it, never a result
value. Receipts stay in .slaw-local/receipts/ on this computer and never go
into git or a .slaw bundle.

"show" and "list" read them, the same sections Studio's Evidence drawer shows.
"settings" shows or changes how long they are kept (your own setting for this
workspace, kept outside the folder); "clear" deletes them all. These mirror
Settings > Workspace > Receipts in Studio.`,
}

var receiptsSettingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Show or change how receipts are kept",
	Long: `With no flag, print the settings in force and what is kept now. With a flag,
save it (the others keep their value) and print the result.

  --keep-params on|off   keep the bound parameter values in a receipt (off keeps
                         only their names and a fingerprint)
  --days N               keep a receipt for N days (1 to 365; default 30)
  --max-count N          keep at most N receipts (100 to 200000; default 20000)
  --max-mb N             keep at most N megabytes (1 to 1024; default 64)

Each number must be positive; pass the word "default" to go back to the default
(0 and negative numbers are refused). A positive value outside its range is moved
to the nearest bound and the result says so. Lowering a limit deletes the oldest
receipts above it before the command returns. The consoles keep no
receipts unless you type .receipts on. See ` + docRef("receipts"),
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runReceiptsSettings,
}

var receiptsClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Delete every receipt of this workspace",
	Long: `Delete every receipt in .slaw-local/receipts/. Settings and the key that
fingerprints results stay. This cannot be undone. It asks first; pass --yes
to skip the question (needed when there is no terminal).`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runReceiptsClear,
}

func init() {
	f := receiptsSettingsCmd.Flags()
	f.String("keep-params", "", "keep bound parameter values: on or off")
	f.String("days", "", "keep a receipt for this many days, or default")
	f.String("max-count", "", "keep at most this many receipts, or default")
	f.String("max-mb", "", "keep at most this many megabytes, or default")
	f.Bool("json", false, "output as JSON")
	receiptsClearCmd.Flags().Bool("yes", false, "delete without asking")
	for _, c := range []*cobra.Command{receiptsSettingsCmd, receiptsClearCmd} {
		addWorkspaceFlag(c)
	}
	receiptsCmd.AddCommand(receiptsSettingsCmd, receiptsClearCmd)
	rootCmd.AddCommand(receiptsCmd)
}

// receiptsSettingsOut is `settings --json`.
type receiptsSettingsOut struct {
	Settings  workspace.ReceiptSettings `json:"settings"`
	Effective workspace.ReceiptSettings `json:"effective"`
	Count     int                       `json:"count"`
	Bytes     int64                     `json:"bytes"`
	Oldest    string                    `json:"oldest,omitempty"`
	Notes     []string                  `json:"notes,omitempty"`
}

func runReceiptsSettings(cmd *cobra.Command, _ []string) error {
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close()
	next, changed, err := receiptsFlagsApplied(cmd, workspace.ReceiptSettingsNow(ws))
	if err != nil {
		return err
	}
	var notes []string
	if changed {
		notes = clampNotes(next)
		if err := workspace.SetLocalReceipts(ws, next); err != nil {
			return err
		}
		notes = append(notes, pruneNow(ws)...)
	}
	out := receiptsSettingsOut{
		Settings:  workspace.ReceiptSettingsNow(ws),
		Effective: workspace.SettingsOf(workspace.ReceiptPolicy(ws.Local())),
		Notes:     notes,
	}
	if rec := ws.Receipts(); rec != nil {
		var oldest time.Time
		out.Count, out.Bytes, oldest = rec.Store().Usage()
		if !oldest.IsZero() {
			out.Oldest = oldest.UTC().Format(time.RFC3339)
		}
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return encodeJSON(cmd.OutOrStdout(), out)
	}
	printReceiptsSettings(cmd.OutOrStdout(), out)
	return nil
}

// receiptsFlagsApplied is cur with each flag the person passed; changed says
// whether any was.
func receiptsFlagsApplied(cmd *cobra.Command, cur workspace.ReceiptSettings) (workspace.ReceiptSettings, bool, error) {
	fl := cmd.Flags()
	changed := false
	if fl.Changed("keep-params") {
		v, _ := fl.GetString("keep-params")
		switch strings.ToLower(v) {
		case "on":
			cur.KeepParams = true
		case "off":
			cur.KeepParams = false
		default:
			return cur, false, fmt.Errorf("--keep-params takes on or off, not %q", v)
		}
		changed = true
	}
	for _, p := range []struct {
		flag string
		to   *int
	}{{"days", &cur.Days}, {"max-count", &cur.MaxCount}, {"max-mb", &cur.MaxMB}} {
		if !fl.Changed(p.flag) {
			continue
		}
		v, _ := fl.GetString(p.flag)
		n, err := parseLimit(v)
		if err != nil {
			return cur, false, fmt.Errorf("--%s: %w", p.flag, err)
		}
		*p.to = n
		changed = true
	}
	return cur, changed, nil
}

// parseLimit reads a limit flag: a positive whole number, or "default" (0 in
// the settings, which means unset). 0 and negatives are refused, so a typo is
// never silently the default.
func parseLimit(v string) (int, error) {
	if strings.EqualFold(strings.TrimSpace(v), "default") {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive whole number; use \"default\" to go back to the default", v)
	}
	return n, nil
}

// pruneWait bounds how long `settings` waits to delete receipts a lowered
// limit no longer allows.
const pruneWait = 5 * time.Second

// pruneNow applies the policy in force to the store before the command exits (a
// short-lived process would otherwise close before the background prune ran).
// It waits at most pruneWait; a timeout or failure is a note, never an error.
func pruneNow(ws workspace.Workspace) []string {
	rec := ws.Receipts()
	if rec == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- rec.Store().Prune(workspace.ReceiptPolicy(ws.Local())) }()
	select {
	case err := <-done:
		if err != nil {
			return []string{"older receipts will be removed the next time Slaw prunes (" + err.Error() + ")"}
		}
	case <-time.After(pruneWait):
		return []string{"older receipts were not removed yet; they will be the next time Slaw prunes"}
	}
	return nil
}

// clampNotes names each limit that Go moved to a bound.
func clampNotes(s workspace.ReceiptSettings) []string {
	eff := workspace.SettingsOf(s.Policy().Clamp())
	var notes []string
	for _, p := range []struct {
		name     string
		in, used int
		unit     string
	}{{"days", s.Days, eff.Days, ""}, {"max-count", s.MaxCount, eff.MaxCount, ""}, {"max-mb", s.MaxMB, eff.MaxMB, " MB"}} {
		if p.in != 0 && p.in != p.used {
			notes = append(notes, fmt.Sprintf("--%s %d is outside the allowed range; using %d%s", p.name, p.in, p.used, p.unit))
		}
	}
	return notes
}

func printReceiptsSettings(w io.Writer, o receiptsSettingsOut) {
	keep := "off (names and a fingerprint only)"
	if o.Effective.KeepParams {
		keep = "on"
	}
	fmt.Fprintf(w, "Keep parameter values  %s\n", keep)
	fmt.Fprintf(w, "Keep for               %d days\n", o.Effective.Days)
	fmt.Fprintf(w, "At most                %d receipts\n", o.Effective.MaxCount)
	fmt.Fprintf(w, "Using at most          %d MB\n", o.Effective.MaxMB)
	d := workspace.ReceiptDefaults()
	fl, ce := workspace.ReceiptFloors(), workspace.ReceiptCeilings()
	fmt.Fprintf(w, "\nDefaults: %d days, %d receipts, %d MB. Allowed: %d-%d days, %d-%d receipts, %d-%d MB.\n",
		d.Days, d.MaxCount, d.MaxMB, fl.Days, ce.Days, fl.MaxCount, ce.MaxCount, fl.MaxMB, ce.MaxMB)
	fmt.Fprintf(w, "Kept now: %d receipts, %s", o.Count, byteSize(o.Bytes))
	if o.Oldest != "" {
		fmt.Fprintf(w, ", oldest from %s", o.Oldest[:10])
	}
	fmt.Fprintln(w)
	for _, n := range o.Notes {
		fmt.Fprintln(w, "note:", n)
	}
}

// byteSize is n in KB or MB with one decimal.
func byteSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func runReceiptsClear(cmd *cobra.Command, _ []string) error {
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close()
	rec := ws.Receipts()
	if rec == nil {
		return errors.New("receipts are not being kept for this workspace (see the warning above)")
	}
	n, size, _ := rec.Store().Usage()
	if yes, _ := cmd.Flags().GetBool("yes"); !yes {
		if f, ok := cmd.InOrStdin().(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
			return errors.New("not a terminal: pass --yes to delete the receipts without asking")
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Delete all %d receipts (%s)? This cannot be undone. [y/N] ", n, byteSize(size))
		line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(cmd.OutOrStdout(), "Nothing deleted.")
			return nil
		}
	}
	if err := rec.Store().Clear(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Deleted %d receipts.\n", n)
	return nil
}
