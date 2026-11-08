package main

// Connections (#117): the bindings behind the connections rail, the connection
// editor, the sign-in and extension prompts and the connection selector.
//
// Write-only contract (plan 4.3): no binding here returns a secret. A password,
// token or secret key can be set, replaced and cleared, and the status says
// whether one is set; the username and key id are not secrets and are readable.
// Every error that reaches the UI is redacted.
//
// This file: the connection types, list, read, save, move and delete.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/connections"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// ServerChange says a connection's server differs from the one the saved
// sign-in was made for.
type ServerChange struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// TypeSpec is one connection type for the editor's picker and form.
type TypeSpec struct {
	ID                string                   `json:"id"`
	Label             string                   `json:"label"`
	Description       string                   `json:"description"`
	Fields            []connections.Field      `json:"fields"`
	Credentials       []connections.Field      `json:"credentials"`
	RequiresExtension []string                 `json:"requiresExtension"`
	Capabilities      connections.Capabilities `json:"capabilities"`
	FingerprintFields []string                 `json:"fingerprintFields"`
	SignInMethods     []string                 `json:"signInMethods"`
	// Presets fill the s3 form for a known S3-compatible store (empty for
	// every other type).
	Presets []connections.Preset `json:"presets"`
	// MetadataDriven marks a type whose table or catalog metadata decides which
	// files DuckDB reads (workspace.MetadataDrivenType); Warning is the
	// plain-words warning the form shows for it (workspace.MetadataWarning).
	MetadataDriven bool   `json:"metadataDriven"`
	Warning        string `json:"warning,omitempty"`
	// Soon marks a type that is shown but cannot be chosen yet. Every type of
	// the registry is available since #119; the flag stays for the picker.
	Soon bool `json:"soon"`
}

var typeDescriptions = map[string]string{
	"duckdb":          "A DuckDB database file on this computer.",
	"postgres":        "A PostgreSQL server. Read it in place; nothing is copied.",
	"motherduck":      "A MotherDuck cloud database.",
	"sqlite":          "A SQLite database file on this computer.",
	"s3":              "Files in an Amazon S3 bucket.",
	"gcs":             "Files in a Google Cloud Storage bucket.",
	"r2":              "Files in a Cloudflare R2 bucket.",
	"mysql":           "A MySQL or MariaDB server. Read it in place; nothing is copied.",
	"azure":           "Files in Azure Blob Storage or Data Lake (az://, azure://, abfss://).",
	"ducklake":        "A DuckLake: tables in Parquet files with their catalog in a DuckDB or SQLite file, here or in object storage.",
	"iceberg":         "An Iceberg table, read with iceberg_scan('<location>').",
	"delta":           "A Delta Lake table, read with delta_scan('<location>').",
	"iceberg_catalog": "An Iceberg catalog (Amazon S3 Tables or a REST catalog): its tables are listed and read through the catalog, like a database.",
}

// ListConnectionTypes returns the connection types, in the order the picker
// shows them, generated from the type registry.
func (a *App) ListConnectionTypes() []TypeSpec {
	infos := map[string]connections.TypeInfo{}
	for _, i := range connections.ListConnectionTypes() {
		infos[i.Name] = i
	}
	order := []string{"duckdb", "postgres", "mysql", "motherduck", "sqlite", "ducklake", "iceberg_catalog", "s3", "gcs", "r2", "azure", "iceberg", "delta"}
	out := make([]TypeSpec, 0, len(order))
	for _, id := range order {
		i, ok := infos[id]
		if !ok {
			continue
		}
		methods := i.SignInMethods
		if len(methods) == 0 {
			methods = []string{"keychain", "env", "prompt"}
		}
		out = append(out, TypeSpec{
			ID: id, Label: i.Label, Description: typeDescriptions[id],
			Fields: nonNilFields(i.Fields), Credentials: nonNilFields(i.Credentials),
			RequiresExtension: append([]string{}, i.Extensions...),
			Capabilities:      i.Capabilities, FingerprintFields: append([]string{}, i.FingerprintFields...),
			SignInMethods: methods, Presets: nonNilPresets(i.Presets),
			MetadataDriven: workspace.MetadataDrivenType(id),
		})
		if workspace.MetadataDrivenType(id) {
			out[len(out)-1].Warning = workspace.MetadataWarning
		}
	}
	return out
}

func nonNilPresets(p []connections.Preset) []connections.Preset {
	if p == nil {
		return []connections.Preset{}
	}
	return p
}

func nonNilFields(f []connections.Field) []connections.Field {
	if f == nil {
		return []connections.Field{}
	}
	return f
}

// ConnectionList is the rail's and the selector's data.
type ConnectionList struct {
	Connections []ConnectionInfo `json:"connections"`
	// RelaunchRequired says the workspace has to be reopened (MotherDuck
	// accepts one token per process, D10); Studio does it by closing and
	// reopening the workspace session.
	RelaunchRequired bool   `json:"relaunchRequired"`
	RelaunchMessage  string `json:"relaunchMessage,omitempty"`
}

