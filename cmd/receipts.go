package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/SandwichLabs/slaw/internal/workspace"
)

var receiptsCmd = &cobra.Command{
	Use:   "receipts",
	Short: "Settings for run receipts, and deleting them",
	Long: `A receipt records that a question was asked and what it touched: the SQL, the
connections, the shape of the result and a fingerprint of it, never a result
value. Receipts stay in .slaw-local/receipts/ on this computer and never go
into git or a .slaw bundle.

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

A value outside its range is moved to the nearest bound and the result says so.
Lowering a limit deletes the oldest receipts above it. The consoles keep no
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
	f.Int("days", 0, "keep a receipt for this many days")
	f.Int("max-count", 0, "keep at most this many receipts")
	f.Int("max-mb", 0, "keep at most this many megabytes")
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
	next, changed, err := receiptsFlagsApplied(cmd, workspace.StoredReceiptSettings(ws.Local()))
	if err != nil {
		return err
	}
	var notes []string
	if changed {
		notes = clampNotes(next)
		if err := workspace.SetLocalReceipts(ws, next); err != nil {
			return err
		}
	}
	out := receiptsSettingsOut{
		Settings:  workspace.StoredReceiptSettings(ws.Local()),
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
		n, _ := fl.GetInt(p.flag)
		if n <= 0 {
			return cur, false, fmt.Errorf("--%s must be a positive number", p.flag)
		}
		*p.to = n
		changed = true
	}
	return cur, changed, nil
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
