package main

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/SandwichLabs/slaw/internal/bundle"
	"github.com/SandwichLabs/slaw/internal/filestore"
)

// .slaw bundles (#118). A bundle is something the person exports and imports;
// no agent tool and no MCP method reaches any of this (see
// internal/bundle/isolation_test.go). The passphrase crosses the Wails bridge
// one way only (into Go), is never stored on the App, and is never logged.

// bundleCreatedBy is recorded in a bundle's manifest.
const bundleCreatedBy = "Slaw"

// maxOpenBundles bounds how many previewed bundles are held in memory at once
// (each is at most 50 MB unpacked); the oldest is dropped past that.
const maxOpenBundles = 3

// bundleHandleTTL is how long a preview can wait for its commit.
const bundleHandleTTL = 30 * time.Minute

type heldBundle struct {
	pkg     *bundle.Package
	expires time.Time
}

// bundleStore holds previewed bundles by handle.
type bundleStore struct {
	mu   sync.Mutex
	held map[string]*heldBundle
	// order lists handles oldest first.
	order []string
}

func (s *bundleStore) put(pkg *bundle.Package) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == nil {
		s.held = map[string]*heldBundle{}
	}
	s.expireLocked()
	for len(s.order) >= maxOpenBundles {
		delete(s.held, s.order[0])
		s.order = s.order[1:]
	}
	h := uuid.NewString()
	s.held[h] = &heldBundle{pkg: pkg, expires: time.Now().Add(bundleHandleTTL)}
	s.order = append(s.order, h)
	return h
}

// take removes and returns a held bundle in one step: of two commits racing for
// one handle, only one gets it.
func (s *bundleStore) take(h string) (*bundle.Package, bool) {
	// Lookup, expiry, removal and the order trim happen under one lock: with the
	// steps separate, two commits could both find the handle and both get it.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	e, ok := s.held[h]
	if !ok {
		return nil, false
	}
	s.dropLocked(h)
	return e.pkg, true
}

// clear forgets every held bundle (the workspace they were previewed against
// was closed or switched).
func (s *bundleStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held, s.order = nil, nil
}

// restore puts a taken bundle back under its handle.
func (s *bundleStore) restore(h string, pkg *bundle.Package) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == nil {
		s.held = map[string]*heldBundle{}
	}
	s.held[h] = &heldBundle{pkg: pkg, expires: time.Now().Add(bundleHandleTTL)}
	s.order = append(s.order, h)
}

func (s *bundleStore) drop(h string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropLocked(h)
}

func (s *bundleStore) dropLocked(h string) {
	delete(s.held, h)
	for i, o := range s.order {
		if o == h {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

func (s *bundleStore) expireLocked() {
	now := time.Now()
	for h, e := range s.held {
		if now.After(e.expires) {
			delete(s.held, h)
			for i, o := range s.order {
				if o == h {
					s.order = append(s.order[:i], s.order[i+1:]...)
					break
				}
			}
		}
	}
}

var errBundleGone = errors.New("This import has expired. Choose the file again.")

// ---- what can be exported ----

// BundleContentItem, BundleContentConnection and BundleContent are what the
// Export dialog can offer; bundle.ContentsOf builds them.
type (
	BundleContentItem       = bundle.ContentItem
	BundleContentConnection = bundle.ContentConnection
	BundleContent           = bundle.Contents
)

// GetBundleContent lists what the open workspace could export.
func (a *App) GetBundleContent() (*BundleContent, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	return bundle.ContentsOf(a.ctxOrBackground(), a.ws)
}

// ---- export ----

// BundleExportRequest is the Export dialog's answer.
type BundleExportRequest struct {
	// Path is the destination; empty asks with a save dialog.
	Path      string           `json:"path"`
	Selection bundle.Selection `json:"selection"`
	// Passphrase, when non-empty, encrypts the bundle.
	Passphrase string `json:"passphrase"`
}

// BundleExportResult says what was written.
type BundleExportResult struct {
	Path      string   `json:"path"`
	Cancelled bool     `json:"cancelled"`
	Encrypted bool     `json:"encrypted"`
	Notebooks int      `json:"notebooks"`
	Queries   int      `json:"queries"`
	Skills    int      `json:"skills"`
	Artifacts int      `json:"artifacts"`
	Semantics int      `json:"semantics"`
	Watches   int      `json:"watches"`
	Connected int      `json:"connections"`
	Bytes     int      `json:"bytes"`
	Warnings  []string `json:"warnings"`
}

// chooseBundleSaveFile asks where to write a bundle.
func (a *App) chooseBundleSaveFile(defaultName string) (string, error) {
	if defaultName == "" {
		defaultName = "workspace"
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export workspace content",
		DefaultFilename: filestore.Slug(defaultName) + ".slaw",
		Filters:         []runtime.FileFilter{{DisplayName: "Slaw bundle (*.slaw)", Pattern: "*.slaw"}},
	})
	if err != nil {
		return "", fmt.Errorf("failed to open the save dialog: %w", err)
	}
	return path, nil
}

// SelectBundleFile asks which bundle to import.
func (a *App) SelectBundleFile() (string, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Import a .slaw bundle",
		Filters: []runtime.FileFilter{{DisplayName: "Slaw bundle (*.slaw)", Pattern: "*.slaw"}},
	})
	if err != nil {
		return "", fmt.Errorf("failed to open the file dialog: %w", err)
	}
	return path, nil
}