// snapshot returns the open workspace, session and read-only flag without
// holding a.mu, so a slow connect or test never blocks open, close or settings.
func (a *App) snapshot() (workspace.Workspace, session.Session, bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return nil, nil, false, err
	}
	return a.ws, a.sess, a.readOnly, nil
}

// ListConnections lists every connection with its status, scope, shadowing,
// read-only flag and whether it is remote. It never dials.
func (a *App) ListConnections() (*ConnectionList, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	out := &ConnectionList{Connections: a.connectionInfosLocked()}
	out.RelaunchRequired, out.RelaunchMessage = a.sess.RelaunchRequired()
	return out, nil
}

// ConnectionRef names one connection.
type ConnectionRef struct {
	Name string `json:"name"`
	// Scope is shared | local | user; empty means the winning definition.
	Scope string `json:"scope"`
}

// ConnectionDetail is one connection as the editor shows it.
type ConnectionDetail struct {
	Name  string                  `json:"name"`
	Scope string                  `json:"scope"`
	Def   workspace.ConnectionDef `json:"def"`
	// Fingerprint is workspace.Fingerprint(Def): what a saved sign-in is bound
	// to. The editor compares it with ConnectionFingerprint(draft); the
	// comparison is Go's, so every field that counts is counted. A one-way hash
	// of non-secret values.
	Fingerprint string `json:"fingerprint"`
	// Shadows are the lower scopes that also define this name.
	Shadows []string `json:"shadows"`
	// ShadowedBy is set when this is not the winning definition: the scope that
	// overrides it on this computer.
	ShadowedBy string `json:"shadowedBy,omitempty"`
	// SignIn is this person's sign-in method; SignInHint the team file's
	// suggestion (it has no effect).
	SignIn     string                   `json:"signIn"`
	SignInHint string                   `json:"signInHint,omitempty"`
	Credential secrets.CredentialStatus `json:"credential"`
	Info       ConnectionInfo           `json:"info"`
}

func scopeStrings(s []workspace.Scope) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, string(x))
	}
	return out
}

// scopeChoice reads the scope the editor sent; the wording is Studio's.
const scopeChoiceMessage = "Choose where to save this connection: Team, This computer or All my workspaces."

func scopeChoice(s string) (workspace.Scope, error) {
	sc, ok := workspace.ParseScope(s)
	if !ok {
		return "", errors.New(scopeChoiceMessage)
	}
	return sc, nil
}

// GetConnection describes the winning definition of name.
func (a *App) GetConnection(name string) (*ConnectionDetail, error) {
	return a.GetConnectionInScope(ConnectionRef{Name: name})
}

// GetConnectionInScope describes name's definition in one scope (the shadowed
// Team copy behind "Look at the Team one"), or the winning one with no scope.
// Nothing in the result is a secret.
func (a *App) GetConnectionInScope(ref ConnectionRef) (*ConnectionDetail, error) {
	ws, _, _, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	d, err := definitionDetail(ws, ref)
	if err != nil {
		return nil, err
	}
	d.Info = a.infoFor(ref.Name)
	d.Credential = a.credentialStatus(ws, ref.Name, d.SignIn)
	return d, nil
}

// definitionDetail is the winning definition of ref.Name, or its copy in
// ref.Scope when that differs, with the fingerprint a saved sign-in is bound to.
func definitionDetail(ws workspace.Workspace, ref ConnectionRef) (*ConnectionDetail, error) {
	v, err := workspace.ViewDefinition(ws, ref.Name, ref.Scope)
	var noCopy *workspace.NoScopeCopyError
	var badScope *workspace.InvalidScopeError
	switch {
	case errors.As(err, &badScope):
		return nil, errors.New(scopeChoiceMessage)
	case errors.As(err, &noCopy):
		return nil, fmt.Errorf("There is no %s copy of %q.", scopeWord(noCopy.Scope), noCopy.Name)
	case err != nil:
		return nil, err
	}
	return &ConnectionDetail{
		Name: v.Name, Scope: string(v.Scope), Def: v.Def, Fingerprint: v.Fingerprint,
		Shadows: scopeStrings(v.Shadows), ShadowedBy: string(v.ShadowedBy),
		SignIn: v.SignIn, SignInHint: v.SignInHint,
	}, nil
}

// infoFor is name's live state as the rail shows it (the zero value when the
// workspace has no such connection).
func (a *App) infoFor(name string) ConnectionInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var info ConnectionInfo
	for _, ci := range a.connectionInfosLocked() {
		if ci.Name == name {
			info = ci
		}
	}
	return info
}

// credentialStatus is what is saved for name, never the secret; a lookup that
// fails reports the redacted reason under the sign-in method in effect.
func (a *App) credentialStatus(ws workspace.Workspace, name, signIn string) secrets.CredentialStatus {
	ctx, cancel := context.WithTimeout(a.ctxOrBackground(), keychainTimeout)
	defer cancel()
	st, err := ws.CredentialStatus(ctx, name)
	if err != nil {
		st = secrets.CredentialStatus{Method: signIn, Detail: a.redact(err.Error())}
	}
	return st
}

