package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/watches"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// The Watches view's logic (E7 W2, docs/reference/watches.md). The bindings in
// app_watches.go only take the lock and call these; the rules (the strict
// decoder and validator, the compare-and-write store, the history, the check
// itself) live in internal/watches and internal/workspace, shared with
// `slaw watch`. Nothing here runs SQL: a check goes through the read-only
// handler the caller passes to workspace.CheckWatches.

// WatchWhen is a condition on the wire: thresholds travel as the text of the
// file (a JavaScript number would round a large integer), so what the form
// shows is what is compared.
type WatchWhen struct {
	Rows    string   `json:"rows,omitempty"`
	Above   string   `json:"above,omitempty"`
	Below   string   `json:"below,omitempty"`
	Outside []string `json:"outside,omitempty"`
}

// WatchDraft is the New watch / Edit form's answer. Rev is the revision the
// form loaded ("" creates and refuses to replace a file); Overwrite is "Keep
// mine" after a conflict.
type WatchDraft struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Question    watches.Question `json:"question"`
	When        WatchWhen        `json:"when"`
	Rev         string           `json:"rev,omitempty"`
	Overwrite   bool             `json:"overwrite,omitempty"`
}

// WatchSummary is one row of the list: the file's fields, its revision, why it
// could not be read, and the last check from the history.
type WatchSummary struct {
	Name         string           `json:"name"`
	Description  string           `json:"description,omitempty"`
	Question     watches.Question `json:"question"`
	When         WatchWhen        `json:"when"`
	QuestionText string           `json:"question_text,omitempty"`
	WhenText     string           `json:"when_text,omitempty"`
	Rev          string           `json:"rev,omitempty"`
	Error        string           `json:"error,omitempty"`
	// Last is the newest recorded check (its reason, value or row count);
	// LastChecked and LastFired are the times from the whole history.
	Last        *watches.Entry `json:"last,omitempty"`
	LastChecked *time.Time     `json:"last_checked,omitempty"`
	LastFired   *time.Time     `json:"last_fired,omitempty"`
	LastStatus  string         `json:"last_status,omitempty"`
}

// WatchList is the Watches view's list. Alerts counts the watches whose newest
// check fired (the nav badge). HistoryError says the history could not be read;
// the watches still list.
type WatchList struct {
	Watches      []WatchSummary `json:"watches"`
	Alerts       int            `json:"alerts"`
	HistoryError string         `json:"history_error,omitempty"`
}

// WatchDetail is one watch opened: the summary and the file as stored.
type WatchDetail struct {
	WatchSummary
	Text string `json:"text"`
}

// WatchCheckRun is what a check did. Cancelled says Stop (or a close) ended it
// early: the results are the watches checked before it, and the interrupted
// one is left out, because it says nothing about the watch.
type WatchCheckRun struct {
	Results   []workspace.WatchResult `json:"results"`
	Cancelled bool                    `json:"cancelled,omitempty"`
}

func whenOf(c watches.Condition) WatchWhen {
	w := WatchWhen{Rows: c.Rows}
	if c.Above != nil {
		w.Above = c.Above.String()
	}
	if c.Below != nil {
		w.Below = c.Below.String()
	}
	for _, t := range c.Outside {
		w.Outside = append(w.Outside, t.String())
	}
	return w
}

func parseThreshold(label, text string) (*watches.Threshold, error) {
	t, err := watches.ParseThreshold(strings.TrimSpace(text))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return &t, nil
}

// condition reads the form's text with the model's own threshold parser; a
// field left empty is not set, so the validator's "exactly one condition"
// message covers a form with none or several.
func (w WatchWhen) condition() (watches.Condition, error) {
	c := watches.Condition{Rows: w.Rows}
	var err error
	if w.Above != "" {
		if c.Above, err = parseThreshold("above", w.Above); err != nil {
			return c, err
		}
	}
	if w.Below != "" {
		if c.Below, err = parseThreshold("below", w.Below); err != nil {
			return c, err
		}
	}
	labels := []string{"outside (low)", "outside (high)"}
	for i, text := range w.Outside {
		t, perr := parseThreshold(labels[min(i, 1)], text)
		if perr != nil {
			return c, perr
		}
		c.Outside = append(c.Outside, *t)
	}
	return c, nil
}

func (d WatchDraft) watch() (*watches.Watch, error) {
	cond, err := d.When.condition()
	if err != nil {
		return nil, err
	}
	q := d.Question
	params := map[string]string{}
	for k, v := range q.Params { // a blank input is "use the default", not an empty value
		if strings.TrimSpace(v) != "" {
			params[k] = v
		}
	}
	q.Params = nil
	if len(params) > 0 {
		q.Params = params
	}
	w := &watches.Watch{Name: d.Name, Description: d.Description, Question: q, When: cond}
	w.Rev = d.Rev
	return w, nil
}

// previewWatch is the file a draft would write, or the reason it would not be
// written (the same decoder-side validation a save runs).
func previewWatch(d WatchDraft) (string, error) {
	w, err := d.watch()
	if err != nil {
		return "", err
	}
	data, err := watches.Encode(w)
	if err != nil {
		return "", errors.New(watches.ReadableError(err))
	}
	return string(data), nil
}

