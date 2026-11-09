// Package notebooks implements the Notebook contract (#66 part A): a title
// plus an ordered list of cells of three types (sql, markdown, a2ui), stored
// one per file in a workspace (notebooks/<id>.slnb, see file_format.go). Ported from schoolwork-workspace's
// src/notebook/model.js (emptyNotebook, newCell, coerceNotebook, VIEWS,
// SCHEMA_VERSION) — same whitelist-rebuild-on-load contract, same rules:
//
//   - No inter-cell references — the database is the only shared state.
//   - Outputs are NEVER persisted. Only source/view/page are stored; a data
//     cell's last result lives in the Studio UI's memory only and is gone the
//     moment the notebook is reopened.
//   - Coerce is the ONE path every load goes through (storage.Get, import,
//     and Save all call it) — untrusted input (an imported file, a row a
//     future version wrote, a hand-edited file shared by someone else) never
//     reaches the UI without being rebuilt field-by-field from a whitelist.
package notebooks

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/SandwichLabs/slaw/internal/a2ui"
	"github.com/google/uuid"
)

// SchemaVersion is this package's notebook schema version. Coerce rejects a
// notebook whose declared schemaVersion is newer — the same "a file from a
// newer Slaw must not silently corrupt under an older one" rule model.js's
// coerceNotebook enforces.
//
// Version 2 (#108) adds an optional `connection` on sql cells (and on the
// `sql`-bound components of a2ui cells, see internal/a2ui): the connection
// whose catalog is the default for the cell's unqualified table names. Unset
// means the workspace default. A version-1 notebook is a valid version-2
// notebook with no connection set: it loads unchanged and is stamped with the
// current version the next time it is saved.
const SchemaVersion = 2

// FileKind is the "kind" tag an exported notebook file (ExportNotebook) is
// stamped with, and the value ImportNotebook checks against — mirrors
// model.js's FILE_KIND, adapted to this product's naming.
const FileKind = "slaw/notebook"

// maxCells caps a notebook at a sane size (issue #66 part A: "cap cell
// count"). A pathological import (or a future runaway task-mode loop) that
// tries to write thousands of cells is truncated, with a warning, rather
// than accepted whole.
const maxCells = 500

// maxCellSourceBytes caps a single cell's `source` (sql/markdown) at 256KB.
// A cell source this large is almost certainly not something the notebook
// UI's editor was meant to hold, and would otherwise bloat the encrypted
// container on every save.
const maxCellSourceBytes = 256 * 1024

// MaxCellSourceBytes is maxCellSourceBytes for callers that refuse an oversized
// cell up front instead of letting Coerce truncate it with a warning.
const MaxCellSourceBytes = maxCellSourceBytes

// maxDataBoundComponents caps the TOTAL number of live (sql-bound) A2UI
// components across every a2ui cell in one notebook, not just per cell
// (review fix, step 4a: a single notebook could otherwise pack 500 a2ui
// cells x 200 components each, all sql-bound, and every one of them fires a
// live query the moment the notebook opens — a trivial DoS on the file's
// one DuckDB connection). Once the budget is exhausted, further data-bound
// components in later cells are demoted to static (their "sql" field is
// dropped, so a2ui.Validate's own shape is still satisfied) rather than the
// whole cell being dropped.
const maxDataBoundComponents = 200

// maxTitleRunes / maxLabelRunes cap a notebook's title and an sql cell's
// label (review fix: an import or a hand-edited file could otherwise carry
// an arbitrarily long title/label into the UI).
const maxTitleRunes = 200
const maxLabelRunes = 120

// Views is the closed set of view kinds an sql cell may declare: model.js's
// original four plus the KINETIC charts (area, columns, rings, radar, sankey,
// heatmap), each rendered as the matching internal/a2ui chart component.
var Views = []string{"table", "bar", "line", "stat", "area", "columns", "rings", "radar", "sankey", "heatmap"}

func isValidView(v string) bool {
	for _, ok := range Views {
		if v == ok {
			return true
		}
	}
	return false
}

// CellType is the closed set of cell types a notebook may hold.
type CellType string

