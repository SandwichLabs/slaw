package repl

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
)

// The SGR sequences the console writes itself. Dim marks NULLs, the type row,
// footers and the timer line; red marks the Error label; bold marks the
// prompt's D. Together with the bracketed-paste switch and clearScreen they
// are the only escapes the console emits: nothing from a value ever is.
const (
	sgrDim   = "\x1b[2m"
	sgrBold  = "\x1b[1m"
	sgrRed   = "\x1b[31m"
	sgrReset = "\x1b[0m"

	clearScreen = "\x1b[2J\x1b[H"
)

// palette applies the colour constants, or nothing.
type palette struct{ on bool }

func (p palette) wrap(sgr, s string) string {
	if !p.on || s == "" {
		return s
	}
	return sgr + s + sgrReset
}

func (p palette) dim(s string) string  { return p.wrap(sgrDim, s) }
func (p palette) bold(s string) string { return p.wrap(sgrBold, s) }
func (p palette) red(s string) string  { return p.wrap(sgrRed, s) }

// cell is one displayed value: safe, one-line text.
type cell struct {
	text string
	null bool
}

// grid is a result ready to draw as a table: every string already safe.
type grid struct {
	headers []string
	types   []string // nil: no type row
	numeric []bool   // right-aligned body cells
	rows    [][]cell
	rowIdx  []int // the true index of each shown row
	elideAt int   // rows[:elideAt] are the head; -1: nothing elided between rows
	footer  []string
	footers bool // draw the footer lines (duckbox, not box)
	total   int  // the true row count
}

// renderOpts are what a draw needs besides the result.
type renderOpts struct {
	st    settings
	width int // columns available (maxwidth already resolved)
	pal   palette
}

// render draws rs (total is the true row count; rs holds at most the fetch
// cap of them) in the session's mode. A statement with no result columns
// prints nothing.
func render(ctx context.Context, w io.Writer, rs *engine.ResultSet, total int, o renderOpts) error {
	if rs == nil || len(rs.Columns) == 0 {
		return nil
	}
	switch o.st.mode {
	case modeCSV:
		return renderCSV(ctx, w, rs, o)
	case modeJSON:
		return renderJSON(ctx, w, rs, o)
	case modeLine:
		return renderLine(ctx, w, rs, o)
	case modeMarkdown:
		return renderMarkdown(ctx, w, transposeIf(buildGrid(rs, total, o.st, false, false), o.st), o)
	case modeBox:
		return renderBox(ctx, w, transposeIf(buildGrid(rs, total, o.st, false, false), o.st), o)
	default:
		return renderBox(ctx, w, transposeIf(buildGrid(rs, total, o.st, true, true), o.st), o)
	}
}

// displayCell is a value as one safe line.
func displayCell(v any, null, dbType string) cell {
	if v == nil {
		return cell{text: safe(null), null: true}
	}
	return cell{text: safe(oneLine.Replace(cellText(v, null, dbType)))}
}

// buildGrid turns rs into a grid. With rowElision it keeps DuckDB's head and
// tail of a long result (the first ceil(maxrows/2) rows and the last
// floor(maxrows/2)) when every row was fetched, else the first maxrows rows.
func buildGrid(rs *engine.ResultSet, total int, st settings, typeRow, rowElision bool) *grid {
	g := &grid{elideAt: -1, footers: rowElision, total: total}
	for _, c := range rs.Columns {
		g.headers = append(g.headers, safe(c.Name))
		g.numeric = append(g.numeric, isNumericType(c.Type))
		if typeRow {
			g.types = append(g.types, safe(typeLabel(c.Type)))
		}
	}
	idx, elideAt := selectRows(len(rs.Rows), total, st.maxrows, rowElision)
	g.elideAt = elideAt
	for _, i := range idx {
		row := make([]cell, len(rs.Columns))
		for c := range row {
			var v any
			if c < len(rs.Rows[i]) {
				v = rs.Rows[i][c]
			}
			row[c] = displayCell(v, st.nullvalue, rs.Columns[c].Type)
		}
		g.rows = append(g.rows, row)
		g.rowIdx = append(g.rowIdx, i)
	}
	if rowElision {
		switch {
		case total == 0:
			g.footer = []string{"0 rows"}
		case len(idx) < total:
			g.footer = []string{fmt.Sprintf("%d rows", total), fmt.Sprintf("(%d shown)", len(idx))}
		}
	}
	return g
}

