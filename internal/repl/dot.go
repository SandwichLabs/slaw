package repl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/lsp"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/session"
)

// dotError is a dot command's refusal: the label DuckDB's CLI prints and the
// message under it.
type dotError struct{ label, msg string }

func (e *dotError) Error() string { return e.label + " " + e.msg }

func failf(format string, a ...any) error {
	return &dotError{label: "Error:", msg: fmt.Sprintf(format, a...)}
}

// usageErr is the shape DuckDB's CLI gives a malformed dot command.
func usageErr(cmd, usage string) error {
	return &dotError{label: "Invalid Command Error:", msg: fmt.Sprintf("Invalid usage of command '.%s'\n\nUsage: '%s'", cmd, usage)}
}

// dotCommand is one entry of the closed command set. Nothing is ever passed
// through to a shell or to DuckDB's own dot commands.
type dotCommand struct {
	name  string
	usage string
	help  string
	run   func(s *Session, args []string, rest string) error
}

// refusedCommands are DuckDB CLI commands the console answers with a reason.
var refusedCommands = map[string]string{
	"shell":  ".shell is not available: the console runs SQL on the workspace hub, never a shell.",
	"system": ".system is not available: the console runs SQL on the workspace hub, never a shell.",
	"open":   ".open is not available: the console is bound to this workspace's hub; connections are defined in slaw.yaml.",
	"load":   ".load is not available: Slaw loads the DuckDB extensions a connection needs itself.",
	"cd":     ".cd is not available: the console has no working directory.",
	"import": ".import is not available here: use Import (slaw import, or Studio's Import) instead.",
	"output": ".output is not available: the console does not write files; use .export (Query Explorer) instead.",
	"once":   ".once is not available: the console does not write files; use .export (Query Explorer) instead.",
	"log":    ".log is not available here.",
	"excel":  ".excel is not available here.",
	"grid":   ".grid opens the last result in the Query Explorer; it is not available in this console yet.",
	"export": ".export is not available in this console yet; use the Query Explorer's export.",
}

// commandTable is built per lookup: .help lists it, so a package variable
// would be an initialisation cycle.
func commandTable() []dotCommand {
	return []dotCommand{
		{"help", ".help", "show this list", (*Session).dotHelp},
		{"mode", ".mode [box|csv|duckbox|json|line|markdown]", "set how results are drawn", (*Session).dotMode},
		{"timer", ".timer on|off", "show how long each statement took", (*Session).dotTimer},
		{"maxrows", ".maxrows [COUNT]", "rows to draw (0: all fetched)", (*Session).dotMaxrows},
		{"maxwidth", ".maxwidth [COUNT]", "columns to draw within (0: the terminal width)", (*Session).dotMaxwidth},
		{"nullvalue", ".nullvalue STRING", "how NULL is written", (*Session).dotNullvalue},
		{"headers", ".headers on|off", "csv header row", (*Session).dotHeaders},
		{"receipts", ".receipts on|off", "keep a receipt for each read in this session", (*Session).dotReceipts},
		{"columns", ".columns", "draw one line per column, one column per row", func(s *Session, _ []string, _ string) error { s.st.columns = true; return nil }},
		{"rows", ".rows", "draw rows as rows (the default)", func(s *Session, _ []string, _ string) error { s.st.columns = false; return nil }},
		{"tables", ".tables [PATTERN]", "tables and views of the current connection", (*Session).dotTables},
		{"schema", ".schema [PATTERN]", "CREATE statements of the current connection", (*Session).dotSchema},
		{"conn", ".conn [NAME]", "show the connections, or switch to one", (*Session).dotConn},
		{"read", ".read NAME", "run a saved query", (*Session).dotRead},
		{"save", ".save TITLE", "save the last statement as a query", (*Session).dotSave},
		{"clear", ".clear", "clear the screen", (*Session).dotClear},
		{"exit", ".exit", "leave the console", (*Session).dotExit},
		{"quit", ".quit", "leave the console", (*Session).dotExit},
	}
}

