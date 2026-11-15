package main

// The catalog tree, table capabilities, and what a run changed.

import (
	"context"
	"errors"
	"strings"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/sqllex"
)

// CatalogRequest, CatalogNode and CatalogListing are one lazy level of a
// connection's tree: its schemas, a schema's tables and views, or a table's
// columns. The tree is built by engine.ListCatalog.
type (
	CatalogRequest = engine.CatalogRequest
	CatalogNode    = engine.CatalogNode
	CatalogListing = engine.CatalogListing
)

// ListCatalog returns one lazy level of a connection's tree. It never dials: a
// connection that is not connected is an error the rail answers by connecting
// first.
func (a *App) ListCatalog(req CatalogRequest) (*CatalogListing, error) {
	_, sess, _, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.ctxOrBackground(), connTimeout)
	defer cancel()
	out, err := engine.ListCatalog(ctx, sess, req)
	if err != nil {
		return nil, a.catalogErr(err)
	}
	return out, nil
}

// catalogErr redacts a catalog error for the UI; the "no such connection"
// answer names only what the caller sent and passes through.
func (a *App) catalogErr(err error) error {
	var unknown *engine.UnknownConnectionError
	if errors.As(err, &unknown) {
		return err
	}
	return errors.New(a.redact(err.Error()))
}

// TableCapabilities says whether a table can be edited and, when not, why.
type TableCapabilities = engine.TableCapabilities

// GetTableCapabilities explains why a table is read-only, for the datasheet's
// banner (SPEC 07 E). It never dials.
func (a *App) GetTableCapabilities(table string) (*TableCapabilities, error) {
	_, sess, ro, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	return engine.DescribeTableCapabilities(a.ctxOrBackground(), sess, table, ro)
}

// catalogChanged is the one place a catalog change is announced: the rail
// reloads (catalog:changed) and the completion catalog rebuilds. "" = all.
// It must not block or take a.mu: afterRun calls it under the read lock.
func (a *App) catalogChanged(conn string) {
	a.emitEvent("catalog:changed", map[string]any{"conn": conn})
	if l := a.liveCat.Load(); l != nil {
		l.Invalidate(conn)
	}
}

// afterRun tells the rail what a run changed: a connection that attached (or
// failed) gets its status refreshed and its catalog reported changed, and a run
// that is not a plain read reports the catalogs it may have written: the run's
// connection (conn, "" = the default) and every connection the statement names.
// Only those are announced and rebuilt (#267). Every connection is announced
// where the run's connection is unknown (the console, which can switch
// connections: it passes unknownConn) or the text ATTACHes, DETACHes or USEs.
func (a *App) afterRun(sess session.Session, before map[string]session.StatusKind, sqlText, conn string) {
	moved := a.settleRun(sess, before)
	if !agents.IsReadOnlyStatement(a.ctxOrBackground(), sess.DB(), sqlText, nil) {
		written, all := writtenCatalogs(sess, sqlText, conn)
		if all {
			a.catalogChanged("")
			return
		}
		moved = append(moved, written...)
	}
	a.catalogsChanged(moved)
}

// writtenCatalogs is the connections a non-read run may have written: conn (""
// = the default catalog) plus every registered connection named by an
// identifier of the statement, compared case-insensitively. all reports that
// the run could have changed any of them (conn unknown, or ATTACH/DETACH/USE).
func writtenCatalogs(sess session.Session, sqlText, conn string) (names []string, all bool) {
	if conn == unknownConn {
		return nil, true
	}
	if conn == "" {
		conn = sess.DefaultCatalog()
	}
	names = []string{conn}
	registered := sess.Connections()
	for _, stmt := range sqllex.Statements(sqlText) {
		for _, t := range stmt {
			if t.Kind == sqllex.Word && (t.Text == "ATTACH" || t.Text == "DETACH" || t.Text == "USE") {
				return nil, true
			}
			if !t.IsName() {
				continue
			}
			for _, st := range registered {
				if strings.EqualFold(t.Text, st.Name) {
					names = append(names, st.Name)
				}
			}
		}
	}
	return names, false
}

// unknownConn is afterRun's "the run's connection is not known" marker.
const unknownConn = "\x00"

// catalogsChanged announces each name once.
func (a *App) catalogsChanged(names []string) {
	seen := map[string]bool{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			a.catalogChanged(n)
		}
	}
}

// afterReadOnlyRun is afterRun for a run already vetted as a plain read (an
// artifact query): the S3 URL setting and connection status are settled as
// usual, but a catalog is only reported changed when its connection attached.
func (a *App) afterReadOnlyRun(sess session.Session, before map[string]session.StatusKind) {
	a.catalogsChanged(a.settleRun(sess, before))
}

// settleRun re-checks the S3 URL setting and publishes any connection status
// that moved; it returns the names of the connections whose status did.
func (a *App) settleRun(sess session.Session, before map[string]session.StatusKind) []string {
	// Defence in depth (#138): whatever the run did, the S3 URL setting is on
	// again before the next one. A no-op unless httpfs is loaded. Residual: a
	// session-level override on the editor's own pinned connection is reset only
	// if the pool hands that connection back here; reaching one needs both the
	// text guard and the parser guard (usersql.CheckParsed) to have missed it.
	if err := session.DenyUncoveredStorage(a.ctxOrBackground(), sess.DB()); err != nil && a.logger != nil {
		a.logger.Warn("could not re-check the S3 URL setting after a run", "error", err)
	}
	var moved []string
	for _, st := range sess.Connections() {
		if before[st.Name] != st.Kind {
			moved = append(moved, st.Name)
			a.emitStatus(sess, st.Name)
		}
	}
	return moved
}