// ExportBundle writes the selected content of the open workspace to a .slaw
// bundle. It never reads credentials or data.
func (a *App) ExportBundle(req BundleExportRequest) (*BundleExportResult, error) {
	a.mu.RLock()
	if err := a.requireWorkspace(); err != nil {
		a.mu.RUnlock()
		return nil, err
	}
	ws := a.ws
	name := ws.Name()
	a.mu.RUnlock()

	path := req.Path
	if path == "" {
		p, err := a.chooseBundleSaveFile(name)
		if err != nil {
			return nil, err
		}
		if p == "" {
			return &BundleExportResult{Cancelled: true, Warnings: []string{}}, nil
		}
		path = p
	}
	path = bundle.WithExt(path)
	res, err := bundle.Export(a.ctxOrBackground(), ws, bundle.ExportOptions{Selection: req.Selection, Passphrase: req.Passphrase, AppVersion: bundleCreatedBy})
	if err != nil {
		return nil, errors.New(a.redact(err.Error()))
	}
	// The file dialog already asked about replacing; a typed-in path is the
	// person's own choice too.
	if err := bundle.WriteFile(path, res.Data); err != nil {
		return nil, err
	}
	n := res.Manifest.Counts()
	return &BundleExportResult{
		Path: path, Encrypted: res.Encrypted, Bytes: len(res.Data), Warnings: append([]string{}, res.Manifest.Warnings...),
		Notebooks: n.Notebooks, Queries: n.Queries, Skills: n.Skills, Artifacts: n.Artifacts, Semantics: n.Semantics, Watches: n.Watches, Connected: n.Connections,
	}, nil
}

// ---- import ----

// BundlePreview is what the Import dialog shows. When the bundle is encrypted
// and no passphrase (or a wrong one) was given, NeedsPassphrase is set and the
// rest is empty.
type BundlePreview struct {
	Handle          string `json:"handle"`
	NeedsPassphrase bool   `json:"needsPassphrase"`
	WrongPassphrase bool   `json:"wrongPassphrase"`
	Path            string `json:"path"`
	bundle.Preview
}

// PreviewBundleImport opens a bundle file, verifies it, and reports what is in
// it and which items already exist in the open workspace (none, when no
// workspace is open: the launcher importing into a new one). Nothing is written.
func (a *App) PreviewBundleImport(path, passphrase string) (*BundlePreview, error) {
	data, err := bundle.ReadFile(path)
	if err != nil {
		return nil, bundleError(err)
	}
	info, err := bundle.Peek(data)
	if err != nil {
		return nil, bundleError(err)
	}
	pv := &BundlePreview{Path: path, Preview: bundle.Preview{Items: []bundle.PreviewItem{}, Connections: []bundle.PreviewConnection{}, Warnings: []string{}}}
	if info.Encrypted && passphrase == "" {
		pv.NeedsPassphrase, pv.Encrypted = true, true
		return pv, nil
	}
	pkg, err := bundle.Open(data, passphrase)
	switch {
	case errors.Is(err, bundle.ErrWrongPassphrase):
		pv.NeedsPassphrase, pv.WrongPassphrase, pv.Encrypted = true, true, true
		return pv, nil
	case err != nil:
		return nil, bundleError(err)
	}
	a.mu.RLock()
	var inner bundle.Preview
	if a.ws != nil {
		inner = pkg.Preview(a.ctxOrBackground(), a.ws)
	} else {
		inner = pkg.Preview(a.ctxOrBackground(), nil)
	}
	a.mu.RUnlock()
	pv.Preview = inner
	pv.Handle = a.bundles.put(pkg)
	return pv, nil
}