// dot runs one dot command line (it starts with "."). It reports whether the
// session goes on.
func (s *Session) dot(line string) bool {
	body := strings.TrimPrefix(line, ".")
	name, rest, _ := strings.Cut(body, " ")
	rest = strings.TrimSpace(rest)
	if reason, ok := refusedCommands[name]; ok {
		s.errorLine("Error:", reason)
		return true
	}
	for _, c := range commandTable() {
		if c.name != name {
			continue
		}
		if err := c.run(s, strings.Fields(rest), rest); err != nil {
			s.reportDotError(err)
		} else if !s.exiting {
			s.hist.record(line) // only a known command that worked, and only if it holds no credential
		}
		return !s.exiting
	}
	s.errorLine("Unknown Command Error:", fmt.Sprintf("Unrecognized command '%s'\nRun '.help' for more information.", name))
	return true
}

func (s *Session) reportDotError(err error) {
	var de *dotError
	if errors.As(err, &de) {
		s.errorLine(de.label, de.msg)
		return
	}
	s.errorLine("Error:", err.Error())
}

func (s *Session) dotHelp(_ []string, _ string) error {
	for _, c := range commandTable() {
		if c.name == "quit" {
			continue
		}
		s.printf("%-42s %s\n", c.usage, c.help)
	}
	s.printf("\nStatements end with ';'. Ctrl+C cancels the running statement. Ctrl+D exits.\n")
	return nil
}

func (s *Session) dotMode(args []string, _ string) error {
	if len(args) == 0 {
		s.printf("current output mode: %s\n", s.st.mode)
		return nil
	}
	m, ok := parseMode(args[0])
	if !ok || len(args) > 1 {
		return failf("mode should be one of: %s", strings.Join(modeNames(), " "))
	}
	s.st.mode = m
	return nil
}

// onOff parses the argument of .timer and .headers.
func onOff(args []string, cmd string) (bool, error) {
	if len(args) == 1 {
		switch strings.ToLower(args[0]) {
		case "on":
			return true, nil
		case "off":
			return false, nil
		}
	}
	return false, usageErr(cmd, "."+cmd+" on|off")
}

func (s *Session) dotTimer(args []string, _ string) error {
	on, err := onOff(args, "timer")
	if err == nil {
		s.st.timer = on
	}
	return err
}

// dotReceipts turns run receipts on or off for this console session only: the
// choice is not remembered, and the prompt shows [receipts] while it is on.
func (s *Session) dotReceipts(args []string, _ string) error {
	if len(args) == 0 {
		if s.st.receipts {
			s.printf("receipts are on\n")
		} else {
			s.printf("receipts are off\n")
		}
		return nil
	}
	on, err := onOff(args, "receipts")
	if err != nil {
		return err
	}
	if on && s.o.Receipts == nil {
		return failf("receipts are not available here (the receipts folder could not be opened)")
	}
	s.st.receipts = on
	s.makePrompts()
	return nil
}

func (s *Session) dotHeaders(args []string, _ string) error {
	on, err := onOff(args, "headers")
	if err == nil {
		s.st.headers = on
	}
	return err
}