const (
	CellSQL      CellType = "sql"
	CellMarkdown CellType = "markdown"
	CellA2UI     CellType = "a2ui"
)

// Cell is one notebook cell. Which fields are meaningful depends on Type;
// MarshalJSON only ever emits the fields that type actually uses, matching
// model.js's per-type cell shape (a markdown cell never carries `view`, an
// sql cell never carries `page`, ...).
type Cell struct {
	ID        string
	Type      CellType
	Collapsed bool

	// sql / markdown
	Source string

	// sql only
	View       string
	ViewPinned bool
	Label      string
	// Connection (v2) names the connection whose catalog is the default for
	// this cell's unqualified table names. "" = the workspace default.
	Connection string
	// Motion is the chart animation option (a2ui's "motion" object:
	// easing, speed, stagger, spring tuning). nil = the default spring.
	Motion map[string]any

	// a2ui only. Never nil for a CellA2UI cell — Coerce drops any a2ui cell
	// whose page fails internal/a2ui.Validate rather than storing a partial
	// or invalid page.
	Page *a2ui.Page
}

// MarshalJSON emits only the fields meaningful for the cell's Type, in the
// same shape model.js's cells persist.
func (c Cell) MarshalJSON() ([]byte, error) {
	switch c.Type {
	case CellSQL:
		return json.Marshal(struct {
			ID         string         `json:"id"`
			Type       string         `json:"type"`
			Collapsed  bool           `json:"collapsed"`
			Source     string         `json:"source"`
			View       string         `json:"view"`
			ViewPinned bool           `json:"viewPinned"`
			Label      string         `json:"label"`
			Connection string         `json:"connection,omitempty"`
			Motion     map[string]any `json:"motion,omitempty"`
		}{c.ID, string(c.Type), c.Collapsed, c.Source, c.View, c.ViewPinned, c.Label, c.Connection, c.Motion})
	case CellMarkdown:
		return json.Marshal(struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Collapsed bool   `json:"collapsed"`
			Source    string `json:"source"`
		}{c.ID, string(c.Type), c.Collapsed, c.Source})
	case CellA2UI:
		return json.Marshal(struct {
			ID        string     `json:"id"`
			Type      string     `json:"type"`
			Collapsed bool       `json:"collapsed"`
			Page      *a2ui.Page `json:"page"`
		}{c.ID, string(c.Type), c.Collapsed, c.Page})
	default:
		return nil, fmt.Errorf("notebooks: cannot marshal cell %q of unknown type %q", c.ID, c.Type)
	}
}

// Notebook is a validated, whitelist-rebuilt notebook document — the Go
// mirror of model.js's notebook shape.
type Notebook struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	Title         string `json:"title"`
	Cells         []Cell `json:"cells"`
	UpdatedAt     string `json:"updatedAt"`
	// Rev is the store's opaque revision of this notebook as loaded (file
	// stores: the content hash). Hand it back with the notebook on Save and a
	// write against a stale copy returns ErrConflict. Empty for stores without
	// compare-and-write (the legacy container) and never persisted.
	Rev string `json:"rev,omitempty"`
}

func newID(prefix string) string {
	return prefix + "_" + uuid.New().String()
}

