package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/repl"
	"github.com/SandwichLabs/slaw/internal/session"
)

// maxConsoles is how many console tabs one workspace may have open; each pins
// a connection of the hub's pool.
const maxConsoles = 4

// consoleCloseBound is how long closeAll waits for one session to end.
const consoleCloseBound = 5 * time.Second

var (
	errUnknownConsole = errors.New("unknown console")
	consoleIDRe       = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// consoleHost owns the open console sessions of the workspace: the Wails
// bindings in app_repl.go are thin calls into it.
type consoleHost struct {
	mu       sync.Mutex
	sessions map[string]*console
	limiter  *repl.Limiter
	limits   pumpLimits
}

type console struct {
	id     string
	s      *repl.Session
	pump   *outPump
	cancel context.CancelFunc
}

func newConsoleHost() *consoleHost {
	return &consoleHost{sessions: map[string]*console{}, limiter: repl.NewLimiter(maxConsoles), limits: defaultPumpLimits()}
}

func newConsoleID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("failed to make a console id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// open starts a console on connection ("" = the default). The caller holds
// a.mu for reading and has checked that a workspace is open.
func (h *consoleHost) open(a *App, connection string) (string, error) {
	id, err := newConsoleID()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(a.ctxOrBackground())
	c := &console{id: id, cancel: cancel}
	c.pump = newOutPump(func(f replFrame) { a.emitEvent("repl:out:"+id, f) }, h.limits, func() { h.ended(id, "") })
	sess, ws := a.sess, a.ws // under the lock the caller holds: the closure below never reads a
	expand := hub.PersonExpander(ws, sess, a.logger)
	o := repl.Options{
		Engine: a.engine, Sess: sess, Workspace: a.ws.Name(), Root: a.ws.Root(), Queries: a.ws.Queries(),
		Connection: connection, ConnectTimeout: connTimeout, Cols: 80, Rows: 24, Color: true, Logger: a.logger, Limiter: h.limiter,
		Hooks: repl.Hooks{
			AfterRun: func(before map[string]session.StatusKind, text string) { a.afterRun(sess, before, text) },
			Exit:     func() { h.ended(id, "exit") },
			Conn:     func(name string) { a.emitEvent("repl:conn:"+id, name) },
		},
		Complete: completeFromCatalog(a),
		Expand:   expand,
		Receipts: ws.Receipts(),
	}
	h.mu.Lock()
	h.sessions[id] = c
	h.mu.Unlock()
	s, err := repl.New(ctx, c.pump, o)
	if err != nil {
		h.mu.Lock()
		delete(h.sessions, id)
		h.mu.Unlock()
		cancel()
		c.pump.close("failed")
		if errors.Is(err, repl.ErrTooManySessions) {
			return "", fmt.Errorf("At most %d consoles can be open; close one first.", maxConsoles)
		}
		return "", fmt.Errorf("failed to open a console: %w", err)
	}
	h.mu.Lock()
	c.s = s
	_, live := h.sessions[id]
	h.mu.Unlock()
	if !live { // closeAll ran while the session was starting
		cancel()
		c.pump.close("workspace closed")
		go func() { _ = s.Close() }()
		return "", errNoWorkspace
	}
	go func() {
		<-s.Done()
		h.ended(id, reasonFrom(s.Err()))
	}()
	return id, nil
}

// reasonFrom is the closing frame's reason for a session that ended on its own.
func reasonFrom(err error) string {
	switch {
	case err == nil:
		return "exit"
	case errors.Is(err, errReaderGone):
		return errReaderGone.Error()
	case errors.Is(err, context.Canceled):
		return "workspace closed"
	}
	return "the console stopped unexpectedly"
}

// ended forgets the console and closes its stream. Idempotent; reason "" is a
// reader that went away, which also stops the session.
func (h *consoleHost) ended(id, reason string) {
	h.mu.Lock()
	c := h.sessions[id]
	delete(h.sessions, id)
	var s *repl.Session
	if c != nil {
		s = c.s
	}
	h.mu.Unlock()
	if c == nil {
		return
	}
	if reason == "" {
		reason = errReaderGone.Error()
	}
	c.pump.close(reason)
	c.cancel()
	if s != nil {
		go func() { _ = s.Close() }()
	}
}

func (h *consoleHost) get(id string) (*console, error) {
	if !consoleIDRe.MatchString(id) {
		return nil, errUnknownConsole
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.sessions[id]
	if c == nil || c.s == nil {
		return nil, errUnknownConsole
	}
	return c, nil
}

func (h *consoleHost) input(id, data string) error {
	c, err := h.get(id)
	if err != nil {
		return err
	}
	return c.s.Input([]byte(data))
}

func (h *consoleHost) resize(id string, cols, rows int) error {
	c, err := h.get(id)
	if err != nil {
		return err
	}
	if cols < 1 || rows < 1 || cols > 1000 || rows > 1000 {
		return errors.New("that terminal size is out of range")
	}
	c.s.Resize(cols, rows)
	return nil
}

// use switches a console's connection for the "Run on" picker (see
// repl.Session.Use). A refusal comes back as its plain-language text.
func (h *consoleHost) use(id, name string) (string, error) {
	c, err := h.get(id)
	if err != nil {
		return "", err
	}
	if name != "" {
		if err := session.ValidateName(name); err != nil {
			return "", fmt.Errorf("unknown connection %q", name)
		}
	}
	cur, err := c.s.Use(name)
	if errors.Is(err, repl.ErrUseBusy) || errors.Is(err, repl.ErrUseMidStatement) || errors.Is(err, repl.ErrUseLineNotEmpty) {
		return "", errors.New("finish the statement or press Ctrl+C first")
	}
	return cur, err
}

func (h *consoleHost) ack(id string, seq int) error {
	c, err := h.get(id)
	if err != nil {
		return err
	}
	c.pump.ack(seq)
	return nil
}

// close ends one console at the person's request.
func (h *consoleHost) close(id string) error {
	if _, err := h.get(id); err != nil {
		return err
	}
	h.ended(id, "closed")
	return nil
}

// closeAll ends every console (workspace close, switch, quit). The pumps close
// first so a writer blocked on a full window is released; each session gets a
// bounded wait. Nil-safe.
func (h *consoleHost) closeAll(log *slog.Logger) {
	if h == nil {
		return
	}
	h.mu.Lock()
	all := make([]*console, 0, len(h.sessions))
	live := make([]*repl.Session, 0, len(h.sessions))
	for _, c := range h.sessions {
		all = append(all, c)
		if c.s != nil {
			live = append(live, c.s)
		}
	}
	h.sessions = map[string]*console{}
	h.mu.Unlock()
	for _, c := range all {
		c.pump.close("workspace closed")
		c.cancel()
	}
	// All sessions end in parallel under one bound, not one bound each.
	var wg sync.WaitGroup
	for _, s := range live {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Close() }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(consoleCloseBound):
		if log != nil {
			log.Warn("a console did not end in time")
		}
	}
}

// completeFromCatalog is Tab for a console: names from the live completion
// catalog. It never connects a connection and never runs SQL.
func completeFromCatalog(a *App) repl.Completer {
	return func(req repl.CompleteRequest) []string {
		l := a.liveCat.Load()
		if l == nil || req.Pos < 0 || req.Pos > len(req.Text) {
			return nil
		}
		off := len(utf16.Encode([]rune(req.Text[:req.Pos])))
		res := l.Complete(req.Text, off, req.Connection)
		out := make([]string, 0, len(res.Items))
		for _, it := range res.Items {
			if it.Insert != "" {
				continue // a quoted name: the line editor's word rule cannot replace it
			}
			out = append(out, it.Label)
		}
		return out
	}
}