// bundleError turns a bundle error into the sentence the dialog shows.
func bundleError(err error) error {
	switch {
	case errors.Is(err, bundle.ErrLegacy):
		return errors.New("This is an older Vibe file that holds data. Slaw doesn't open these.")
	case errors.Is(err, bundle.ErrNotBundle):
		return errors.New("That file isn't a Slaw bundle.")
	case errors.Is(err, bundle.ErrTooLarge):
		return errors.New("That bundle is too large to import.")
	case errors.Is(err, bundle.ErrCorrupt):
		return errors.New("That bundle is damaged or has been changed, so it was not imported. " + err.Error())
	}
	return err
}

// BundleDecisions is the Import dialog's answer.
type BundleDecisions struct {
	// OnConflict applies to items without their own entry: rename (default),
	// overwrite or skip.
	OnConflict string `json:"onConflict"`
	// Items maps "notebook/<id>" and so on to rename, overwrite or skip.
	Items map[string]string `json:"items"`
	// Connections names the definitions to add to "This computer".
	Connections []string `json:"connections"`
}

func (d BundleDecisions) options() bundle.ImportOptions {
	opts := bundle.ImportOptions{OnConflict: bundle.Action(d.OnConflict), IncludeConnections: d.Connections}
	if len(d.Items) > 0 {
		opts.Decisions = make(map[string]bundle.Action, len(d.Items))
		for k, v := range d.Items {
			opts.Decisions[k] = bundle.Action(v)
		}
	}
	return opts
}

// CommitBundleImport writes a previewed bundle into the open workspace. The
// handle is spent the moment it is taken (a bundle is committed once), and the
// workspace stays locked against being closed or switched while it is written.
func (a *App) CommitBundleImport(handle string, d BundleDecisions) (*bundle.Result, error) {
	pkg, ok := a.bundles.take(handle)
	if !ok {
		return nil, errBundleGone
	}
	return a.commitPackage(handle, pkg, d)
}

func (a *App) commitPackage(handle string, pkg *bundle.Package, d BundleDecisions) (*bundle.Result, error) {
	a.mu.RLock()
	if err := a.requireWorkspace(); err != nil {
		a.mu.RUnlock()
		return nil, err
	}
	res, err := pkg.Import(a.ctxOrBackground(), a.ws, d.options())
	a.mu.RUnlock()
	if err != nil {
		// Nothing was written (bad options): the person can choose again.
		a.bundles.restore(handle, pkg)
		return nil, err
	}
	a.afterBundleImport(res)
	return &res, nil
}

func (a *App) afterBundleImport(res bundle.Result) {
	// A bundle holds no data, so it never creates a table; a connection it
	// wrote is re-registered, and the rail and completion reload (#71).
	touched := false
	for _, o := range res.Outcomes {
		if o.Kind == bundle.KindConnection && o.Error == "" && o.Result != "skipped" {
			touched = true
		}
	}
	// The stores' own writes are not reported by the file watcher, so tell the
	// lists to refresh.
	a.emitNotebooksChanged("", nil)
	a.emitEvent("data:changed")
	// Same for artifacts: the Library and an open artifact listen for
	// the watcher's on-disk event, which the store's own writes never raise.
	for _, o := range res.Outcomes {
		if o.Kind == bundle.KindArtifact && o.Error == "" && o.Result != "skipped" {
			a.emitEvent("workspace:changed-on-disk", map[string]any{"kind": "artifact", "id": o.FinalID, "op": "changed"})
		}
	}
	if touched {
		a.syncConnections()
		a.emitEvent("workspace:changed", map[string]any{"kind": "connections"})
		// syncConnections also re-registers what depends on a touched connection.
		a.catalogChanged("")
	}
}

// ImportBundleIntoNewWorkspace creates a workspace (the launcher flow) and
// imports a previewed bundle into it. The handle is taken first: opening the
// new workspace drops every other held preview.
func (a *App) ImportBundleIntoNewWorkspace(handle string, req CreateWorkspaceRequest, d BundleDecisions) (*BundleImportOpened, error) {
	pkg, ok := a.bundles.take(handle)
	if !ok {
		return nil, errBundleGone
	}
	opened, err := a.CreateWorkspace(req)
	if err != nil {
		a.bundles.restore(handle, pkg)
		return nil, err
	}
	res, err := a.commitPackage(handle, pkg, d)
	if err != nil {
		return nil, fmt.Errorf("The workspace was created, but the import failed: %w", err)
	}
	return &BundleImportOpened{Opened: opened, Result: res}, nil
}

// BundleImportOpened is the result of importing into a new workspace.
type BundleImportOpened struct {
	Opened *OpenWorkspaceResult `json:"opened"`
	Result *bundle.Result       `json:"result"`
}

// DiscardBundleImport forgets a previewed bundle (the dialog was closed).
func (a *App) DiscardBundleImport(handle string) { a.bundles.drop(handle) }