// selectRows picks which of the n fetched rows to show. total is the true row
// count (more than n once the fetch cap was hit). elideAt is the index in the
// picked rows after which the elision band goes, or -1.
func selectRows(n, total, maxrows int, elide bool) (idx []int, elideAt int) {
	all := func(k int) []int {
		out := make([]int, k)
		for i := range out {
			out[i] = i
		}
		return out
	}
	if !elide || maxrows <= 0 || n <= maxrows {
		return all(n), -1
	}
	if total > n { // the true tail is not available
		return all(maxrows), -1
	}
	head := (maxrows + 1) / 2
	tail := maxrows / 2
	idx = all(head)
	for i := n - tail; i < n; i++ {
		idx = append(idx, i)
	}
	return idx, head
}

// transposeIf is the .columns view: one line per column, one column per row.
// The Type column is there when the grid has a type row.
func transposeIf(g *grid, st settings) *grid {
	if !st.columns {
		return g
	}
	withType := g.types != nil
	t := &grid{elideAt: -1, footer: g.footer, footers: g.footers, total: g.total, headers: []string{"Column"}, numeric: []bool{false}}
	if withType {
		t.headers = append(t.headers, "Type")
		t.numeric = append(t.numeric, false)
	}
	for i := range g.rows {
		if g.elideAt == i {
			t.headers = append(t.headers, "…")
			t.numeric = append(t.numeric, true)
		}
		t.headers = append(t.headers, fmt.Sprintf("Row %d", g.rowIdx[i]+1))
		t.numeric = append(t.numeric, true)
	}
	for c, name := range g.headers {
		row := []cell{{text: name}}
		if withType {
			row = append(row, cell{text: g.types[c]})
		}
		for i := range g.rows {
			if g.elideAt == i {
				row = append(row, cell{text: "…"})
			}
			row = append(row, g.rows[i][c])
		}
		t.rows = append(t.rows, row)
	}
	return t
}

// renderError prints a message under a red label ("Error:"). The message came
// from a statement, a saved query or a connection and is not trusted text.
func renderError(w io.Writer, label, msg string, p palette) {
	fmt.Fprintf(w, "%s %s\n", p.red(label), strings.TrimRight(safeMultiline(msg), "\n"))
}

// RenderOptions are what a surface outside the console (the TUI REPL) sets
// when it draws a result with the console's duckbox renderer.
type RenderOptions struct {
	Width   int // columns available; <= 0 means 80
	MaxRows int // most rows ever drawn; <= 0 means every row
	// TotalUnknown says the rows rs holds are only a lower bound on the result
	// (the caller capped the fetch): a truncated footer reads "MaxRows+ rows".
	TotalUnknown bool
}

// RenderDuckbox draws rs as the console's duckbox table, uncoloured: the one
// formatter every surface shares, with every value through engine.TerminalText
// (the console's safe). When rs holds more than MaxRows rows only the first
// MaxRows are drawn and the footer reports the count of rows rs held. A result
// with no columns draws nothing.
func RenderDuckbox(w io.Writer, rs *engine.ResultSet, o RenderOptions) error {
	if rs == nil || len(rs.Columns) == 0 {
		return nil
	}
	st := defaultSettings()
	st.maxrows = 0
	total := len(rs.Rows)
	if o.MaxRows > 0 && total > o.MaxRows {
		trimmed := *rs
		trimmed.Rows = rs.Rows[:o.MaxRows]
		rs = &trimmed
		st.maxrows = o.MaxRows
	}
	width := o.Width
	if width <= 0 {
		width = 80
	}
	g := buildGrid(rs, total, st, true, true)
	if o.TotalUnknown && st.maxrows > 0 && len(g.footer) > 0 && len(rs.Rows) < total {
		g.footer[0] = fmt.Sprintf("%d+ rows", len(rs.Rows))
	}
	return renderBox(context.Background(), w, g, renderOpts{st: st, width: width})
}
