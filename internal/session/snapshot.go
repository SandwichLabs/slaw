package session

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/sqllex"
)

// This file collects one snapshot fact per connection a run read from, for the
// run's receipt (docs/plans/run-receipts.md, R3). A fact states what the
// connection looked like when the run finished: a DuckLake snapshot id, a
// Postgres WAL position, a file's size and mtime. Each type knows its own fact
// (snapshot_collect.go); the receipts package stores what it is handed and knows
// none of them.
//
// Rules a collector never breaks:
//   - the whole collection has one deadline (SnapshotBudget); a fact that misses
//     it is not_recorded and is never retried, because a later value would
//     describe a different moment;
//   - a connection that is not attached is skipped, never connected to collect;
//   - the queries run on the pool, never on the run's pinned connection and never
//     through the engine, so they cannot change the run's result, move its
//     catalog or mint a receipt of their own;
//   - a value is a version, a position, a size or a time: never a path, a key, a
//     bucket, a host or the text of an error.

// SnapshotBudget is the total time all of one run's collectors share.
const SnapshotBudget = 300 * time.Millisecond

// SnapshotLabel says whether a fact can be used to read the same data again.
// The values are the receipts package's labels (a test keeps them equal).
type SnapshotLabel string

// The snapshot labels.
const (
	SnapshotReplayable  SnapshotLabel = "replayable"
	SnapshotLiveOnly    SnapshotLabel = "live_only"
	SnapshotNotRecorded SnapshotLabel = "not_recorded"
)

// What a fact that was not recorded says instead of a value (never an error's
// text, which can name a host or a path).
const (
	snapshotValueTimeout = "timed out"
	snapshotValueMissing = "unavailable"
	snapshotValuePinned  = "version pinned in the statement"
)

// Snapshot is one fact about a connection's state.
type Snapshot struct {
	Fact  string
	Value string
	Label SnapshotLabel
}

// ConnectionSnapshot is a connection a statement read from and its fact. The
// fact is nil when the connection is not attached: it was not asked.
type ConnectionSnapshot struct {
	Info     ConnectionInfo
	Snapshot *Snapshot
}

// specSource is implemented by the hub: a connection's definition (its path and
// options, which no ConnectionInfo carries), for the collectors only.
type specSource interface {
	snapshotSpec(name string) (Spec, bool)
}

// snapTarget is one connection to collect from and what the statement named in
// it: the string literals that point into a storage scope or at a table.
//
// Only the connection's own definition (spec.Path) is ever collected from; a
// literal in the statement decides whether a connection is listed and which
// objects of a storage scope are asked, and never where a collector looks.
type snapTarget struct {
	info ConnectionInfo
	spec Spec
	urls []string
	// pinned: the statement names a version or time, so "the newest" would not
	// be what it read (replayable types only).
	pinned bool
}

// snapSlots caps the collectors in flight in the whole process, so a burst of
// receipted reads cannot open a burst of connections and metadata requests. A
// run that cannot get a slot within its budget records the fact as not recorded.
var snapSlots = make(chan struct{}, 4)

// SnapshotConnections lists the connections the statement (the text that ran)
// reads from, as ReferencedConnections does, plus the object-storage and table
// connections that a string literal in it points into, and collects each
// attached one's snapshot fact within SnapshotBudget. Sorted by name.
func SnapshotConnections(ctx context.Context, s Session, catalog, sqlText string) []ConnectionSnapshot {
	if s == nil {
		return nil
	}
	src, _ := s.(specSource)
	var targets []snapTarget
	for _, info := range ReferencedConnections(s, catalog, sqlText) {
		t := snapTarget{info: info}
		if src != nil {
			t.spec, _ = src.snapshotSpec(info.Name)
		}
		targets = append(targets, t)
	}
	if src != nil {
		targets = append(targets, literalTargets(s, src, sqlText)...)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].info.Name < targets[j].info.Name })
	if pinsVersion(sqlText) {
		for i := range targets {
			targets[i].pinned = true
		}
	}
	return collectAll(ctx, s, targets, SnapshotBudget)
}

