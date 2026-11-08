// Package session is the DuckDB "hub" for a workspace (plan §4.4): one DuckDB
// instance whose attached catalogs are the user's connections, with
// catalog-aware introspection, cancellable statements, a schema cache and
// open-time hardening that is read back and fails closed.
//
// # Transition choice (#108): wrap, don't move
//
// The legacy .vibe container materialises its DuckDB blob into a temp file and
// opens it with its own *sql.DB, and a second DuckDB instance in the same
// process cannot ATTACH a file the first instance holds. Rather than reroute
// Save / Dirty / Revert (#81, #98) through the session now, the container
// opens its file with OpenDB (hardened DSN) and hands that instance to Wrap:
//
//   - the container's default catalog IS the file. Its name is the file stem
//     of the temp file ("analytics"), stable for every legacy container, and
//     every caller asks Session.DefaultCatalog() instead of assuming "main"
//     or "memory";
//   - the container keeps ownership of the *sql.DB, checkpoint, blob
//     round-trip and temp cleanup; Session.Close never closes it;
//   - Revert re-materialises the file, so the container builds a new Session
//     over the new instance (the old one is closed first).
//
// New() builds the other shape: an in-memory hub that owns its instance, which
// #109's workspace uses as the default. Both shapes share the same hardening,
// introspection, attach and cache code; only ownership differs.
//
// # Hardening
//
// OpenDB passes allow_persistent_secrets=false, autoinstall/autoload off,
// allow_community_extensions=false and app-private extension_directory /
// temp_directory in the connector DSN. Wrap SETs secret_directory (the DSN
// silently ignores it, spike finding J), then lock_configuration=true last,
// then reads every option back and refuses to open if any did not take.
// lock_configuration does not block ATTACH/DETACH/secret DDL/INSTALL/LOAD/USE;
// the agent SQL policy owns those (§4.5). httpfs's http_timeout (30 s) and
// http_retries (1) are set where httpfs loads, with the deny secrets (a context
// cancel does not interrupt a hung catalog request), and read back too.
//
// Findings: docs/plans/spike-duckdb-hub-findings.md.
package session