// ConnectionFingerprint is the fingerprint of a draft definition from the
// editor form, to compare with ConnectionDetail.Fingerprint: when they differ,
// Save re-binds the saved sign-in. It reads only the definition (never a
// credential) and returns only the hash.
func (a *App) ConnectionFingerprint(def workspace.ConnectionDef) string {
	return workspace.Fingerprint(def)
}

func scopeWord(s string) string {
	switch s {
	case "shared":
		return "Team"
	case "local":
		return "This computer"
	}
	return "All my workspaces"
}

// redact scrubs credential-shaped text and every secret value the app has seen.
func (a *App) redact(s string) string {
	return secrets.RedactPatterns(a.secrets.Redactor().Redact(s))
}

// SaveConnectionRequest saves a definition. SignIn is this person's sign-in
// method (keychain, prompt or env:PREFIX); empty keeps the current one. It never
// carries a credential: those go through SetConnectionCredentials.
type SaveConnectionRequest struct {
	Scope  string                  `json:"scope"`
	Def    workspace.ConnectionDef `json:"def"`
	IsNew  bool                    `json:"isNew"`
	SignIn string                  `json:"signIn"`
	// ConfirmRebind is the person's yes to using an environment sign-in for a
	// server other than the one it was chosen for. The UI sets it only after
	// showing the server-changed confirmation; without it the backend refuses to
	// re-bind, whatever a client sends.
	ConfirmRebind bool `json:"confirmRebind"`
}

// rebindPrefix marks the refusal the editor answers with its confirmation.
const rebindPrefix = "confirm-rebind: "

// setSignIn applies a sign-in method; a re-bind of an environment sign-in to a
// changed server needs confirm.
func (a *App) setSignIn(ws workspace.Workspace, name, method string, confirm bool) error {
	if confirm {
		return ws.RebindSignIn(name, method)
	}
	if err := ws.SetSignIn(name, method); err != nil {
		if errors.Is(err, workspace.ErrRebindNeedsConfirm) {
			return errors.New(rebindPrefix + err.Error())
		}
		return err
	}
	return nil
}

// SaveConnection adds or updates a definition in the chosen scope and applies it
// to the open session without dialling. The name is immutable after the first
// save: an update names an existing connection.
func (a *App) SaveConnection(req SaveConnectionRequest) (*ConnectionDetail, error) {
	ws, _, _, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	scope, err := scopeChoice(req.Scope)
	if err != nil {
		return nil, err
	}
	req.Def.Name = strings.TrimSpace(req.Def.Name)
	if req.IsNew {
		err = ws.AddConnection(scope, req.Def)
	} else {
		err = ws.UpdateConnection(scope, req.Def)
	}
	if err != nil {
		return nil, errors.New(a.redact(err.Error()))
	}
	if req.SignIn != "" {
		if err := a.setSignIn(ws, req.Def.Name, req.SignIn, req.ConfirmRebind); err != nil {
			a.syncConnections() // the definition was saved; the binding is unchanged and still guards it
			return nil, errors.New(a.redact(err.Error()))
		}
	}
	a.syncConnections()
	a.emitEvent("workspace:changed", map[string]any{"kind": "connections"})
	return a.GetConnection(req.Def.Name)
}

// MoveConnectionRequest moves a connection to another scope.
type MoveConnectionRequest struct {
	Name string `json:"name"`
	To   string `json:"to"`
}

// MoveConnection changes where a connection is saved. The saved sign-in follows
// it (it is bound to the workspace id, the name and the server fingerprint).
func (a *App) MoveConnection(req MoveConnectionRequest) (*ConnectionDetail, error) {
	ws, _, _, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	to, err := scopeChoice(req.To)
	if err != nil {
		return nil, err
	}
	cur, ok := ws.Connection(req.Name)
	if !ok || cur.Scope == "" {
		return nil, fmt.Errorf("There is no connection called %q.", req.Name)
	}
	from := cur.Scope
	ctx, cancel := context.WithTimeout(a.ctxOrBackground(), keychainTimeout)
	defer cancel()
	if err := ws.MoveConnection(ctx, req.Name, from, to); err != nil {
		return nil, errors.New(a.redact(err.Error()))
	}
	a.syncConnections()
	a.emitEvent("workspace:changed", map[string]any{"kind": "connections"})
	return a.GetConnection(req.Name)
}

// DeleteConnection removes a definition from one scope. Its saved sign-in goes
// with it when nothing else keeps using it. Your database isn't touched.
func (a *App) DeleteConnection(ref ConnectionRef) error {
	ws, _, _, err := a.snapshot()
	if err != nil {
		return err
	}
	scope, err := scopeChoice(ref.Scope)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctxOrBackground(), keychainTimeout)
	defer cancel()
	if err := ws.RemoveConnection(ctx, scope, ref.Name); err != nil {
		return errors.New(a.redact(err.Error()))
	}
	a.syncConnections()
	a.emitEvent("workspace:changed", map[string]any{"kind": "connections"})
	return nil
}