// collectAll runs every attached target's collector at once under one deadline.
// A collector that has not answered when the deadline passes is recorded as
// timed out and left to finish on its own: its answer goes to a channel nobody
// reads, so there is no race with the caller.
func collectAll(ctx context.Context, s Session, targets []snapTarget, budget time.Duration) []ConnectionSnapshot {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	out := make([]ConnectionSnapshot, len(targets))
	chans := make([]chan Snapshot, len(targets))
	for i, t := range targets {
		out[i].Info = t.info
		if t.info.Kind != StatusOK {
			continue
		}
		chans[i] = make(chan Snapshot, 1)
		go func(t snapTarget, ch chan<- Snapshot) {
			select {
			case snapSlots <- struct{}{}:
				defer func() { <-snapSlots }()
			case <-ctx.Done():
				ch <- Snapshot{Fact: collectorFor(t).fact, Label: SnapshotNotRecorded, Value: snapshotValueTimeout}
				return
			}
			ch <- collectOne(ctx, s, t)
		}(t, chans[i])
	}
	for i, t := range targets {
		if chans[i] == nil {
			continue
		}
		var sn Snapshot
		select {
		case sn = <-chans[i]:
		default:
			select {
			case sn = <-chans[i]:
			case <-ctx.Done():
				sn = Snapshot{Fact: collectorFor(t).fact, Label: SnapshotNotRecorded, Value: snapshotValueTimeout}
			}
		}
		out[i].Snapshot = &sn
	}
	return out
}

// collectOne runs one target's collector. A failure, of any kind, is a fact that
// was not recorded; its text is dropped (it can name a host or a path).
func collectOne(ctx context.Context, s Session, t snapTarget) Snapshot {
	c := collectorFor(t)
	if t.pinned && c.replayable {
		return Snapshot{Fact: c.fact, Label: SnapshotNotRecorded, Value: snapshotValuePinned}
	}
	value, label, err := c.run(ctx, s, t)
	switch {
	case ctx.Err() != nil:
		return Snapshot{Fact: c.fact, Label: SnapshotNotRecorded, Value: snapshotValueTimeout}
	case err != nil:
		return Snapshot{Fact: c.fact, Label: SnapshotNotRecorded, Value: snapshotValueMissing}
	}
	return Snapshot{Fact: c.fact, Value: value, Label: label}
}

// literalTargets finds the connections that attach no catalog and that the
// statement reads by name: a storage connection whose scope holds a URL the
// statement reads (in FROM or JOIN position, or as the first argument of a
// read_*, *_scan or read_blob call), and an Iceberg or Delta table connection
// whose location such a literal names. A literal claimed by a table connection
// is not also an object of its storage connection. Merely mentioning a URL
// (a WHERE comparison) lists nothing.
func literalTargets(s Session, src specSource, sqlText string) []snapTarget {
	lits := readLiterals(sqlText)
	if len(lits) == 0 {
		return nil
	}
	var storage, tables []snapTarget
	for _, info := range s.ConnectionInfos() {
		if !info.Catalogless {
			continue
		}
		spec, ok := src.snapshotSpec(info.Name)
		if !ok {
			continue
		}
		t := snapTarget{info: info, spec: spec}
		switch {
		case info.Type == "iceberg" || info.Type == "delta":
			t.urls = literalsAt(lits, spec.Path)
			loc := strings.TrimRight(spec.Path, "/")
			for _, l := range t.urls {
				// A metadata file inside the table is a version the statement chose.
				t.pinned = t.pinned || strings.TrimRight(l, "/") != loc
			}
			if len(t.urls) > 0 {
				tables = append(tables, t)
			}
		case info.Scope != "":
			storage = append(storage, t)
		}
	}
	claimed := map[string]bool{}
	for _, t := range tables {
		for _, u := range t.urls {
			claimed[u] = true
		}
	}
	out := tables
	for _, t := range storage {
		for _, l := range lits {
			if !claimed[l] && objectURL(l) && withinScope(l, t.info.Scope) {
				t.urls = append(t.urls, l)
			}
		}
		if len(t.urls) > 0 {
			sort.Strings(t.urls)
			t.urls = dedupe(t.urls)
			out = append(out, t)
		}
	}
	return out
}