// Empty returns a fresh, empty notebook (model.js's emptyNotebook).
func Empty(title string) Notebook {
	if title == "" {
		title = "Untitled notebook"
	}
	return Notebook{
		SchemaVersion: SchemaVersion,
		ID:            newID("nb"),
		Title:         title,
		Cells:         []Cell{},
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

// NewCell builds a fresh cell of the given type with sane defaults —
// model.js's newCell. cellType must be one of CellSQL, CellMarkdown,
// CellA2UI.
func NewCell(cellType CellType) (Cell, error) {
	switch cellType {
	case CellSQL:
		return Cell{ID: newID("cell"), Type: CellSQL, View: "table"}, nil
	case CellMarkdown:
		return Cell{ID: newID("cell"), Type: CellMarkdown}, nil
	case CellA2UI:
		// internal/a2ui.Validate rejects a page with zero components ("must
		// contain at least one component"), so a genuinely empty page would
		// fail Coerce the moment it round-trips through Save — seed one
		// placeholder component so a freshly added a2ui cell is valid (and
		// therefore savable) until the user or agent replaces it.
		return Cell{ID: newID("cell"), Type: CellA2UI, Page: &a2ui.Page{
			Title:      "Untitled",
			Components: []a2ui.Component{{ID: "placeholder", Type: "text", Fields: map[string]any{"text": "Empty card. Edit the source below."}}},
		}}, nil
	default:
		return Cell{}, fmt.Errorf("notebooks: unknown cell type %q", cellType)
	}
}

// decode normalizes raw into a generic JSON value, the same permissive
// input internal/a2ui.Validate accepts: a JSON string, a []byte of JSON, or
// an already-decoded value (map[string]any/[]any/...). Storage.Get builds
// the already-decoded form directly from database columns; ImportNotebook
// and SaveNotebook hand in a JSON string from the frontend.
func decode(raw any) (any, error) {
	switch v := raw.(type) {
	case string:
		var out any
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return out, nil
	case []byte:
		var out any
		if err := json.Unmarshal(v, &out); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return out, nil
	case Notebook:
		return decode(notebookToRaw(v))
	case *Notebook:
		if v == nil {
			return nil, fmt.Errorf("not a notebook: nil")
		}
		return decode(notebookToRaw(*v))
	default:
		return raw, nil
	}
}

// Coerce normalizes arbitrary input into a Notebook, dropping anything
// unrecognized: unknown cell types are dropped, unknown fields discarded,
// `view` clamped to the closed enum, a newer schemaVersion rejected, an
// invalid a2ui page dropped (with a warning) rather than stored broken, and
// the cell count / per-cell source size capped. It never trusts its input —
// this is the ONE path every load (storage.Get, ImportNotebook, SaveNotebook)
// goes through, mirroring model.js's coerceNotebook.
//
// Returns the rebuilt Notebook, a list of human-readable warnings describing
// anything dropped or clamped, and an error only for input malformed enough
// that no notebook could be built at all (not JSON, no cells array, or a
// schemaVersion newer than this package supports).
func Coerce(raw any) (Notebook, []string, error) {
	data, err := decode(raw)
	if err != nil {
		return Notebook{}, nil, err
	}

	obj, isObj := data.(map[string]any)
	if !isObj {
		return Notebook{}, nil, fmt.Errorf("not a notebook: expected a JSON object")
	}

	// An exported file wraps the notebook as {kind, schemaVersion, notebook:
	// {...}}; a bare notebook object (the storage.Get / SaveNotebook shape)
	// is used directly.
	if kind, ok := obj["kind"]; ok {
		if s, ok := kind.(string); !ok || s != FileKind {
			return Notebook{}, nil, fmt.Errorf("unrecognized file kind: %v", kind)
		}
	}
	src := obj
	if nested, ok := obj["notebook"].(map[string]any); ok {
		src = nested
	}

	// schemaVersion is honored wherever it appears: on the wrapper (an
	// export's own schemaVersion, which may be newer even when the nested
	// notebook object omits its own copy) and on the nested notebook object
	// itself. Review fix: only checking src's copy let a wrapper like
	// {"kind":"slaw/notebook","schemaVersion":9,"notebook":{"cells":[]}}
	// (no schemaVersion on the nested object) sail through uncaught.
	for _, holder := range [2]map[string]any{obj, src} {
		if sv, ok := holder["schemaVersion"]; ok {
			if n, ok := toFloat(sv); ok && n > SchemaVersion {
				return Notebook{}, nil, fmt.Errorf("notebook was written by a newer version (schema %v)", sv)
			}
		}
	}

	rawCells, ok := src["cells"].([]any)
	if !ok {
		return Notebook{}, nil, fmt.Errorf("notebook has no cells array")
	}

	var warnings []string

	if len(rawCells) > maxCells {
		warnings = append(warnings, fmt.Sprintf("notebook has %d cells, exceeding the cap of %d — extra cells were dropped", len(rawCells), maxCells))
		rawCells = rawCells[:maxCells]
	}

	cells := make([]Cell, 0, len(rawCells))
	seenIDs := make(map[string]bool, len(rawCells))
	dataBoundBudget := maxDataBoundComponents

	for i, rc := range rawCells {
		cellObj, ok := rc.(map[string]any)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("cell[%d]: not an object, dropped", i))
			continue
		}

		id, _ := cellObj["id"].(string)
		if id == "" {
			id = newID("cell")
		} else if seenIDs[id] {
			// Review fix: a duplicate id (e.g. an import with several cells
			// sharing id "dup") would otherwise let a later cell's edits or
			// deletes silently hit an earlier cell with the same id — every
			// id-keyed operation (delete, upsert-by-id) assumes uniqueness.
			warnings = append(warnings, fmt.Sprintf("cell %q: duplicate id, assigned a new id", id))
			id = newID("cell")
			for seenIDs[id] {
				id = newID("cell")
			}
		}
		seenIDs[id] = true

		collapsed, _ := cellObj["collapsed"].(bool)
		cellType, _ := cellObj["type"].(string)

		switch CellType(cellType) {
		case CellSQL:
			source, truncated, nonString := clampSource(cellObj["source"])
			if nonString {
				warnings = append(warnings, fmt.Sprintf("cell %q: source was not a string, defaulted to empty", id))
			} else if truncated {
				warnings = append(warnings, fmt.Sprintf("cell %q: source exceeded %d bytes, truncated", id, maxCellSourceBytes))
			}
			view, _ := cellObj["view"].(string)
			if !isValidView(view) {
				if view != "" {
					warnings = append(warnings, fmt.Sprintf("cell %q: unknown view %q, defaulted to table", id, view))
				}
				view = "table"
			}
			viewPinned, _ := cellObj["viewPinned"].(bool)
			label, _ := cellObj["label"].(string)
			if clamped, wasTrunc := clampRunes(label, maxLabelRunes); wasTrunc {
				label = clamped
				warnings = append(warnings, fmt.Sprintf("cell %q: label exceeded %d characters, truncated", id, maxLabelRunes))
			}
			// The name is kept as written, even if no such connection exists
			// (a pulled workspace may name one this machine lacks): the cell
			// fails at run time with "unknown connection", it is never dropped
			// and never silently retargeted at the default. It only ever
			// reaches SQL as a quoted identifier.
			connection := ""
			if rawConn, present := cellObj["connection"]; present && rawConn != nil {
				if name, isStr := rawConn.(string); isStr {
					connection = name
				} else {
					warnings = append(warnings, fmt.Sprintf("cell %q: connection was not a string, cleared", id))
				}
			}
			cells = append(cells, Cell{
				ID: id, Type: CellSQL, Collapsed: collapsed,
				Source: source, View: view, ViewPinned: viewPinned, Label: label,
				Connection: connection, Motion: coerceMotion(cellObj["motion"], id, &warnings),
			})
		case CellMarkdown:
			source, truncated, nonString := clampSource(cellObj["source"])
			if nonString {
				warnings = append(warnings, fmt.Sprintf("cell %q: source was not a string, defaulted to empty", id))
			} else if truncated {
				warnings = append(warnings, fmt.Sprintf("cell %q: source exceeded %d bytes, truncated", id, maxCellSourceBytes))
			}
			cells = append(cells, Cell{ID: id, Type: CellMarkdown, Collapsed: collapsed, Source: source})
		case CellA2UI:
			pageRaw, has := cellObj["page"]
			if !has {
				warnings = append(warnings, fmt.Sprintf("cell %q (a2ui): missing page, dropped", id))
				continue
			}
			page, err := a2ui.Validate(pageRaw)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("cell %q (a2ui): invalid page, dropped: %v", id, err))
				continue
			}
			dataBoundBudget = demoteExcessDataBound(&page, dataBoundBudget, id, &warnings)
			cells = append(cells, Cell{ID: id, Type: CellA2UI, Collapsed: collapsed, Page: &page})
		default:
			warnings = append(warnings, fmt.Sprintf("cell %q: unknown type %q, dropped", id, cellType))
		}
	}

	title, _ := src["title"].(string)
	if title == "" {
		title = "Untitled notebook"
	}
	if clamped, wasTrunc := clampRunes(title, maxTitleRunes); wasTrunc {
		title = clamped
		warnings = append(warnings, fmt.Sprintf("notebook title exceeded %d characters, truncated", maxTitleRunes))
	}
	id, _ := src["id"].(string)
	if id == "" {
		id = newID("nb")
	}
	updatedAt, _ := src["updatedAt"].(string)
	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	rev, _ := src["rev"].(string)

	return Notebook{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Title:         title,
		Cells:         cells,
		UpdatedAt:     updatedAt,
		Rev:           rev,
	}, warnings, nil
}

