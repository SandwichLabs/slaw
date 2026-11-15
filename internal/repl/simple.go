package repl

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
)

// renderLine prints one "name = value" line per column, a blank line between
// rows.
func renderLine(ctx context.Context, w io.Writer, rs *engine.ResultSet, o renderOpts) error {
	nameW := 5
	names := make([]string, len(rs.Columns))
	for i, c := range rs.Columns {
		names[i] = safe(c.Name)
		nameW = max(nameW, cellWidth(names[i]))
	}
	for r, row := range rs.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r > 0 {
			fmt.Fprintln(w)
		}
		for i := range rs.Columns {
			var v any
			if i < len(row) {
				v = row[i]
			}
			c := displayCell(v, o.st.nullvalue, rs.Columns[i].Type)
			text := c.text
			if c.null {
				text = o.pal.dim(text)
			}
			fmt.Fprintf(w, "%s = %s\n", padLeft(names[i], nameW), text)
		}
	}
	return nil
}

// renderCSV prints RFC 4180 records with \n line ends; values keep their line
// breaks inside quotes.
func renderCSV(ctx context.Context, w io.Writer, rs *engine.ResultSet, o renderOpts) error {
	cw := csv.NewWriter(w)
	if o.st.headers {
		head := make([]string, len(rs.Columns))
		for i, c := range rs.Columns {
			head[i] = safe(c.Name)
		}
		if err := cw.Write(head); err != nil {
			return err
		}
	}
	for _, row := range rs.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		rec := make([]string, len(rs.Columns))
		for i := range rec {
			var v any
			if i < len(row) {
				v = row[i]
			}
			if v == nil {
				rec[i] = safeMultiline(o.st.nullvalue)
				continue
			}
			rec[i] = safeMultiline(cellText(v, "", rs.Columns[i].Type))
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// renderJSON prints an array with one object per line. NULL is null whatever
// nullvalue is, and every string is made terminal-safe before it is encoded.
func renderJSON(ctx context.Context, w io.Writer, rs *engine.ResultSet, o renderOpts) error {
	var out strings.Builder
	out.WriteString("[")
	keys := make([]string, len(rs.Columns))
	for i, c := range rs.Columns {
		keys[i] = safeMultiline(c.Name)
	}
	for r, row := range rs.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r > 0 {
			out.WriteString(",\n")
		}
		out.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				out.WriteString(",")
			}
			var v any
			if i < len(row) {
				v = row[i]
			}
			kb, err := encodeJSON(k)
			if err != nil {
				return err
			}
			vb, err := encodeJSON(jsonValue(v))
			if err != nil {
				return err
			}
			out.WriteString(kb + ":" + vb)
		}
		out.WriteString("}")
	}
	out.WriteString("]\n")
	_, err := io.WriteString(w, out.String())
	return err
}

func encodeJSON(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("failed to encode value: %w", err)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// jsonValue is v with every string made terminal-safe and anything JSON cannot
// carry (NaN, an unknown type) spelled as text.
func jsonValue(v any) any {
	switch x := v.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return x
	case json.Number:
		return x
	case string:
		return safeMultiline(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return cellText(x, "", "")
		}
		return x
	case float32:
		return jsonValue(float64(x))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[safeMultiline(k)] = jsonValue(e)
		}
		return out
	}
	return safeMultiline(cellText(v, "", ""))
}

// renderMarkdown prints a pipe table: numbers right-aligned.
func renderMarkdown(ctx context.Context, w io.Writer, g *grid, _ renderOpts) error {
	n := len(g.headers)
	widths := make([]int, n)
	for i, h := range g.headers {
		widths[i] = max(cellWidth(mdCell(h)), 1)
		for _, r := range g.rows {
			widths[i] = max(widths[i], cellWidth(mdCell(r[i].text)))
		}
	}
	head := make([]string, n)
	sep := make([]string, n)
	for i, h := range g.headers {
		head[i] = " " + center(mdCell(h), widths[i]) + " "
		if g.numeric[i] {
			sep[i] = strings.Repeat("-", widths[i]+1) + ":"
		} else {
			sep[i] = strings.Repeat("-", widths[i]+2)
		}
	}
	fmt.Fprintln(w, "|"+strings.Join(head, "|")+"|")
	fmt.Fprintln(w, "|"+strings.Join(sep, "|")+"|")
	for _, r := range g.rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		parts := make([]string, n)
		for i := range parts {
			t := mdCell(r[i].text)
			if g.numeric[i] {
				parts[i] = " " + padLeft(t, widths[i]) + " "
			} else {
				parts[i] = " " + padRight(t, widths[i]) + " "
			}
		}
		fmt.Fprintln(w, "|"+strings.Join(parts, "|")+"|")
	}
	return nil
}

// mdCell keeps a literal pipe from ending its cell.
func mdCell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
