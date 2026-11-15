package main

import (
	"fmt"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/importer"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/skills"
)

// ExportQueryResults writes the result of a SQL query to CSV or Parquet via
// DuckDB COPY (W5). format is "csv" or "parquet". dst is the destination path;
// when empty, a native save dialog is shown (the UI path) — a non-empty dst
// writes directly (scriptable/testable, mirroring ExportTableXLSX). Returns
// the written path, or "" if the dialog was cancelled.
func (a *App) ExportQueryResults(sqlStr, format, dst string) (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return "", err
	}
	if strings.TrimSpace(sqlStr) == "" {
		return "", fmt.Errorf("no query to export")
	}

	ext, copyOpts, err := session.CopyFormat(format)
	if err != nil {
		return "", err
	}
	if dst == "" {
		if dst, err = a.saveDialog("Export query results", "query"+ext, format, ext); err != nil || dst == "" {
			return "", err
		}
	}
	if !strings.HasSuffix(strings.ToLower(dst), ext) {
		dst += ext
	}
	if err := a.exportQuery(sqlStr, dst, copyOpts); err != nil {
		return "", err
	}
	a.logger.Info("exported query results", "format", format, "path", dst)
	return dst, nil
}

// exportQuery writes the editor's statement to dst as the editor would run it:
// guarded, metric() calls expanded, held to one SELECT and parameters bound
// (hub.PrepareExport), then COPY. The caller holds a.mu.
func (a *App) exportQuery(sqlStr, dst, copyOpts string) error {
	ctx := a.ctxOrBackground()
	ddb := a.sess.DB()
	if ddb == nil {
		return fmt.Errorf("DuckDB not available")
	}
	exp, err := hub.PrepareExport(ctx, a.ws, a.sess, a.logger, hub.PersonRun{SQL: sqlStr, Guard: hub.InteractiveGuard(a.sess)})
	if err != nil {
		return err
	}
	text, args, err := engine.BindParams(exp.SQL, exp.Params)
	if err != nil {
		return err
	}
	// The statement runs on the pool, not the engine: renew a storage secret
	// that is about to end and dial what it names, as the engine would.
	_ = a.sess.ConnectReferenced(ctx, "", exp.SQL)
	if err := session.CopyQueryTo(ctx, ddb, text, args, dst, copyOpts); err != nil {
		return fmt.Errorf("failed to export query results: %w", err)
	}
	return nil
}

// saveDialog asks where to save a file of the given format and extension. It
// returns "" with no error when the dialog was cancelled.
func (a *App) saveDialog(title, filename, format, ext string) (string, error) {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           title,
		DefaultFilename: filename,
		Filters: []runtime.FileFilter{
			{DisplayName: strings.ToUpper(format) + " Files (*" + ext + ")", Pattern: "*" + ext},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to open save dialog: %w", err)
	}
	return path, nil
}

// --- Import/Export ---

// SelectImportFile opens a file dialog to select a data file for import
// (CSV/TSV or Excel). Replaces SelectCSVFile and the old
// SelectSpreadsheetFile stub (which always returned "not implemented" —
// the xlsx Browse button was broken in the real app before this).
func (a *App) SelectImportFile() (string, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Import data",
		Filters: []runtime.FileFilter{
			{DisplayName: "Data files (*.csv, *.tsv, *.xlsx)", Pattern: "*.csv;*.tsv;*.xlsx"},
			{DisplayName: "CSV Files (*.csv)", Pattern: "*.csv"},
			{DisplayName: "Excel Files (*.xlsx)", Pattern: "*.xlsx"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to open file dialog: %w", err)
	}
	return path, nil
}

// InspectImport stages the whole file and returns its columns (with
// inferred types and primary-key candidates), a preview of the first 50
// rows, and the total row count — the data behind the wizard's preview
// grid. Read-only: it never writes to the container, only to the DuckDB
// session's own temp catalog, so it takes RLock like any other query.
func (a *App) InspectImport(src importer.Source) (*importer.Inspection, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	db := a.sess.DB()
	if db == nil {
		return nil, fmt.Errorf("DuckDB not available")
	}
	return importer.Inspect(a.ctx, db, src)
}

// ImportFile runs one import per the supplied options — create, replace,
// append, or upsert — pre-flighted and written atomically by
// importer.Import. Replaces ImportCSV, ImportSpreadsheet, and
// PreviewSpreadsheet.
//
// A dry run (o.DryRun) only writes to the DuckDB session's temp catalog, so
// it takes RLock; a real run takes the full Lock. Only a real, successful
// run (!DryRun && summary.OK) marks the file dirty, refreshes the
// workspace-<table> skill, and emits data:changed — a dry run or a
// pre-flight failure leaves the file exactly as it was.
func (a *App) ImportFile(o importer.Options) (*importer.Summary, error) {
	// No identifier check here: importer.Import validates o.Table
	// itself (engine.ValidIdentifier)
	// and returns a clean "invalid table name" error — a second
	// check here would just be two call sites that could quietly diverge.
	if o.DryRun {
		a.mu.RLock()
		defer a.mu.RUnlock()
	} else {
		a.mu.Lock()
		defer a.mu.Unlock()
	}

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	db := a.sess.DB()
	if db == nil {
		return nil, fmt.Errorf("DuckDB not available")
	}

	summary, err := importer.Import(a.ctx, db, o)
	if err != nil {
		return nil, err
	}

	if !o.DryRun && summary.OK {
		// The write already landed atomically inside importer.Import's own
		// transaction — there is no "mark dirty as writes begin" window to
		// worry about here, unlike the old per-row importer.
		// summary.Table, not o.Table: for append/upsert/replace into an
		// existing table, Import resolves and returns the CATALOG's own
		// spelling (a case-insensitive match, e.g. o.Table="customers"
		// against a table actually named "Customers") — writing the skill
		// under the caller's original casing could create a second,
		// wrong-table skill instead of refreshing the real one's.
		if err := skills.WriteImportSkill(a.ws.Skills(), a.sess, summary.Table); err != nil {
			a.logger.Warn("failed to write import skill", "table", summary.Table, "error", err)
		}
		a.emitEvent("data:changed")
		// The rail lists tables from the catalog, so it reloads (#71); only the
		// connection the import wrote to, or all when its name does not resolve.
		a.catalogChanged(a.importCatalog(o.Table))
	}

	return summary, nil
}

// importCatalog is the connection a table name (as the import was given it)
// lives on: its catalog when it is three-part, the default when it is a bare
// name, and "" (every connection) otherwise, since a two-part name is a schema
// of the current catalog first (the importer's own rule) and an attached
// catalog second. The caller holds a.mu.
func (a *App) importCatalog(table string) string {
	ref, err := engine.ParseTable(table)
	switch {
	case err != nil:
		return ""
	case ref.Catalog != "" && ref.Schema != "":
		return ref.Catalog
	case ref.Catalog == "" && ref.Schema == "":
		return a.sess.DefaultCatalog()
	}
	return ""
}

// --- Helpers ---
