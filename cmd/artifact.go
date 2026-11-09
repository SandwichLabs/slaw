package cmd

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/SandwichLabs/slaw/internal/artifacts"
)

var artifactCmd = &cobra.Command{
	Use:   "artifact",
	Short: "List the workspace's data artifacts, trust their custom views, render static pages",
	Long: `Data artifacts are parameterised, live views kept in the workspace's
artifacts/<name>/ folders. They are made and edited in Studio (or by an agent
over MCP). "list" shows them the way Studio's Library does (live views, pages,
chart scripts), "view" allows or denies a custom script view or page, and
"render" draws an allowed static HTML page to a folder of files.`,
}

var artifactListCmd = &cobra.Command{
	Use:   "list",
	Short: "List data artifacts: name, title, view kind and any error",
	Long: `List the workspace's data artifacts the way Studio's Library shows them (live
views, pages, chart scripts): name, title, view kind and, for one that cannot be
read, why. It reads files only: no connection is opened and no query is run.`,
	Args:         cobra.NoArgs,
	RunE:         runArtifactList,
	SilenceUsage: true,
}

// artifactRow is one line of the list: what the Studio list shows to identify
// an artifact, and nothing of its queries or view.
type artifactRow struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	View  string `json:"view"`
	Error string `json:"error,omitempty"`
}

func artifactRows(list []artifacts.Artifact) []artifactRow {
	rows := make([]artifactRow, 0, len(list))
	for _, a := range list {
		rows = append(rows, artifactRow{Name: a.Name, Title: a.Manifest.Title, View: artifacts.ViewLabel(&a), Error: a.Error})
	}
	return rows
}

func runArtifactList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	ws, err := openWS(cmd)
	if err != nil {
		return err
	}
	defer ws.Close()
	list, err := ws.Artifacts().List(cmdCtx(cmd))
	if err != nil {
		return fmt.Errorf("failed to list artifacts: %w", err)
	}
	rows := artifactRows(list)
	out := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "No data artifacts. Create one in Studio (Artifacts) or ask an agent to.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTITLE\tVIEW\tERROR")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, dash(oneLine(r.Title)), dash(r.View), dash(oneLine(r.Error)))
	}
	return tw.Flush()
}

func init() {
	artifactListCmd.Flags().Bool("json", false, "output as JSON")
	addWorkspaceFlag(artifactListCmd)
	artifactCmd.AddCommand(artifactListCmd)
	rootCmd.AddCommand(artifactCmd)
}
