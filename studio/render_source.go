package main

// Where a render-time read came from, for its receipt (docs/plans/run-receipts.md,
// R4a). The frontend says which notebook cell or A2UI component asked; Go does
// not trust it. The source is checked against the open notebook or artifact, and
// one that does not match is dropped: the run goes ahead as a plain "render"
// with no ref, never refused.

import (
	"context"
	"encoding/json"

	"github.com/SandwichLabs/slaw/internal/artifacts"
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
func renderOrigin(ctx context.Context, ws workspace.Workspace, src RenderSource) receipts.Origin {
	plain := receipts.Origin{Surface: receipts.SurfaceRender}
	if ws == nil {
		return plain
	}
	var o receipts.Origin
	var ok bool
	switch {
	case src.Kind == renderSourceCell && src.Notebook != "" && src.Cell != "":
		o, ok = notebookOrigin(ctx, ws, src)
	case src.Kind == renderSourceA2UI && src.Notebook != "" && src.Cell != "" && src.Component != "":
		o, ok = notebookOrigin(ctx, ws, src)
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
func notebookOrigin(ctx context.Context, ws workspace.Workspace, src RenderSource) (receipts.Origin, bool) {
	nb, _, err := ws.Notebooks().Get(ctx, src.Notebook)
	if err != nil {
		return receipts.Origin{}, false
	}
	ref := "notebooks/" + nb.ID + ".slnb#" + src.Cell
	for _, c := range nb.Cells {
		if c.ID != src.Cell {
			continue
		}
		switch {
		case src.Kind == renderSourceCell && c.Type == notebooks.CellSQL:
			return receipts.Origin{Surface: receipts.SurfaceNotebookCell, Source: receipts.Source{Kind: receipts.SourceCell, Ref: ref}}, true
		case src.Kind == renderSourceA2UI && c.Type == notebooks.CellA2UI && c.Page != nil && pageHas(c, src.Component):
			return receipts.Origin{Surface: receipts.SurfaceA2UI, Source: receipts.Source{Kind: receipts.SourceComponent, Ref: ref + "/" + src.Component}}, true
		}
	}
	return receipts.Origin{}, false
}

// pageHas reports whether the A2UI cell's page has a component with this id.
func pageHas(c notebooks.Cell, id string) bool {
	for _, comp := range c.Page.Components {
		if comp.ID == id {
			return true
		}
	}
	return false
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
