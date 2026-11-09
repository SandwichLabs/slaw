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
type snapTarget struct {
	info ConnectionInfo
	spec Spec
	urls []string
}

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
// statement still reads: a storage connection whose scope holds a URL literal,
// and an Iceberg or Delta table connection whose location a literal names.
// A literal claimed by a table connection is not also an object of its storage
// connection.
func literalTargets(s Session, src specSource, sqlText string) []snapTarget {
	lits := stringLiterals(sqlText)
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

// stringLiterals are the string literals of a statement, decoded.
func stringLiterals(sqlText string) []string {
	var out []string
	for _, stmt := range sqllex.Statements(sqlText) {
		for _, t := range stmt {
			if t.Kind == sqllex.String && t.Text != "" {
				out = append(out, t.Text)
			}
		}
	}
	return out
}

// literalsAt are the literals that are the location or lie inside it (a table
// folder, or a metadata file in it), sorted and without duplicates.
func literalsAt(lits []string, location string) []string {
	loc := strings.TrimRight(location, "/")
	if loc == "" {
		return nil
	}
	var out []string
	for _, l := range lits {
		if t := strings.TrimRight(l, "/"); t == loc || strings.HasPrefix(l, loc+"/") {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return dedupe(out)
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
