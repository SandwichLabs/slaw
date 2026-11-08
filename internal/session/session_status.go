package session

// Connection status, detach, default-catalog switching and the introspection
// helpers the hub shares with the connect path.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

func (h *hub) SetDefaultCatalog(ctx context.Context, name string) error {
	if name == h.defCat() {
		return nil
	}
	if !h.ownsDB {
		return fmt.Errorf("the default catalog of a wrapped session cannot be changed")
	}
	e, eAtt := h.entryOf(name)
	if e == nil || !eAtt {
		return fmt.Errorf("connection %q is not attached", name)
	}
	n := name
	h.cfg.initCatalog.Store(&n)
	// Recycle idle pooled connections so every one re-runs the USE above.
	h.db.SetMaxIdleConns(0)
	h.db.SetMaxIdleConns(2)

	conn, err := h.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	var cat, schema string
	if err := conn.QueryRowContext(ctx, "SELECT current_database(), current_schema()").Scan(&cat, &schema); err != nil {
		return fmt.Errorf("failed to read default catalog: %w", err)
	}
	if cat != name {
		return fmt.Errorf("failed to switch the default catalog to %q", name)
	}
	h.mu.Lock()
	h.defMu.Lock()
	if old := h.conns[h.defaultCatalog]; old != nil {
		old.status.Default = false
	}
	e.status.Default = true
	h.defaultCatalog = name
	h.defSchemaName = schema
	h.defMu.Unlock()
	h.mu.Unlock()
	h.schemaMu.Lock()
	h.defaultSchema = map[string]string{name: schema}
	h.schemaMu.Unlock()
	h.InvalidateSchema("")
	return nil
}

func (h *hub) setStatus(name string, kind StatusKind, msg string, pid int) {
	h.setStatusFull(name, kind, msg, pid, statusExtras{})
}

// setStatusCause is setStatus for a failure whose cause is known: a changed
// server is recorded as typed data, so no caller parses it back out of msg.
func (h *hub) setStatusCause(name string, kind StatusKind, msg string, cause error) {
	var fp *secrets.ErrFingerprintChanged
	if errors.As(cause, &fp) && fp.Old != "" && fp.New != "" {
		h.setStatusFull(name, kind, msg, 0, statusExtras{serverChanged: &ServerChange{Old: fp.Old, New: fp.New}})
		return
	}
	var x statusExtras
	var me *missingExtensionError
	if errors.As(cause, &me) {
		x.extension = me.name
	}
	var nc *secrets.ErrNeedsCredentials
	if errors.As(cause, &nc) && nc.SignIn != nil {
		hint := *nc.SignIn
		x.signIn = &hint
	}
	h.setStatusFull(name, kind, msg, 0, x)
}

// statusExtras are the typed, non-secret details a status may carry beside its
// kind and message.
type statusExtras struct {
	serverChanged *ServerChange
	extension     string
	signIn        *secrets.SignInHint
}

// setStatusFull replaces the whole status of a connection in one update.
func (h *hub) setStatusFull(name string, kind StatusKind, msg string, pid int, x statusExtras) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.conns[name]; e != nil {
		e.status.Kind, e.status.Message, e.status.LockedByPID, e.status.ServerChanged, e.status.Extension = kind, msg, pid, x.serverChanged, x.extension
		e.status.SignIn = x.signIn
		// Its own status: whatever it waited on before no longer decides it.
		e.status.BlockedBy = ""
	}
}

