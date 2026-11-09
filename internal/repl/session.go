package repl

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/lsp"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/session"
)

// Options configures one console session.
type Options struct {
	Engine     *engine.Manager // required
	Sess       session.Session // required
	Workspace  string          // the workspace name, for the prompt ("" = no name)
	Root       string          // workspace root; history lives in Root/.slaw-local/repl/history ("" = no history)
	Queries    queries.Store   // for .read and .save (nil: both answer "no saved queries here")
	Connection string          // initial catalog ("" = the session default)
	Cols, Rows int             // terminal size (0 = 80x24)
	Color      bool            // emit the SGR sequences (dim, bold, red)
	Logger     *slog.Logger    // debug lines only, never statement text; nil discards
	Hooks      Hooks
	Limiter    *Limiter         // nil = unlimited
	Complete   Completer        // nil = Tab does nothing
	Clock      func() time.Time // nil = time.Now (the .timer line in tests)
	// Expand, when set, rewrites a typed statement before the editor's gate
	// sees it (metric() calls become the metric's saved query). connection is
	// the catalog the console works in. It returns the text to check and run
	// and the parameter values it needs; an error is shown and nothing runs.
	// The history keeps what was typed.
	Expand func(ctx context.Context, text, connection string) (string, map[string]any, error)
	// Receipts keeps the receipts of the statements this console runs while the
	// person has turned them on (.receipts on, per session, off by default).
	// nil: the command answers that receipts are not available here.
	Receipts *receipts.Recorder
	// ConnectTimeout bounds dialling a connection that is not attached yet
	// (opening on one, .conn, Use). 0 = no bound beyond the session's context.
	ConnectTimeout time.Duration
}

// Hooks are what the embedding surface does around a run. Both may be nil.
type Hooks struct {
	// AfterRun is called after every statement that passed the gates, whether
	// it succeeded or not, with the status snapshot taken just before the run
	// (session.StatusSnapshot) and the statement text. Studio passes its
	// afterRun (rail refresh, catalog:changed, the live completion catalog). A
	// switch to a connection that had to be dialled calls it with a USE
	// statement.
	AfterRun func(before map[string]session.StatusKind, sqlText string)
	// Exit is called once when the person types .exit/.quit or Ctrl+D on an
	// empty line, before the session ends. Studio closes the tab.
	Exit func()
	// Conn is called, on the session's goroutine, whenever the catalog the
	// console works in changes: .conn, a USE statement, Use. Studio tells the
	// Query Explorer so its picker never disagrees with the console.
	Conn func(name string)
}

// CompleteRequest is what Tab asks about: the statement typed so far (the
// earlier lines of an unfinished statement, then the current line) with Pos
// the byte offset of the cursor in Text, and the catalog the console works in.
type CompleteRequest struct {
	Text       string
	Pos        int
	Connection string
}

// Completer answers Tab: candidates for the word ending at req.Pos.
type Completer func(req CompleteRequest) []string

// Session is one console: a line editor, a pinned connection and the loop
// between them. Create it with New; feed it with Input; end it with Close.
type Session struct {
	o      Options
	log    *slog.Logger
	ctx    context.Context
	stop   context.CancelFunc
	parent context.Context

	sink *sinkWriter
	out  *bufio.Writer // statement output, CRLF line ends
	term *term.Terminal
	in   *inputQueue
	flt  inputFilter
	inMu sync.Mutex // one Input at a time: the filter's state and the queue write go together
	// busy is true while the loop handles a line (a statement, a dot command).
	busy atomic.Bool
	// useReq is the pending Use request, served on the loop's goroutine (use.go).
	useMu  sync.Mutex
	useReq *useRequest

	conn    *session.Conn
	current string // the catalog the pinned connection works in
	st      settings
	hist    *history
	last    string // the last statement .save offers
	buf     string // the statement being typed, lines so far

	mainPrompt, contPrompt string
	exiting                bool // set by .exit/.quit
	left                   bool // the person left (.exit, Ctrl+D): Exit is called once the loop is over
	aborted                bool // a Ctrl+C cancelled the statement: the rest of the batch is dropped

	cancel      atomic.Pointer[context.CancelFunc] // set while a statement is handled
	interrupted atomic.Bool
	dropRest    atomic.Bool // a Ctrl+C cancelled a statement: discard what was typed ahead
	running     atomic.Bool
	closed      atomic.Bool
	cols, rows  atomic.Int32

	done      chan struct{}
	closeOnce sync.Once
	errMu     sync.Mutex
	err       error
}

// sinkWriter is the session's one door to the caller's writer. The first write
// error ends the session and is kept.
type sinkWriter struct {
	w    io.Writer
	s    *Session
	mu   sync.Mutex
	fail error
}

func (k *sinkWriter) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail != nil {
		return 0, k.fail
	}
	n, err := k.w.Write(p)
	if err != nil {
		k.fail = err
		k.s.setErr(fmt.Errorf("failed to write to the console: %w", err))
		k.s.stop()
	}
	return n, err
}

