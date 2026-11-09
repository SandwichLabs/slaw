package repl

import "sort"

// mode is how a result is drawn.
type mode string

const (
	modeDuckbox  mode = "duckbox"
	modeBox      mode = "box"
	modeLine     mode = "line"
	modeCSV      mode = "csv"
	modeJSON     mode = "json"
	modeMarkdown mode = "markdown"
)

// modes is the closed set .mode accepts.
var modes = []mode{modeBox, modeCSV, modeDuckbox, modeJSON, modeLine, modeMarkdown}

func modeNames() []string {
	names := make([]string, len(modes))
	for i, m := range modes {
		names[i] = string(m)
	}
	sort.Strings(names)
	return names
}

func parseMode(s string) (mode, bool) {
	for _, m := range modes {
		if string(m) == s {
			return m, true
		}
	}
	return "", false
}

// settings are a session's display settings (the dot commands change them).
type settings struct {
	mode      mode
	timer     bool
	maxrows   int    // 0 or -1: every fetched row
	maxwidth  int    // 0: the terminal width
	nullvalue string // how NULL reads in every mode but json
	headers   bool   // csv header row
	columns   bool   // transposed rendering
	receipts  bool   // .receipts on: each plain read keeps a receipt (this session only)
}

func defaultSettings() settings {
	return settings{mode: modeDuckbox, maxrows: 40, nullvalue: "NULL", headers: true}
}
