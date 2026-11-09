package receipts

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Note says why a run that should have a receipt has none: a reason class for
// the drawer, never an error message (which could name a path).
type Note string

// The notes. A run with a receipt, or one a receipt is never made for (not a
// single read), has none.
const (
	NoteNone Note = ""
	// NoteNotKept: the receipt could not be made or written (a full or
	// unwritable store).
	NoteNotKept Note = "not_kept"
	// NoteUnavailable: this workspace keeps no receipts at all (its receipts
	// folder cannot be used).
	NoteUnavailable Note = "unavailable"
)

// NoteFor is the note for the outcome of Finish: none when a receipt was kept,
// "not_kept" when Finish failed, "unavailable" when there was no recorder.
func NoteFor(rec *Recorder, id string, err error) Note {
	switch {
	case id != "":
		return NoteNone
	case err != nil:
		return NoteNotKept
	case rec == nil:
		return NoteUnavailable
	}
	return NoteNone
}

// List limits: the default and largest page, and how many files a List reads
// while it looks for matches, so a filter nothing satisfies stays bounded.
const (
	DefaultListLimit = 50
	MaxListLimit     = 500
	MaxListScan      = 2000 // files one List reads
	previewBytes     = 160
)

// ListFilter narrows List. A zero Surface is every surface; a zero or negative
// Limit is DefaultListLimit, and one over MaxListLimit is MaxListLimit.
type ListFilter struct {
	Surface Surface
	Limit   int
}

func (f ListFilter) limit() int {
	switch {
	case f.Limit <= 0:
		return DefaultListLimit
	case f.Limit > MaxListLimit:
		return MaxListLimit
	}
	return f.Limit
}

// Summary is one line of a listing: what was asked and when, never a value.
// SQL is the first line of the statement as written, cut short; it is empty
// when the statement was withheld.
type Summary struct {
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	DurationMS  int64     `json:"duration_ms"`
	Surface     Surface   `json:"surface"`
	Actor       Actor     `json:"actor"`
	Source      Source    `json:"source"`
	Verdict     Verdict   `json:"verdict"`
	Rows        Rows      `json:"rows"`
	Connections []string  `json:"connections,omitempty"`
	SQL         string    `json:"sql,omitempty"`
	SQLWithheld bool      `json:"sql_withheld,omitempty"`
}

// Summarize is the listing line of r.
func Summarize(r Receipt) Summary {
	s := Summary{
		ID: r.ID, At: r.At, DurationMS: r.DurationMS, Surface: r.Surface, Actor: r.Actor,
		Source: r.Source, Verdict: r.Verdict, Rows: r.Rows, SQLWithheld: r.SQLWithheld,
	}
	for _, c := range r.Connections {
		s.Connections = append(s.Connections, c.Name)
	}
	s.SQL = firstLine(r.SQL)
	return s
}

// firstLine is the first non-blank line of text, cut to previewBytes on a rune
// boundary.
func firstLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if len(l) > previewBytes {
			cut := previewBytes
			for cut > 0 && !utf8.RuneStart(l[cut]) {
				cut--
			}
			l = l[:cut] + "…"
		}
		return l
	}
	return ""
}

// sortNewest puts the newest receipt first (equal times by id, so the order is
// stable).
func sortNewest(out []Summary) {
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
}

// List returns the newest receipts, newest first, narrowed by the filter. It
// orders the files by modification time (names and sizes only, no file opened)
// and decodes only as many as it takes to fill the page, at most MaxListScan, so
// a large store and a filter nothing matches are both bounded. A file that does
// not decode is skipped. capped says the scan stopped at MaxListScan files with
// the page not full and more files unread: an empty or short answer then means
// "none among the newest", not "none".
func (s *FileStore) List(f ListFilter) (out []Summary, capped bool, err error) {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, false, err
	}
	defer root.Close() //nolint:errcheck
	entries, err := list(root, false)
	if err != nil {
		return nil, false, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].mod.Equal(entries[j].mod) {
			return entries[i].mod.After(entries[j].mod)
		}
		return entries[i].rel > entries[j].rel
	})
	limit := f.limit()
	out = make([]Summary, 0, min(limit, len(entries)))
	for i, e := range entries {
		if len(out) >= limit {
			break
		}
		if i >= MaxListScan {
			capped = true
			break
		}
		r, ok := readEntry(root, e)
		if !ok || (f.Surface != "" && r.Surface != f.Surface) {
			continue
		}
		out = append(out, Summarize(r))
	}
	sortNewest(out)
	return out, capped, nil
}

// readEntry decodes one listed receipt file through root; ok false for a file
// that is gone, too big or not a receipt.
func readEntry(root *os.Root, e entry) (Receipt, bool) {
	if e.size > MaxFileBytes {
		return Receipt{}, false
	}
	fh, err := root.Open(e.rel)
	if err != nil {
		return Receipt{}, false
	}
	defer fh.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(fh, MaxFileBytes+1))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Receipt{}, false
	}
	r, err := Decode(data)
	if err != nil || !strings.HasPrefix(e.rel[3:], r.ID) {
		return Receipt{}, false
	}
	return r, true
}