// crlfWriter turns each line feed into CR LF, which a terminal needs.
type crlfWriter struct{ w io.Writer }

func (c crlfWriter) Write(p []byte) (int, error) {
	if _, err := c.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// editorWriter is what the line editor draws through: the marker rune of an
// interrupt is never shown. (What the person types is cleaned on the way in,
// by inputFilter.)
type editorWriter struct{ k *sinkWriter }

func (e editorWriter) Write(p []byte) (int, error) {
	if _, err := e.k.Write(bytes.ReplaceAll(p, []byte(string(interruptRune)), nil)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// New pins a connection, loads history, writes the banner and the first
// prompt, and starts the session's goroutine. out receives VT bytes; writes
// come from one goroutine, may block, and a write error ends the session.
func New(ctx context.Context, out io.Writer, o Options) (*Session, error) {
	if o.Engine == nil || o.Sess == nil {
		return nil, errors.New("a console needs an engine and a session")
	}
	if o.Connection != "" {
		if err := session.ValidateName(o.Connection); err != nil {
			return nil, fmt.Errorf("unknown connection %q", o.Connection)
		}
	}
	if err := o.Limiter.acquire(); err != nil {
		return nil, err
	}
	s, err := open(ctx, out, o)
	if err != nil {
		o.Limiter.release()
		return nil, err
	}
	go s.run()
	return s, nil
}

// open builds a Session and everything it holds; on error it has released
// whatever it took.
func open(parent context.Context, out io.Writer, o Options) (*Session, error) {
	hist, histNotice := openHistory(o.Root)
	conn, err := o.Sess.Conn(parent)
	if err != nil {
		return nil, fmt.Errorf("failed to pin a connection for the console: %w", err)
	}
	ctx, stop := context.WithCancel(parent)
	s := &Session{o: o, parent: parent, ctx: ctx, stop: stop, conn: conn, hist: hist,
		current: o.Sess.DefaultCatalog(), st: defaultSettings(), in: newInputQueue(), done: make(chan struct{})}
	s.log = o.Logger
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.sink = &sinkWriter{w: out, s: s}
	s.out = bufio.NewWriterSize(crlfWriter{s.sink}, 64<<10)
	cols, rows := o.Cols, o.Rows
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	s.cols.Store(int32(cols))
	s.rows.Store(int32(rows))
	context.AfterFunc(ctx, s.in.close)

	s.term = term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{s.in, editorWriter{s.sink}}, "")
	s.term.History = termHistory{hist}
	_ = s.term.SetSize(cols, rows)
	s.term.AutoCompleteCallback = s.complete // Tab, and the Use request (use.go)
	s.makePrompts()
	if o.Connection != "" && o.Connection != s.current {
		if err := s.switchTo(o.Connection); err != nil {
			stop()
			_ = conn.Close()
			return nil, err
		}
	}
	s.term.SetBracketedPasteMode(true)
	s.banner()
	if histNotice != "" {
		s.printf("%s\n", s.pal().dim("History is off: "+safe(histNotice)+"."))
	}
	return s, nil
}

func (s *Session) clock() time.Time {
	if s.o.Clock != nil {
		return s.o.Clock()
	}
	return time.Now()
}

func (s *Session) pal() palette { return palette{on: s.o.Color} }

func (s *Session) width() int { return int(s.cols.Load()) }

func (s *Session) renderOpts() renderOpts {
	w := s.st.maxwidth
	if w <= 0 {
		w = s.width()
	}
	return renderOpts{st: s.st, width: w, pal: s.pal()}
}

// printf writes to the statement output (buffered; flushed before the next
// prompt).
func (s *Session) printf(format string, a ...any) { fmt.Fprintf(s.out, format, a...) }

// errorLine prints a label (red) and a message that is not trusted text.
func (s *Session) errorLine(label, msg string) {
	renderError(s.out, label, msg, s.pal())
}

func (s *Session) makePrompts() {
	s.mainPrompt, s.contPrompt = prompts(s.o.Workspace, s.pal(), s.st.receipts)
}

// prompts are the main and continuation prompts: "<workspace> ▸ D " and the
// same width of spaces ending in "· ". The workspace name is not trusted text.
// While receipts are on the main prompt says so ("D [receipts] ").
func prompts(workspace string, pal palette, receiptsOn bool) (main, cont string) {
	d := pal.bold("D")
	tag := ""
	if receiptsOn {
		tag = " " + receiptsTag
	}
	if workspace == "" {
		return d + tag + " ", strings.Repeat(" ", cellWidth(tag)) + "· "
	}
	name := runewidth.Truncate(safe(workspace), 32, "…")
	return name + " ▸ " + d + tag + " ", strings.Repeat(" ", cellWidth(name)+3+cellWidth(tag)) + "· "
}

// receiptsTag is what the prompt shows while .receipts is on.
const receiptsTag = "[receipts]"

func bannerText(workspace string) string {
	who := ""
	if workspace != "" {
		who = " on " + runewidth.Truncate(safe(workspace), 32, "…")
	}
	return "Slaw console" + who + ". Statements end with ';'. Enter .help for commands."
}

func (s *Session) banner() { s.printf("%s\n", s.pal().dim(bannerText(s.o.Workspace))) }

// complete is the line editor's Tab hook (the seam R4 grows).
func (s *Session) complete(line string, pos int, key rune) (string, int, bool) {
	if key == useRune {
		s.serveUse(line)
		return line, pos, true // the key is a signal, never text
	}
	if key != '\t' || s.o.Complete == nil || pos < 0 || pos > len(line) {
		return "", 0, false
	}
	prior := s.buf // the earlier lines; lsp.Split may have trimmed their last newline
	if prior != "" && !strings.HasSuffix(prior, "\n") {
		prior += "\n"
	}
	cands := s.o.Complete(CompleteRequest{Text: prior + line, Pos: len(prior) + pos, Connection: s.current})
	if len(cands) == 0 {
		return "", 0, false
	}
	start := pos
	for start > 0 && lspWordByte(line[start-1]) {
		start--
	}
	word := commonPrefix(cands)
	if len(cands) > 1 && len(word) <= pos-start {
		return "", 0, false
	}
	return line[:start] + word + line[pos:], start + len(word), true
}

func lspWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func commonPrefix(ss []string) string {
	p := ss[0]
	for _, s := range ss[1:] {
		i := 0
		for i < len(p) && i < len(s) && p[i] == s[i] {
			i++
		}
		p = p[:i]
	}
	return p
}

// Resize sets the terminal size. Safe from any goroutine.
func (s *Session) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	s.cols.Store(int32(cols))
	s.rows.Store(int32(rows))
	_ = s.term.SetSize(cols, rows)
}

// Done is closed when the session's goroutine has exited.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the session ended (nil for .exit, Ctrl+D and Close).
func (s *Session) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

func (s *Session) setErr(err error) {
	s.errMu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.errMu.Unlock()
}

// Close cancels a running statement, ends the loop, releases the pinned
// connection, turns bracketed paste off and flushes history. Idempotent;
// returns after the goroutine has exited.
func (s *Session) Close() error {
	s.closed.Store(true)
	s.stop()
	if c := s.cancel.Load(); c != nil {
		(*c)()
	}
	s.in.close()
	<-s.done
	return nil
}

// run is the session's goroutine. Exit is called last, after Done is closed,
// so a hook that closes the session (Studio closing the tab) cannot deadlock.
func (s *Session) run() {
	s.guarded()
	s.finish()
	close(s.done)
	if s.left && s.o.Hooks.Exit != nil {
		s.o.Hooks.Exit()
	}
}

// guarded runs the loop and turns a panic into an ended session.
func (s *Session) guarded() {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("console loop panicked", slog.Any("panic", r))
			s.setErr(errors.New("the console stopped unexpectedly"))
		}
	}()
	s.loop()
}

