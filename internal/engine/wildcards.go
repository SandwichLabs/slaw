package engine

import (
	"context"
	"fmt"

	"github.com/SandwichLabs/slaw/internal/storageglob"
)

// expandWildcards rewrites the s3, gcs and r2 wildcard patterns in a bound
// statement into explicit lists of the objects they match (#138 follow-up; see
// internal/storageglob). The hub reads those URLs as literal keys, so this is
// the one place a person's SQL (the Studio editor, `slaw query`, the TUI, a
// notebook cell, an A2UI binding, MCP) gets its wildcards.
//
// SQL that already went through the agent policy arrives with its rewrite
// attached to ctx (storageglob.WithRewrite): it was expanded before it was
// vetted and runs as written. A relative file path (read_csv('data/x.csv')) is
// resolved against the workspace root here too (Options.Root), so the process
// working directory never decides which file runs. Text with nothing to
// rewrite comes back unchanged and costs a substring scan.
func (e *DuckDBEngine) expandWildcards(ctx context.Context, sqlText string) (string, *storageglob.Rewrite, error) {
	if rw, ok := storageglob.FromContext(ctx); ok {
		return sqlText, rw, nil
	}
	lister, _ := e.sess.(storageglob.Lister)
	out, rw, err := storageglob.Expand(ctx, sqlText, storageglob.Options{Lister: lister, Root: e.root})
	if err != nil {
		return "", nil, fmt.Errorf("failed to expand a wildcard: %w", err)
	}
	return out, rw, nil
}

// redactedError carries an error's text with the rewritten file list put back
// as the pattern the person wrote, and unwraps to the original.
type redactedError struct {
	msg string
	err error
}

func (r *redactedError) Error() string { return r.msg }
func (r *redactedError) Unwrap() error { return r.err }

// shorten keeps an error from quoting the rewritten statement.
func shorten(rw *storageglob.Rewrite, err error) error {
	if err == nil || !rw.Changed() {
		return err
	}
	return &redactedError{msg: rw.Redact(err.Error()), err: err}
}
