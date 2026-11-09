package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/repl"
)

// replCmd is the terminal driver of the data console engine (internal/repl):
// the same Session Studio's Console tab renders, on this terminal. It mirrors
// that tab and is for people, not agents: the agent reaches data through MCP
// and never reads a console.
var replCmd = &cobra.Command{
	Use:   "repl",
	Short: "An interactive SQL console on the workspace hub (Studio's console, in the terminal)",
	Long: `An interactive SQL console on the workspace's DuckDB session: the engine of
Studio's Console tab (Query Explorer), driven from this terminal. It is Slaw's own REPL, not a
shell and not the duckdb binary: statements take the same checks and the same
lazy attach as the Query Explorer, and dot commands are a closed set (.help).
Statements end with ';'. Ctrl+C cancels the running statement, Ctrl+D exits.
It needs a terminal on standard input. History is kept per workspace in
.slaw-local/repl/history; see ` + docRef("console"),
	Args:         cobra.NoArgs,
	RunE:         runREPL,
	SilenceUsage: true,
}

func init() {
	replCmd.Flags().StringP("connection", "c", "", "connection whose catalog unqualified names use (default: the workspace's default connection)")
	addWorkspaceFlag(replCmd)
	rootCmd.AddCommand(replCmd)
}

func runREPL(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("workspace")
	conn, _ := cmd.Flags().GetString("connection")
	errW := cmd.ErrOrStderr()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("slaw repl needs a terminal")
	}

	o, err := openWorkspace(ctx, dir, false, false, "slaw repl", errW)
	if err != nil {
		return err
	}
	defer o.Close()
	if conn != "" {
		if _, ok := o.knownConnection(conn); !ok {
			return fmt.Errorf("unknown connection %q; this workspace has: %s", conn, connectionNames(o))
		}
	}

	state, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("failed to put the terminal in raw mode: %w", err)
	}
	defer term.Restore(fd, state) //nolint:errcheck

	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}
	s, err := repl.New(ctx, os.Stdout, repl.Options{
		Engine: o.Eng, Sess: o.Sess,
		Workspace: o.WS.Name(), Root: o.WS.Root(), Queries: o.WS.Queries(),
		Connection: conn, Cols: cols, Rows: rows, Color: true, Logger: logger, Limiter: repl.NewLimiter(1),
		Expand: hub.PersonExpander(o.WS, o.Sess, logger), Receipts: o.WS.Receipts(),
	})
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck
	stopResize := watchResize(fd, s)
	defer stopResize()
	go feedInput(os.Stdin, s)

	<-s.Done()
	fmt.Fprint(os.Stdout, "\r\n")
	return s.Err()
}

// feedInput copies the terminal's keystrokes to the console. In raw mode
// Ctrl+C arrives as the byte 0x03, exactly as it does from a terminal widget.
func feedInput(r io.Reader, s *repl.Session) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 && !deliver(s, buf[:n]) {
			return
		}
		if err != nil {
			_ = s.Close()
			return
		}
	}
}

// deliver hands bytes to the console, waiting while its queue is full (a long
// paste during a long query) rather than giving up on the keyboard, which
// would leave a console nobody can type into or exit. It reports false once
// the console is closed.
func deliver(s *repl.Session, b []byte) bool {
	for {
		err := s.Input(b)
		if err == nil {
			return true
		}
		if !errors.Is(err, repl.ErrInputOverflow) {
			return false
		}
		select {
		case <-s.Done():
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
}
