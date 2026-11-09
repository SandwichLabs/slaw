package main

// Where a render-time read came from, for its receipt (docs/plans/run-receipts.md,
// R4a). The frontend says which notebook cell or A2UI component asked; Go does
// not trust it. The source is checked against the open notebook or artifact, and
// one that does not match is dropped: the run goes ahead as a plain "render"
// with no ref, never refused.

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/SandwichLabs/slaw/internal/artifacts"
	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/notebooks"
	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// Render source kinds.
const (
	// renderSourceCell is a notebook SQL cell.
	renderSourceCell = "cell"
	// renderSourceA2UI is an A2UI component: in a notebook's A2UI cell, or in an
	// artifact's view.
	renderSourceA2UI = "a2ui"
)

// RenderSource names the notebook cell or A2UI component a RunA2UIQueryIn run is
// for. A cell is {kind: "cell", notebook, cell}; a component is {kind: "a2ui",
// notebook, cell, component} or {kind: "a2ui", artifact, component}. The zero
// value (a chat reply's page) is a render with no source.
type RenderSource struct {
	Kind      string `json:"kind"`
	Notebook  string `json:"notebook,omitempty"`
	Artifact  string `json:"artifact,omitempty"`
	Cell      string `json:"cell,omitempty"`
	Component string `json:"component,omitempty"`
}

// renderOrigin is the receipt origin of a render-time read: the surface and the
// file-relative ref src names when the open workspace has that notebook cell or
// component, otherwise a plain render with no ref.
func renderOrigin(ctx context.Context, ws workspace.Workspace, idx *notebookIndexCache, src RenderSource) receipts.Origin {
	plain := receipts.Origin{Surface: receipts.SurfaceRender}
	if ws == nil {
		return plain
	}
	var o receipts.Origin
	var ok bool
	switch {
	case src.Kind == renderSourceCell && src.Notebook != "" && src.Cell != "":
		o, ok = notebookOrigin(ws, idx, src)
	case src.Kind == renderSourceA2UI && src.Notebook != "" && src.Cell != "" && src.Component != "":
		o, ok = notebookOrigin(ws, idx, src)
	case src.Kind == renderSourceA2UI && src.Artifact != "" && src.Component != "":
		o, ok = artifactComponentOrigin(ctx, ws, src)
	}
	if !ok || !receipts.ValidRef(o.Source.Ref) {
		return plain
	}
	return o
}

// notebookOrigin checks a notebook source against the notebook on disk: it
// exists, the cell is in it and is of the right type, and a component is on that
// cell's page.
func notebookOrigin(ws workspace.Workspace, idx *notebookIndexCache, src RenderSource) (receipts.Origin, bool) {
	cells, ok := idx.cells(ws.Root(), src.Notebook)
	if !ok {
		return receipts.Origin{}, false
	}
	c, ok := cells[src.Cell]
	if !ok {
		return receipts.Origin{}, false
	}
	ref := "notebooks/" + src.Notebook + ".slnb#" + src.Cell
	switch {
	case src.Kind == renderSourceCell && c.typ == notebooks.CellSQL:
		return receipts.Origin{Surface: receipts.SurfaceNotebookCell, Source: receipts.Source{Kind: receipts.SourceCell, Ref: ref}}, true
	case src.Kind == renderSourceA2UI && c.typ == notebooks.CellA2UI && c.components[src.Component]:
		return receipts.Origin{Surface: receipts.SurfaceA2UI, Source: receipts.Source{Kind: receipts.SourceComponent, Ref: ref + "/" + src.Component}}, true
	}
	return receipts.Origin{}, false
}

// cellIndex is what a source check needs of one cell: its type and the ids of
// the components on its page.
type cellIndex struct {
	typ        notebooks.CellType
	components map[string]bool
}

// notebookIndexCache keeps the cell index of the notebooks a render asked about,
// so a page of N components does not read and parse the notebook N times. An entry
// is good while the file's size and modification time are the ones it was built
// from (one stat per use); a change from git, an editor or the autosave makes it
// miss and the file is read again. It reads the file itself, never through the
// notebook store, whose Get would record the revision as seen and so hide a later
// conflict from the editor. It is bounded: at most notebookIndexMax notebooks.
type notebookIndexCache struct {
	mu sync.Mutex
	m  map[string]notebookIndex
}

const notebookIndexMax = 16

type notebookIndex struct {
	size  int64
	mod   time.Time
	cells map[string]cellIndex
}

func (c *notebookIndexCache) cells(root, id string) (map[string]cellIndex, bool) {
	if !filestore.ValidID(id) {
		return nil, false
	}
	rel := notebooks.Dir + "/" + id + notebooks.Extension
	abs, err := filestore.Resolve(root, rel)
	if err != nil {
		return nil, false
	}
	fi, err := os.Lstat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	key := root + "\x00" + id
	c.mu.Lock()
	e, hit := c.m[key]
	c.mu.Unlock()
	if hit && e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
		return e.cells, true
	}
	data, _, err := filestore.Read(root, rel)
	if err != nil {
		return nil, false
	}
	nb, _, err := notebooks.Coerce(data)
	if err != nil {
		return nil, false
	}
	e = notebookIndex{size: fi.Size(), mod: fi.ModTime(), cells: make(map[string]cellIndex, len(nb.Cells))}
	for _, cell := range nb.Cells {
		ci := cellIndex{typ: cell.Type}
		if cell.Page != nil {
			ci.components = make(map[string]bool, len(cell.Page.Components))
			for _, comp := range cell.Page.Components {
				ci.components[comp.ID] = true
			}
		}
		e.cells[cell.ID] = ci
	}
	c.mu.Lock()
	if c.m == nil || len(c.m) >= notebookIndexMax {
		c.m = make(map[string]notebookIndex, notebookIndexMax)
	}
	c.m[key] = e
	c.mu.Unlock()
	return e.cells, true
}

// artifactComponentOrigin checks an artifact source: the artifact exists, its
// view is an A2UI document, and the component is in it.
func artifactComponentOrigin(ctx context.Context, ws workspace.Workspace, src RenderSource) (receipts.Origin, bool) {
	art, err := ws.Artifacts().Get(ctx, src.Artifact)
	if err != nil || (art.Manifest.View != "" && art.Manifest.View != artifacts.ViewA2UI) {
		return receipts.Origin{}, false
	}
	var view struct {
		Components []struct {
			ID string `json:"id"`
		} `json:"components"`
	}
	if json.Unmarshal(art.View, &view) != nil {
		return receipts.Origin{}, false
	}
	for _, c := range view.Components {
		if c.ID == src.Component {
			ref := "artifacts/" + art.Name + "/" + artifacts.ViewFileOf(&art.Manifest) + "#" + src.Component
			return receipts.Origin{Surface: receipts.SurfaceA2UI, Source: receipts.Source{Kind: receipts.SourceComponent, Ref: ref}}, true
		}
	}
	return receipts.Origin{}, false
}
