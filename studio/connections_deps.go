package main

// Connections that read with another connection's secret (a DuckLake or table in
// object storage, session.Spec.Requires) follow it: when the storage connection
// is edited, added or removed, the dependent is registered again, so its scope
// check runs against the new definition.

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// connDeps is what one connection was registered as requiring. Unresolved is
// set when its spec could not be built (for example its URL was outside every
// storage scope): any storage connection that changes may fix it.
type connDeps struct {
	Requires   []string
	Unresolved bool
}

// currentDeps reads the requirements of every connection of ws.
func currentDeps(ws workspace.Workspace) map[string]connDeps {
	out := map[string]connDeps{}
	for _, c := range ws.Connections() {
		if !usesStorageSecret(c.Type) {
			continue
		}
		spec, err := workspace.SessionSpec(ws, c.Name)
		out[c.Name] = connDeps{Requires: spec.Requires, Unresolved: err != nil}
	}
	return out
}

func usesStorageSecret(t string) bool {
	return t == "ducklake" || t == "iceberg" || t == "delta" || t == "iceberg_catalog"
}

// changedNames lists the connections that are new, edited or gone.
func changedNames(cur map[string]workspace.Connection, old, next map[string]string) map[string]string {
	changed := map[string]string{} // name -> type ("" when gone)
	for n := range old {
		if _, ok := cur[n]; !ok {
			changed[n] = "gone"
		}
	}
	for n, c := range cur {
		if old[n] != next[n] {
			changed[n] = c.Type
		}
	}
	return changed
}

// dependentsOf returns the unchanged connections that required (before or now)
// a changed connection, or could not resolve while a storage connection changed.
func dependentsOf(cur map[string]workspace.Connection, changed map[string]string, oldDeps, nextDeps map[string]connDeps) map[string]bool {
	out := map[string]bool{}
	storageChanged := false
	for _, t := range changed {
		storageChanged = storageChanged || workspace.IsStorageType(t)
	}
	for n := range cur {
		if _, ok := changed[n]; ok {
			continue
		}
		for _, d := range []connDeps{oldDeps[n], nextDeps[n]} {
			if d.Unresolved && storageChanged {
				out[n] = true
			}
			for _, r := range d.Requires {
				if _, ok := changed[r]; ok {
					out[n] = true
				}
			}
		}
	}
	return out
}

// Storage detection (#130): the lake, Iceberg and Delta forms ask whether a
// storage connection covers the URL being typed. The binding takes URLs only
// and answers with what ListConnections already shows (a connection's name,
// type, scope, where it is defined and its status word) or the connection to
// add. It reads no credential and no status detail. The typed URL is never
// expanded: one written with ${ENV} references is answered "checked when
// opened". Storage connections' own scopes are expanded as at open time, the
// same rule as the open-time check, so a match can confirm the value of a
// templated scope (only to this person's own frontend).

const (
	maxStorageURLs     = 4
	maxStorageURLBytes = 2048
)

// FindStorageRequest is the URLs to check, in the order to answer.
type FindStorageRequest struct {
	URLs []string `json:"urls"`
}

// StorageMatchView is the covering connection with its live status.
type StorageMatchView struct {
	workspace.StorageMatch
	// Status is the status word only (ok, not-connected, needs-credentials...);
	// the detail text stays in the connection's own editor.
	Status string `json:"status"`
}

// StorageNeedView is the answer for one URL.
type StorageNeedView struct {
	URL     string                       `json:"url"`
	Match   *StorageMatchView            `json:"match,omitempty"`
	Suggest *workspace.StorageSuggestion `json:"suggest,omitempty"`
	Problem string                       `json:"problem,omitempty"`
	// Group numbers the uncovered URLs that share one Suggest.
	Group int `json:"group,omitempty"`
	// Templated says the URL has ${ENV} references: it is not checked here.
	Templated bool `json:"templated,omitempty"`
}

// StorageNeedsView answers a FindStorageRequest, one entry per URL.
type StorageNeedsView struct {
	Needs []StorageNeedView `json:"needs"`
}

// FindStorageFor says, for each storage URL typed into a lake or table form,
// which connection of the workspace covers it or which one to add.
func (a *App) FindStorageFor(req FindStorageRequest) (*StorageNeedsView, error) {
	if err := validateStorageRequest(req); err != nil {
		return nil, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	return storageNeedsView(a.ws, a.sess, a.issues, req.URLs), nil
}

// validateStorageRequest refuses a request that is too large or carries a
// control character (a newline in a URL has no meaning and must never reach a
// log line); the error never repeats the URL.
func validateStorageRequest(req FindStorageRequest) error {
	if len(req.URLs) > maxStorageURLs {
		return fmt.Errorf("at most %d URLs can be checked at once", maxStorageURLs)
	}
	for _, u := range req.URLs {
		if len(u) > maxStorageURLBytes {
			return fmt.Errorf("a URL is longer than %d bytes", maxStorageURLBytes)
		}
		if strings.ContainsFunc(u, unicode.IsControl) {
			return errors.New("a URL contains a control character")
		}
	}
	return nil
}

// storageNeedsView runs workspace.StorageFor over the checkable URLs and puts
// each answer back at its URL's position. An empty URL and a templated one are
// not checked.
func storageNeedsView(ws workspace.Workspace, sess session.Session, issues []workspace.SessionIssue, urls []string) *StorageNeedsView {
	out := &StorageNeedsView{Needs: make([]StorageNeedView, len(urls))}
	var checked []string
	var at []int
	for i, u := range urls {
		out.Needs[i].URL = u
		switch {
		case strings.TrimSpace(u) == "":
			out.Needs[i].Problem = "no URL"
		case strings.Contains(u, "${"):
			out.Needs[i].Templated = true
		default:
			checked, at = append(checked, u), append(at, i)
		}
	}
	status := map[string]string{}
	for _, st := range workspace.ConnectionStates(ws, sess, issues) {
		status[st.Name] = string(st.Status)
	}
	for k, n := range workspace.StorageFor(ws, checked, nil) {
		v := &out.Needs[at[k]]
		v.Suggest, v.Problem, v.Group = n.Suggest, n.Problem, n.Group
		if n.Match != nil {
			v.Match = &StorageMatchView{StorageMatch: *n.Match, Status: status[n.Match.Name]}
		}
	}
	return out
}