// clampSource coerces v to a string source, truncating at maxCellSourceBytes
// on a valid UTF-8 rune boundary (never splitting a multi-byte rune in
// half), and reports whether it truncated and whether v was present but not
// a string at all (a hand-edited or malicious file's `"source": 12345`).
func clampSource(v any) (result string, truncated bool, nonString bool) {
	if v == nil {
		return "", false, false
	}
	s, ok := v.(string)
	if !ok {
		return "", false, true
	}
	if len(s) <= maxCellSourceBytes {
		return s, false, false
	}
	cut := maxCellSourceBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true, false
}

// clampRunes truncates s to at most maxRunes runes (not bytes — a rune cap
// reads naturally as "N characters" regardless of script), reporting
// whether it truncated.
func clampRunes(s string, maxRunes int) (string, bool) {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s, false
	}
	r := []rune(s)
	return string(r[:maxRunes]), true
}

// coerceMotion keeps an sql cell's motion option when it passes the same
// check an a2ui chart's "motion" field gets, and clears it (with a warning)
// otherwise: a bad animation setting is never a reason to drop the cell.
func coerceMotion(raw any, cellID string, warnings *[]string) map[string]any {
	if raw == nil {
		return nil
	}
	if err := a2ui.ValidateMotion(raw); err != nil {
		*warnings = append(*warnings, fmt.Sprintf("cell %q: invalid motion, cleared: %v", cellID, err))
		return nil
	}
	m, _ := raw.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	return m
}