func (s *Session) dotMaxrows(args []string, _ string) error {
	if len(args) == 0 {
		s.printf("current max rows: %d\n", s.st.maxrows)
		return nil
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < -1 || len(args) > 1 {
		return usageErr("maxrows", ".maxrows COUNT")
	}
	s.st.maxrows = n
	return nil
}

func (s *Session) dotMaxwidth(args []string, _ string) error {
	if len(args) == 0 {
		s.printf("current max width: %d\n", s.st.maxwidth)
		return nil
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 0 || len(args) > 1 {
		return usageErr("maxwidth", ".maxwidth COUNT")
	}
	s.st.maxwidth = n
	return nil
}

func (s *Session) dotNullvalue(_ []string, rest string) error {
	if rest == "" {
		return usageErr("nullvalue", ".nullvalue STRING")
	}
	s.st.nullvalue = rest
	return nil
}

func (s *Session) dotClear(_ []string, _ string) error {
	s.printf("%s", clearScreen)
	return nil
}

func (s *Session) dotExit(_ []string, _ string) error {
	s.exiting = true
	return nil
}

// dotTables lists the tables and views of the current catalog, packed into
// columns across the terminal.
func (s *Session) dotTables(_ []string, rest string) error {
	tables, err := s.o.Sess.ListTables(s.ctx, s.current, "")
	if err != nil {
		return failf("%s", s.o.Sess.Redact(err.Error()))
	}
	var names []string
	for _, t := range tables {
		if rest == "" || likeMatch(rest, t.Name) {
			names = append(names, safe(t.Name))
		}
	}
	sort.Strings(names)
	for _, l := range packColumns(names, s.width()) {
		s.printf("%s\n", l)
	}
	return nil
}

// schemaSQL is Slaw's own fixed query, not the person's: it takes no gate, no
// hook and no history.
const schemaSQL = `SELECT sql FROM duckdb_tables() WHERE database_name = current_database() AND NOT internal AND table_name LIKE $pattern
UNION ALL
SELECT sql FROM duckdb_views() WHERE database_name = current_database() AND NOT internal AND view_name LIKE $pattern
ORDER BY 1`

func (s *Session) dotSchema(_ []string, rest string) error {
	pattern := rest
	if pattern == "" {
		pattern = "%"
	}
	rs, _, err := s.o.Engine.ExecuteOn(s.ctx, s.conn, s.current, schemaSQL, map[string]any{"pattern": pattern}, fetchCap)
	if err != nil {
		return failf("%s", err.Error())
	}
	for _, row := range rs.Rows {
		if len(row) == 0 || row[0] == nil {
			continue
		}
		text := strings.TrimRight(safeMultiline(cellText(row[0], "")), "\n")
		if !strings.HasSuffix(text, ";") {
			text += ";"
		}
		s.printf("%s\n", text)
	}
	return nil
}

func (s *Session) dotConn(args []string, _ string) error {
	if len(args) == 0 {
		s.printf("current: %s\n", safe(s.current))
		for _, st := range s.o.Sess.Connections() {
			def := ""
			if st.Default {
				def = "  (default)"
			}
			s.printf("  %s  %s%s\n", safe(st.Name), safe(string(st.Kind)), def)
		}
		return nil
	}
	if len(args) > 1 {
		return usageErr("conn", ".conn [NAME]")
	}
	return s.switchTo(args[0])
}

// switchTo makes name the catalog the console's connection works in,
// connecting it first when it is registered but not attached.
func (s *Session) switchTo(name string) error { return s.switchToCtx(s.ctx, name) }

// switchToCtx is switchTo under ctx, so a Ctrl+C can cancel the dial.
func (s *Session) switchToCtx(ctx context.Context, name string) error {
	if err := session.ValidateName(name); err != nil {
		return failf("unknown connection %q", safe(name))
	}
	st, ok := s.o.Sess.Status(name)
	if !ok {
		return failf("unknown connection %q", safe(name))
	}
	if st.Kind != session.StatusOK {
		before := session.StatusSnapshot(s.o.Sess)
		cctx, cancel := s.connectCtx(ctx)
		err := s.o.Sess.Connect(cctx, name)
		cancel()
		if s.o.Hooks.AfterRun != nil {
			s.o.Hooks.AfterRun(before, "USE "+session.QuoteIdent(name))
		}
		if err != nil {
			return failf("%s", err.Error())
		}
	}
	if err := s.conn.Use(ctx, name); err != nil {
		return failf("%s", err.Error())
	}
	s.setCurrent(name)
	return nil
}

func (s *Session) dotRead(_ []string, rest string) error {
	if s.o.Queries == nil {
		return failf("no saved queries here")
	}
	if rest == "" {
		return usageErr("read", ".read NAME")
	}
	q := s.findQuery(rest)
	if q == nil {
		return failf("no saved query %q", safe(rest))
	}
	if names := engine.ParamNames(q.SQL); len(names) > 0 {
		return failf("saved query %q takes parameters (%s); run it from the Query Explorer", safe(rest), strings.Join(names, ", "))
	}
	stmts, tail, _ := lsp.Split(q.SQL)
	if tail != "" {
		stmts = append(stmts, tail)
	}
	for _, st := range stmts {
		if s.ctx.Err() != nil || s.aborted {
			return nil
		}
		s.printf("%s%s\n", s.mainPrompt, safeMultiline(st))
		s.runStatement(st)
	}
	return nil
}

// findQuery is the saved query called name: its id, else its title.
func (s *Session) findQuery(name string) *queries.Query {
	if q, err := s.o.Queries.Get(s.ctx, name); err == nil && q.Error == "" {
		return q
	}
	all, err := s.o.Queries.List(s.ctx)
	if err != nil {
		return nil
	}
	for i := range all {
		if all[i].Error == "" && strings.EqualFold(all[i].Title, name) {
			return &all[i]
		}
	}
	return nil
}

func (s *Session) dotSave(_ []string, rest string) error {
	if s.o.Queries == nil {
		return failf("no saved queries here")
	}
	if rest == "" {
		return usageErr("save", ".save TITLE")
	}
	if s.last == "" {
		return failf("nothing to save: run a statement first")
	}
	q := &queries.Query{Title: rest, SQL: s.last, Engine: "duckdb", Parameters: []queries.Param{}}
	if s.current != s.o.Sess.DefaultCatalog() {
		q.Connection = s.current
	}
	if err := s.o.Queries.Create(s.ctx, q); err != nil {
		return failf("could not save: %s", err.Error())
	}
	s.printf("saved as queries/%s.sql\n", safe(q.ID))
	return nil
}

// likeMatch reports whether name matches a SQL LIKE pattern (% any run, _ any
// one character; case-sensitive, as DuckDB's LIKE). It is linear in the
// pattern times the name, whatever the pattern.
func likeMatch(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
	pi, ni := 0, 0
	star, mark := -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && p[pi] == '%':
			star, mark = pi, ni
			pi++
		case pi < len(p) && (p[pi] == '_' || p[pi] == n[ni]):
			pi++
			ni++
		case star >= 0:
			mark++
			pi, ni = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '%' {
		pi++
	}
	return pi == len(p)
}

// packColumns lays names out in as many columns as fit width, filled down.
func packColumns(names []string, width int) []string {
	if len(names) == 0 {
		return nil
	}
	colW := 0
	for _, n := range names {
		colW = max(colW, cellWidth(n))
	}
	colW += 2
	perRow := max(width/colW, 1)
	rows := (len(names) + perRow - 1) / perRow
	var lines []string
	for r := 0; r < rows; r++ {
		var b strings.Builder
		for c := 0; c < perRow; c++ {
			i := c*rows + r
			if i >= len(names) {
				break
			}
			b.WriteString(padRight(names[i], colW))
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines
}

// connectCtx is the context a dial runs under: the session's, bounded by
// Options.ConnectTimeout when set.
func (s *Session) connectCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if s.o.ConnectTimeout > 0 {
		return context.WithTimeout(parent, s.o.ConnectTimeout)
	}
	return parent, func() {}
}

// setCurrent records the catalog the console works in and tells the surface.
func (s *Session) setCurrent(name string) {
	if name == s.current {
		return
	}
	s.current = name
	if s.o.Hooks.Conn != nil {
		s.o.Hooks.Conn(name)
	}
}