// readLiterals are the string literals a statement reads by name: the one
// after FROM or JOIN, and the first argument (or each element of a first list
// argument) of a read_* or *_scan call (read_blob included).
func readLiterals(sqlText string) []string {
	var out []string
	for _, stmt := range sqllex.Statements(sqlText) {
		for i, t := range stmt {
			switch {
			case t.Kind == sqllex.Word && (t.Text == "FROM" || t.Text == "JOIN"):
				if i+1 < len(stmt) && stmt[i+1].Kind == sqllex.String && stmt[i+1].Text != "" {
					out = append(out, stmt[i+1].Text)
				}
			case t.Kind == sqllex.Word && readsFiles(t.Text) && i+2 < len(stmt) && stmt[i+1].IsParen("("):
				out = append(out, firstArgLiterals(stmt[i+2:])...)
			}
		}
	}
	return out
}

func readsFiles(word string) bool {
	return strings.HasPrefix(word, "READ_") || strings.HasSuffix(word, "_SCAN")
}

// firstArgLiterals is the literal that starts an argument list, or the literals
// of a list that does (['a', 'b']).
func firstArgLiterals(toks []sqllex.Tok) []string {
	if toks[0].Kind == sqllex.String && toks[0].Text != "" {
		return []string{toks[0].Text}
	}
	if !toks[0].IsPunct("[") {
		return nil
	}
	var out []string
	for _, t := range toks[1:] {
		switch {
		case t.IsPunct("]"):
			return out
		case t.Kind == sqllex.String && t.Text != "":
			out = append(out, t.Text)
		case t.IsPunct(","):
		default:
			return out
		}
	}
	return out
}

// pinsVersion reports a statement that names the version or time it reads: the
// version, snapshot_from_id or snapshot_from_timestamp argument of an Iceberg or
// Delta reader, or a DuckLake AT (VERSION|TIMESTAMP => ...). The newest
// snapshot is then not what it read. Deliberately wider than one connection:
// when in doubt the fact is not recorded rather than wrong.
func pinsVersion(sqlText string) bool {
	for _, stmt := range sqllex.Statements(sqlText) {
		for i, t := range stmt {
			if t.Kind != sqllex.Word {
				continue
			}
			switch t.Text {
			case "VERSION", "SNAPSHOT_FROM_ID", "SNAPSHOT_FROM_TIMESTAMP":
				if i+1 < len(stmt) && stmt[i+1].Kind == sqllex.Punct && strings.ContainsAny(stmt[i+1].Text, "=:") {
					return true
				}
			case "AT":
				if i+2 < len(stmt) && stmt[i+1].IsParen("(") && (stmt[i+2].IsWord("VERSION") || stmt[i+2].IsWord("TIMESTAMP")) {
					return true
				}
			}
		}
	}
	return false
}

// literalsAt are the literals that are the location or lie inside it, sorted
// and without duplicates. A literal with a ".." segment never matches. This
// only decides that the table is read: collectors use the connection's own
// location, never a literal.
func literalsAt(lits []string, location string) []string {
	loc := strings.TrimRight(location, "/")
	if loc == "" {
		return nil
	}
	var out []string
	for _, l := range lits {
		if hasDotDot(l) {
			continue
		}
		if t := strings.TrimRight(l, "/"); t == loc || strings.HasPrefix(l, loc+"/") {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

func hasDotDot(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

func dedupe(sorted []string) []string {
	out := sorted[:0:0]
	for i, v := range sorted {
		if i == 0 || v != sorted[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// snapshotSpec implements specSource: a connection's definition, for the
// collectors (a type's file, a table's location). It never leaves the package.
func (h *hub) snapshotSpec(name string) (Spec, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	e, ok := h.conns[name]
	if !ok {
		return Spec{}, false
	}
	return e.spec, true
}