// demoteExcessDataBound drops the "sql" field from as many of page's
// data-bound components (stat/table/any chart with a "sql" field)
// as needed to stay within budget, the remaining notebook-wide
// maxDataBoundComponents allowance. A demoted component keeps its type and
// other fields — a2ui.Validate already accepted it as valid without "sql"
// (that field is optional on every data-bound type) — it just renders
// static/empty instead of firing a live query. Returns the remaining
// budget after this page.
func demoteExcessDataBound(page *a2ui.Page, budget int, cellID string, warnings *[]string) int {
	// a chart's annotations with their own sql are a second live query of the
	// same component, so they count against the budget on their own
	type binding struct {
		idx   int
		field string
	}
	var bound []binding
	for i, c := range page.Components {
		if _, ok := c.Fields["sql"]; ok {
			bound = append(bound, binding{i, "sql"})
		}
		if ann, ok := c.Fields["annotations"].(map[string]any); ok {
			if _, live := ann["sql"]; live {
				bound = append(bound, binding{i, "annotations"})
			}
		}
	}
	if len(bound) == 0 {
		return budget
	}
	if len(bound) <= budget {
		return budget - len(bound)
	}
	keep := budget
	if keep < 0 {
		keep = 0
	}
	demoted := 0
	for _, b := range bound[keep:] {
		delete(page.Components[b.idx].Fields, b.field)
		demoted++
	}
	*warnings = append(*warnings, fmt.Sprintf(
		"cell %q (a2ui): %d live component(s) exceeded the notebook-wide cap of %d data-bound components across all cells, demoted to static",
		cellID, demoted, maxDataBoundComponents))
	return 0
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		// Rare but seen from hand-edited files: schemaVersion as a string.
		var f float64
		if _, err := fmt.Sscanf(n, "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}