func (h *hub) Detach(ctx context.Context, name string) error {
	if name == h.defCat() {
		return fmt.Errorf("cannot detach the default catalog %q", name)
	}
	if e, _ := h.entryOf(name); e == nil {
		return fmt.Errorf("unknown connection %q", name)
	}
	// Everything below, including taking the entry out of the map, happens under
	// adminMu: a dependent connecting meanwhile must see either an attached
	// connection with its secret, or no connection at all, never an entry that
	// still reads attached after its secret is gone.
	h.adminMu.Lock()
	defer h.adminMu.Unlock()
	e, eAtt := h.entryOf(name) // read again: it may have changed while waiting
	if e == nil {
		return fmt.Errorf("unknown connection %q", name)
	}
	if eAtt {
		conn, err := h.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("failed to get admin connection: %w", err)
		}
		if !h.catalogless(e) {
			_, err = conn.ExecContext(ctx, "DETACH "+QuoteIdent(name))
		}
		if err == nil {
			// A dependent that cannot be detached keeps the secret in place: it
			// must never be left attached with none.
			err = h.detachDependents(ctx, conn, name)
		}
		if err == nil {
			h.dropSecretsOn(ctx, conn, e.secrets)
		}
		_ = conn.Close()
		if err != nil {
			return fmt.Errorf("failed to detach %q: %s", name, h.redact(err.Error()))
		}
	}
	h.removeStage(name)
	h.mu.Lock()
	delete(h.conns, name)
	h.mu.Unlock()
	h.InvalidateSchema("")
	return nil
}

func (h *hub) Status(name string) (Status, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	e, ok := h.conns[name]
	if !ok {
		return Status{}, false
	}
	return e.status, true
}

func (h *hub) Connections() []Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Status, 0, len(h.conns))
	for _, e := range h.conns {
		out = append(out, e.status)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ConnectionInfos implements Session.
func (h *hub) ConnectionInfos() []ConnectionInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]ConnectionInfo, 0, len(h.conns))
	for _, e := range h.conns {
		info := ConnectionInfo{
			Name: e.spec.Name, Type: e.spec.Type, Kind: e.status.Kind,
			ReadOnly: e.spec.ReadOnly, Default: e.status.Default, SignIn: e.status.SignIn,
		}
		if t, err := h.types.Get(e.spec.Type); err == nil {
			info.Remote = t.Remote()
			if c, ok := t.(CataloglessType); ok && c.Catalogless() {
				info.Catalogless = true
				if ss, ok := t.(StorageScopeType); ok && ss.StorageScope() {
					info.Scope = e.spec.Params["scope"]
				}
			}
		} else {
			// An unknown type is never "local".
			info.Remote = true
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (h *hub) catalogless(e *entry) bool {
	t, err := h.types.Get(e.spec.Type)
	if err != nil {
		return false
	}
	c, ok := t.(CataloglessType)
	return ok && c.Catalogless()
}

func (h *hub) isRemote(catalog string) bool {
	e, _ := h.entryOf(catalog)
	if e == nil {
		return false
	}
	t, err := h.types.Get(e.spec.Type)
	return err == nil && t.Remote()
}

// isDDL reports whether the statement can change a catalog's shape.
func isDDL(q string) bool {
	q = strings.TrimSpace(q)
	for strings.HasPrefix(q, "--") {
		i := strings.IndexByte(q, '\n')
		if i < 0 {
			return false
		}
		q = strings.TrimSpace(q[i+1:])
	}
	fields := strings.FieldsFunc(q, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '(' || r == ';' })
	if len(fields) == 0 {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE", "DROP", "ALTER", "ATTACH", "DETACH", "COMMENT", "IMPORT", "TRUNCATE", "CALL":
		return true
	}
	return false
}

// entryOf returns the registered entry and whether it is attached, read under
// the lock (attach is written by Connect on other goroutines).
func (h *hub) entryOf(name string) (*entry, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	e := h.conns[name]
	if e == nil {
		return nil, false
	}
	return e, e.attach
}

// StatusSnapshot records every connection's state, to tell after a run which
// ones a statement dialled (lazy attach) or changed. The Studio query editor
// and the console both take it before a run.
func StatusSnapshot(s Session) map[string]StatusKind {
	out := map[string]StatusKind{}
	for _, st := range s.Connections() {
		out[st.Name] = st.Kind
	}
	return out
}
