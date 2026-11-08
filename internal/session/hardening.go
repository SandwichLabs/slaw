package session

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	duckdb "github.com/duckdb/duckdb-go/v2"
)

// hardeningDSN returns the DSN query parameters for every option DuckDB
// accepts at open. Strings only; secret_directory is also set (and read back)
// in applyHardening, as it was ignored by the DSN on DuckDB 1.4.1. lock_configuration is
// deliberately NOT here: it must be the last thing set (it blocks every later
// SET), and a DSN-time lock would stop the SETs below.
func hardeningDSN(c *config) url.Values {
	q := url.Values{}
	q.Set("allow_persistent_secrets", "false")
	q.Set("autoinstall_known_extensions", "false")
	q.Set("autoload_known_extensions", "false")
	q.Set("allow_community_extensions", "false")
	if c.tempDir != "" {
		q.Set("temp_directory", c.tempDir)
	}
	if c.extensionDir != "" {
		q.Set("extension_directory", c.extensionDir)
	}
	return q
}

// OpenDB opens a DuckDB database (path "" = in-memory) with the open-time
// hardening applied through the connector DSN. The result is not yet locked:
// pass it to Wrap, which applies the SET-only options, locks the configuration
// and reads every option back.
func OpenDB(path string, opts ...Option) (*sql.DB, error) {
	c := newConfig(opts)
	applyDirDefaults(c)
	return openDB(path, c)
}

func openDB(path string, c *config) (*sql.DB, error) {
	for _, d := range []string{c.tempDir, c.extensionDir} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("failed to create DuckDB directory %q: %w", d, err)
		}
	}
	dsn := path + "?" + hardeningDSN(c).Encode()
	conn, err := duckdb.NewConnector(dsn, func(execer driver.ExecerContext) error {
		// A hub whose default catalog was moved (SetDefaultCatalog) starts
		// every new pooled connection in that catalog.
		if p := c.initCatalog.Load(); p != nil {
			if _, err := execer.ExecContext(context.Background(), "USE "+QuoteIdent(*p), nil); err != nil {
				return fmt.Errorf("failed to enter default catalog: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open hardened DuckDB: %w", err)
	}
	return sql.OpenDB(conn), nil
}

type hardeningCheck struct {
	option string
	want   string
}

// expectedSettings lists every option that must read back after Wrap.
func expectedSettings(c *config) []hardeningCheck {
	checks := []hardeningCheck{
		{"allow_persistent_secrets", "false"},
		{"autoinstall_known_extensions", "false"},
		{"autoload_known_extensions", "false"},
		{"allow_community_extensions", "false"},
		{"lock_configuration", "true"},
		// DuckDB 1.5 reads these back as empty lists; a non-empty value would mean
		// something widened or narrowed what a session may touch or change.
		{"allowed_configs", allowedConfigs(c)},
		{"extension_directories", "[]"},
		{"allowed_directories", "[]"},
		{"allowed_paths", "[]"},
	}
	if c.tempDir != "" {
		checks = append(checks, hardeningCheck{"temp_directory", c.tempDir})
	}
	if c.extensionDir != "" {
		checks = append(checks, hardeningCheck{"extension_directory", c.extensionDir})
	}
	if c.secretDir != "" {
		checks = append(checks, hardeningCheck{"secret_directory", c.secretDir})
	}
	if !c.lazyStorage {
		// httpfs is loaded, so its limits exist and read back (a lazy session
		// reads them back where httpfs loads, see setHTTPLimits).
		l := c.httpLimits()
		checks = append(checks,
			hardeningCheck{httpTimeoutSetting, strconv.Itoa(l.timeout)},
			hardeningCheck{httpRetriesSetting, strconv.Itoa(l.retries)})
	}
	return checks
}

// allowedConfigs is what allowed_configs must read back: nothing, except the one
// storage setting when httpfs was not loaded at open (see storage_deny.go).
func allowedConfigs(c *config) string {
	if c.lazyStorage {
		return "[" + strings.Join(lazyAllowedConfigs(), ", ") + "]"
	}
	return "[]"
}

// applyHardening runs the SET-only options on one connection, locks the
// configuration and reads every option back, failing closed when any did not
// take (spike finding J).
func applyHardening(ctx context.Context, conn *sql.Conn, c *config) error {
	if c.secretDir != "" {
		if err := os.MkdirAll(c.secretDir, 0o700); err != nil {
			return fmt.Errorf("failed to create secret directory: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "SET secret_directory = "+QuoteLiteral(c.secretDir)); err != nil {
			return fmt.Errorf("hardening: failed to set secret_directory: %w", err)
		}
	}
	if err := prepareMotherDuck(ctx, conn, c); err != nil {
		return err
	}
	if err := prepareStorage(ctx, conn, c); err != nil {
		return err
	}
	if !c.skipLock {
		if _, err := conn.ExecContext(ctx, "SET lock_configuration = true"); err != nil {
			return fmt.Errorf("hardening: failed to lock configuration: %w", err)
		}
	}
	return verifyHardening(ctx, conn, c)
}

func verifyHardening(ctx context.Context, conn *sql.Conn, c *config) error {
	var bad []string
	for _, chk := range expectedSettings(c) {
		if chk.option == "lock_configuration" && c.skipLock {
			continue
		}
		var got sql.NullString
		err := conn.QueryRowContext(ctx,
			"SELECT CAST(current_setting("+QuoteLiteral(chk.option)+") AS VARCHAR)").Scan(&got)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s (unreadable: %v)", chk.option, err))
			continue
		}
		if !settingMatches(chk.option, chk.want, got.String) {
			bad = append(bad, fmt.Sprintf("%s=%q (want %q)", chk.option, got.String, chk.want))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("DuckDB hardening did not take, refusing to open: %s", strings.Join(bad, "; "))
	}
	return nil
}

func settingMatches(option, want, got string) bool {
	switch option {
	case "temp_directory", "extension_directory", "secret_directory":
		return filepath.Clean(got) == filepath.Clean(want)
	case "allowed_configs":
		// DuckDB keeps the list as a set: the order it reads back is not ours.
		return sameSet(got, want)
	}
	return strings.EqualFold(got, want)
}

// isNoTransaction reports the error DuckDB gives ROLLBACK when no transaction
// is open: a transaction-class error from the driver, not a message to match.
// Any transaction-class error is treated as "nothing to roll back" because the
// caller only wants the connection clean, and a ROLLBACK that fails for a
// transaction reason leaves no transaction either way.
func isNoTransaction(err error) bool {
	var de *duckdb.Error
	return errors.As(err, &de) && de.Type == duckdb.ErrorTypeTransaction
}

// sameSet compares two "[a, b]" lists ignoring order and case.
func sameSet(a, b string) bool {
	norm := func(s string) []string {
		s = strings.Trim(strings.TrimSpace(s), "[]")
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
				out = append(out, p)
			}
		}
		sort.Strings(out)
		return out
	}
	return slices.Equal(norm(a), norm(b))
}
