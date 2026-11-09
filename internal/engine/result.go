package engine

// Column describes a single column in a query result.
type Column struct {
	Name string
	Type string // DuckDB type name, e.g. "DECIMAL(12,2)", "INTEGER[]", "UUID"
}

// ResultSet holds the results of a query execution.
type ResultSet struct {
	Columns []Column
	Rows    [][]any
	// ReceiptID is the run's receipt (receipts.Finish), set by a caller that
	// finished one after trimming its rows. The engine never sets it; it rides on
	// the result so a surface can forward it on its own wire type.
	ReceiptID string `json:"-"`
}

// RowCount returns the number of rows in the result set.
func (rs *ResultSet) RowCount() int {
	return len(rs.Rows)
}

// Value returns the value at the given row and column index.
// Returns nil if the indices are out of bounds.
func (rs *ResultSet) Value(row, col int) any {
	if row < 0 || row >= len(rs.Rows) {
		return nil
	}
	if col < 0 || col >= len(rs.Rows[row]) {
		return nil
	}
	return rs.Rows[row][col]
}

// ColumnIndex returns the index of the column with the given name,
// or -1 if no such column exists.
func (rs *ResultSet) ColumnIndex(name string) int {
	for i, c := range rs.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// NamesAndTypes projects Columns into parallel name/DuckDB-type-name slices
// — the shape every tabular Wails binding (TableData, A2UIQueryResult) and
// the agent/MCP run_query result send alongside rows, so a caller doesn't
// have to reconstruct DECIMAL/BIGINT/UUID scale, grouping or editor kind
// from JSON alone.
func (rs *ResultSet) NamesAndTypes() (names, types []string) {
	names = make([]string, len(rs.Columns))
	types = make([]string, len(rs.Columns))
	for i, c := range rs.Columns {
		names[i] = c.Name
		types[i] = c.Type
	}
	return names, types
}
