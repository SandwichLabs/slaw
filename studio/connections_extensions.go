package main

// DuckDB extensions the connection types need.

import (
	"context"
	"errors"
	"time"

	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// ExtensionInfo is one DuckDB extension a connection type needs.
type ExtensionInfo struct {
	Name      string   `json:"name"`
	Purpose   string   `json:"purpose"`
	NeededBy  []string `json:"neededBy"` // connections in this workspace
	Installed bool     `json:"installed"`
}

var extensionPurpose = map[string]string{
	"postgres":   "Read PostgreSQL databases.",
	"httpfs":     "Read files from S3, Google Cloud Storage and R2, and talk to Iceberg REST catalogs.",
	"sqlite":     "Read SQLite files.",
	"motherduck": "Connect to MotherDuck.",
	"mysql":      "Read MySQL and MariaDB databases.",
	"azure":      "Read files from Azure Blob Storage and Data Lake.",
	"ducklake":   "Read and write DuckLake lakehouses.",
	"iceberg":    "Read Iceberg tables and catalogs (Amazon S3 Tables, REST).",
	"avro":       "Read Avro files (Iceberg metadata needs it).",
	"delta":      "Read Delta Lake tables.",
}

// ListExtensions lists the extensions the connection types need, whether each is
// installed, and which of this workspace's connections need it. It does not
// need an open workspace (App settings show it anyway).
func (a *App) ListExtensions() ([]ExtensionInfo, error) {
	a.mu.RLock()
	ws := a.ws
	a.mu.RUnlock()
	needs := workspace.ExtensionNeeds(ws)
	names := make([]string, len(needs))
	for i, n := range needs {
		names[i] = n.Name
	}
	installed := a.installedExtensions(names)
	out := make([]ExtensionInfo, 0, len(needs))
	for _, n := range needs {
		out = append(out, ExtensionInfo{Name: n.Name, Purpose: extensionPurpose[n.Name], NeededBy: n.NeededBy, Installed: installed[n.Name]})
	}
	return out, nil
}

// installedExtensions asks a throwaway DuckDB that uses the app's private
// extension directory which of names are installed there.
func (a *App) installedExtensions(names []string) map[string]bool {
	out := map[string]bool{}
	dir, _ := session.DefaultDirs()
	db, err := session.OpenDB("", session.WithExtensionDirectory(dir))
	if err != nil {
		return out
	}
	defer db.Close() //nolint:errcheck
	return session.ExtensionsInstalled(a.ctxOrBackground(), session.FromDB(db), names)
}

// InstallExtensionRequest is the person's consent to download one extension.
type InstallExtensionRequest struct {
	Name string `json:"name"`
}

// InstallExtension downloads an extension a connection type needs, after the
// person said yes in the consent dialog (never automatic), then re-checks the
// connections that were waiting for it.
func (a *App) InstallExtension(req InstallExtensionRequest) error {
	_, sess, _, err := a.snapshot()
	if err != nil {
		return err
	}
	a.emitEvent("extension:progress", map[string]any{"name": req.Name, "state": "installing"})
	ctx, cancel := context.WithTimeout(a.ctxOrBackground(), 5*time.Minute)
	defer cancel()
	if err := sess.InstallExtension(ctx, req.Name); err != nil {
		a.emitEvent("extension:progress", map[string]any{"name": req.Name, "state": "error"})
		return errors.New(a.redact(err.Error()))
	}
	for _, st := range sess.Connections() {
		if st.Kind == session.StatusExtensionMissing {
			sess.Preflight(ctx, st.Name)
			a.emitStatus(sess, st.Name)
		}
	}
	a.emitEvent("extension:progress", map[string]any{"name": req.Name, "state": "done"})
	a.emitEvent("workspace:changed", map[string]any{"kind": "connections"})
	return nil
}
