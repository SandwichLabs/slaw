package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Environment variable suffixes read by an env:PREFIX sign-in, and the
// credential field each fills.
//
//	PREFIX_USER      user      (readable)
//	PREFIX_PASSWORD  password  (secret)
//	PREFIX_TOKEN     token     (secret)
//	PREFIX_KEY_ID    key_id    (readable)
//	PREFIX_SECRET    secret    (secret)
//
// MotherDuck uses env:MOTHERDUCK, which reads MOTHERDUCK_TOKEN (spike #107,
// finding H: the token is an explicit env source, never an ambient one).
var envSuffixes = []string{"USER", "PASSWORD", "TOKEN", "KEY_ID", "SECRET"}

// EnvVarNames lists the variables an env:PREFIX sign-in reads.
func EnvVarNames(prefix string) []string {
	out := make([]string, 0, len(envSuffixes))
	for _, s := range envSuffixes {
		out = append(out, prefix+"_"+s)
	}
	return out
}

// EnvVarNamesFor lists the variables an env:PREFIX sign-in reads for a
// definition that uses only some credential fields (an Iceberg REST catalog
// with a bearer token reads PREFIX_TOKEN and nothing else). fields are the
// credential field names ("token", "key_id", ...); none means all of them.
func EnvVarNamesFor(prefix string, fields []string) []string {
	if len(fields) == 0 {
		return EnvVarNames(prefix)
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, prefix+"_"+strings.ToUpper(f))
	}
	return out
}

// EnvStore reads a credential from environment variables. It is read-only
// and has no fingerprint: choosing env:PREFIX is the person's explicit say-so.
type EnvStore struct {
	prefix string
	getenv func(string) string
}

// NewEnvStore returns a store for env:PREFIX.
func NewEnvStore(prefix string) *EnvStore { return &EnvStore{prefix: prefix, getenv: os.Getenv} }

// WithGetenv replaces the environment lookup (tests).
func (e *EnvStore) WithGetenv(f func(string) string) *EnvStore { e.getenv = f; return e }

// Prefix reports the variable prefix.
func (e *EnvStore) Prefix() string { return e.prefix }

// Load builds the credential from the variables that are set.
func (e *EnvStore) Load() (Credential, bool) {
	c := Credential{
		User:     e.getenv(e.prefix + "_USER"),
		Password: e.getenv(e.prefix + "_PASSWORD"),
		Token:    e.getenv(e.prefix + "_TOKEN"),
		KeyID:    e.getenv(e.prefix + "_KEY_ID"),
		Secret:   e.getenv(e.prefix + "_SECRET"),
	}
	return c, c != (Credential{})
}

// SetVars reports which variables are set (never their values).
func (e *EnvStore) SetVars() map[string]bool {
	out := map[string]bool{}
	for _, n := range EnvVarNames(e.prefix) {
		out[n] = e.getenv(n) != ""
	}
	return out
}

// Get implements Store; the reference is ignored (the prefix chose the source).
func (e *EnvStore) Get(context.Context, Ref) ([]byte, error) {
	c, ok := e.Load()
	if !ok {
		return nil, ErrNotFound
	}
	return c.Marshal()
}

// Set implements Store: the environment is read, never written.
func (e *EnvStore) Set(context.Context, Ref, []byte) error {
	return fmt.Errorf("%w: set %s in the environment Slaw starts from", ErrReadOnly, strings.Join(EnvVarNames(e.prefix), ", "))
}

// Delete implements Store: nothing to delete.
func (e *EnvStore) Delete(context.Context, Ref) error { return nil }