// finish releases everything the session holds. It runs once, on the
// session's goroutine, however the loop ended.
func (s *Session) finish() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.stop()
		s.in.close()
		s.term.SetBracketedPasteMode(false)
		_ = s.out.Flush()
		if err := s.parent.Err(); err != nil {
			s.setErr(err)
		}
		if err := s.conn.Close(); err != nil {
			s.log.Debug("could not release the console connection")
		}
		s.o.Limiter.release()
	})
}

// loop reads lines until the session ends.
func (s *Session) loop() {
	for s.ctx.Err() == nil {
		s.term.SetPrompt(s.prompt())
		_ = s.out.Flush()
		line, err := s.term.ReadLine()
		if err != nil && !errors.Is(err, term.ErrPasteIndicator) {
			if errors.Is(err, io.EOF) && s.ctx.Err() == nil {
				s.left = true // Ctrl+D on an empty line
			} else if !errors.Is(err, io.EOF) {
				s.setErr(err)
			}
			return
		}
		line = strings.ReplaceAll(line, string(useRune), "") // a signal, never text
		if line == string(interruptRune) {
			s.buf = "" // Ctrl+C at the prompt: forget the half-typed statement
			continue
		}
		s.busy.Store(true)
		more := s.handleLine(line)
		s.busy.Store(false)
		if !more {
			s.left = true
			return
		}
	}
}

func (s *Session) prompt() string {
	if s.buf != "" {
		return s.contPrompt
	}
	return s.mainPrompt
}

// handleLine takes one line of input: a dot command, or a piece of SQL that
// runs once a ';' completes a statement. It reports whether the session goes
// on.
func (s *Session) handleLine(line string) bool {
	if s.buf == "" {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, ".") {
			s.aborted = false
			return s.dot(t)
		}
	}
	s.buf += line + "\n"
	stmts, rest, _ := lsp.Split(s.buf)
	s.buf = rest
	s.aborted = false
	for _, st := range stmts {
		if s.ctx.Err() != nil || s.aborted {
			break
		}
		s.runStatement(st)
	}
	return true
}
