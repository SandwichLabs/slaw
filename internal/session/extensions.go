package session

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/SandwichLabs/slaw/internal/connname"
)

// Execer is the write surface shared by *sql.DB, *sql.Conn and *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// extensionSizeHint is shown to the user when a one-time download fails.
var extensionSizeHint = map[string]string{"excel": "about 2 MB"}

// EnsureExtension installs (if needed) and loads a core DuckDB extension. The
// hub runs with autoinstall/autoload off and an app-private extension
// directory, so a feature that needs an extension must ask for it explicitly;
// the first use downloads it once. A failure is returned with a message fit
// for the UI, never swallowed, so callers cannot silently degrade.
func EnsureExtension(ctx context.Context, db storageQuerier, name, feature string) error {
	return ensureExtension(ctx, db, name, feature, limitsFor(db))
}

// ensureExtension is EnsureExtension with the HTTP limits the session was configured with.
func ensureExtension(ctx context.Context, db storageQuerier, name, feature string, lim httpLimits) error {
	if !connname.Valid(name) {
		return fmt.Errorf("invalid extension name %q", name)
	}
	q := name
	if _, err := db.ExecContext(ctx, "INSTALL "+q); err != nil {
		size := ""
		if s, ok := extensionSizeHint[name]; ok {
			size = " (" + s + ")"
		}
		return fmt.Errorf("%s needs a one-time download of the DuckDB %q extension%s, and it could not be fetched. Check your network connection and try again: %w", feature, name, size, err)
	}
	if _, err := db.ExecContext(ctx, "LOAD "+q); err != nil {
		return fmt.Errorf("%s could not load the DuckDB %q extension: %w", feature, name, err)
	}
	return protectStorage(ctx, db, true, lim)
}