// saveWatch writes a draft against the revision the form loaded and returns the
// new revision. A stale revision, or a name that exists when creating, is a
// conflict the UI resolves.
func saveWatch(ctx context.Context, st watches.Store, d WatchDraft) (string, error) {
	w, err := d.watch()
	if err != nil {
		return "", err
	}
	var opts []watches.WriteOption
	if d.Overwrite {
		opts = append(opts, watches.WithOverwrite())
	}
	if err := st.Put(ctx, w, opts...); err != nil {
		return "", watchWriteErr(err)
	}
	return w.Rev, nil
}

func deleteWatch(ctx context.Context, st watches.Store, name, rev string) error {
	if rev == "" { // an unconditional delete would remove a file nobody has seen
		return errors.New("reload the list before deleting this watch: the revision shown is unknown")
	}
	return watchWriteErr(st.Delete(ctx, name, watches.WithRev(rev)))
}

// watchWriteErr marks a conflict for the UI and says what is wrong with an
// invalid file in a sentence.
func watchWriteErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, watches.ErrConflict):
		var ce *filestore.ConflictError
		if errors.As(err, &ce) {
			return newConflict(ce.Error())
		}
		return newConflict(watches.ReadableError(err))
	case errors.Is(err, watches.ErrInvalid):
		return errors.New(watches.ReadableError(err))
	}
	return err
}

func questionText(q watches.Question) string {
	switch {
	case q.Query != "":
		return "query " + q.Query
	case q.Metric != "":
		return "metric " + q.Metric
	}
	return ""
}

// summarizeWatch shapes one stored watch with its history.
func summarizeWatch(w watches.Watch, sum map[string]watches.Summary, last map[string]watches.Entry) WatchSummary {
	s := WatchSummary{Name: w.Name, Rev: w.Rev, Error: w.Error}
	if w.Error == "" {
		s.Description, s.Question, s.When = w.Description, w.Question, whenOf(w.When)
		s.QuestionText, s.WhenText = questionText(w.Question), w.When.Describe()
	}
	if m, ok := sum[w.Name]; ok {
		s.LastChecked, s.LastFired, s.LastStatus = m.LastChecked, m.LastFired, string(m.LastStatus)
	}
	if e, ok := last[w.Name]; ok {
		s.Last = &e
	}
	return s
}

// newestEntries is the newest entry of each watch.
func newestEntries(entries []watches.Entry) map[string]watches.Entry {
	out := map[string]watches.Entry{}
	for _, e := range entries {
		if cur, ok := out[e.Watch]; !ok || !e.At.Before(cur.At) {
			out[e.Watch] = e
		}
	}
	return out
}

// listWatches reads the folder and the history. A history that cannot be read
// does not hide the watches.
func listWatches(ctx context.Context, ws workspace.Workspace) (*WatchList, error) {
	list, err := ws.Watches().List(ctx)
	if err != nil {
		return nil, err
	}
	entries, herr := ws.WatchHistory().Entries(ctx, "")
	out := &WatchList{Watches: make([]WatchSummary, 0, len(list))}
	if herr != nil {
		out.HistoryError = herr.Error()
		entries = nil
	}
	sum, last := watches.Summarize(entries), newestEntries(entries)
	for _, w := range list {
		s := summarizeWatch(w, sum, last)
		if s.LastStatus == string(watches.StatusAlert) {
			out.Alerts++
		}
		out.Watches = append(out.Watches, s)
	}
	return out, nil
}

// getWatch opens one watch with its file text; a file that cannot be read is
// still opened, with the reason, so it can be fixed.
func getWatch(ctx context.Context, ws workspace.Workspace, name string) (*WatchDetail, error) {
	src, err := ws.Watches().Source(ctx, name)
	if err != nil {
		return nil, err
	}
	list, err := ws.Watches().List(ctx)
	if err != nil {
		return nil, err
	}
	entries, _ := ws.WatchHistory().Entries(ctx, name)
	sum, last := watches.Summarize(entries), newestEntries(entries)
	d := &WatchDetail{WatchSummary: WatchSummary{Name: name, Rev: src.Rev}, Text: src.Text}
	for _, w := range list {
		if w.Name == name {
			d.WatchSummary = summarizeWatch(w, sum, last)
		}
	}
	d.Rev = src.Rev // the text and its revision were read together; an unreadable file's too
	return d, nil
}

// watchHistory is a watch's past checks, newest first (limit 0 is all).
func watchHistory(ctx context.Context, ws workspace.Workspace, name string, limit int) ([]watches.Entry, error) {
	entries, err := ws.WatchHistory().Entries(ctx, name)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// watchCheckRun turns CheckWatches' (results, error) into what the view gets.
// A stop is a result with Cancelled set, not an error, so the alerts found
// before it still show. The result a stop cut short is marked Interrupted by
// the check itself; the view shows it as "stopped" for this window only.
func watchCheckRun(ctx context.Context, results []workspace.WatchResult, err error) (*WatchCheckRun, error) {
	if err != nil && ctx.Err() == nil {
		return nil, err
	}
	if results == nil {
		results = []workspace.WatchResult{}
	}
	return &WatchCheckRun{Results: results, Cancelled: err != nil}, nil
}

// watchProgress tells the view how a check is going: "running" when a watch
// starts, then its status ("ok", "alert", "error", or "stopped" for the one a
// stop cut short). Only the name and the status are sent, never a row or a reason.
func watchProgress(emit func(string, ...any)) workspace.WatchProgress {
	return func(name string, done *workspace.WatchResult) {
		status := "running"
		if done != nil {
			status = string(done.Status)
			if done.Interrupted {
				status = "stopped"
			}
		}
		emit("watches:progress", map[string]any{"name": name, "status": status})
	}
}
