package connections

// Testing a lake or table that reads with a storage connection's secret: the
// storage connections are tried first, in the same throwaway session, and the
// definition is then tested for real against them.

import (
	"context"
	"errors"
	"fmt"

	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/storageurl"
)

// requiredStorage is a storage connection Test brings up before the definition.
type requiredStorage struct {
	def  Def
	cred secrets.Credential
}

// WithRequiredStorage makes Test attach the storage connection def, with cred,
// before the definition under test (repeatable). The definition is then read
// through the secret of def's own saved scope. A failure of def is reported as
// def's (TestResult.Requirement) and the definition is not attempted. The
// caller passes the credential the connection's own sign-in resolves to; a
// definition tested with no covering storage connection is never read at all.
func WithRequiredStorage(def Def, cred secrets.Credential) TestOption {
	return func(o *testOpts) { o.required = append(o.required, requiredStorage{def: def, cred: cred}) }
}

// addRedacted teaches the result and the caller's redactor about cred.
func (o testOpts) addRedacted(red *secrets.Redactor, cred secrets.Credential) {
	red.AddCredential(cred)
	if o.redactor != nil {
		o.redactor.AddCredential(cred)
	}
}

// attachRequired attaches each required storage connection in sess and proves
// it with one listing of its scope. It returns the names to put in the
// definition's Spec.Requires, or (failed) the storage connection's result.
func attachRequired(ctx context.Context, sess session.Session, def Def, o testOpts, red *secrets.Redactor) (names []string, res TestResult, failed bool) {
	for _, r := range o.required {
		res := testRequirement(ctx, sess, def, r, o, red)
		if !res.OK {
			res.Requirement = r.def.Name
			if res.Code == "" {
				res = res.classified(r.def)
			}
			return nil, res, true
		}
		names = append(names, r.def.Name)
	}
	return names, TestResult{}, false
}

// testRequirement attaches one storage connection under its own time limit.
func testRequirement(ctx context.Context, sess session.Session, def Def, r requiredStorage, o testOpts, red *secrets.Redactor) TestResult {
	fail := func(kind session.StatusKind, err error) TestResult {
		return TestResult{Status: kind, Error: secrets.RedactPatterns(red.Redact(err.Error()))}
	}
	if r.def.Name == def.Name {
		return fail(session.StatusError, errors.New("a storage connection cannot be the connection under test"))
	}
	if err := Validate(r.def); err != nil {
		return fail(session.StatusError, err)
	}
	if !isStorageDef(r.def) {
		return fail(session.StatusError, fmt.Errorf("%s is not a storage connection", r.def.Type))
	}
	if !r.cred.HasSecret() {
		// Never attach without a secret: DuckDB would read the URL anonymously.
		res := fail(session.StatusNeedsCredentials, errors.New("no credentials are available for this connection"))
		res.Code = CodeNeedsCredentials
		res.Message = fmt.Sprintf("No credentials are saved for %s on this computer. Open it to sign in.", r.def.Name)
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	if o.install {
		if err := installBeforeOpen(ctx, r.def, o); err != nil {
			return fail(session.StatusError, err)
		}
	}
	spec := r.def.Spec()
	spec.ReadOnly = true
	c := r.cred
	spec.Credential = func(context.Context) (secrets.Credential, error) { return c, nil }
	return attachAndProbe(ctx, sess, r.def, spec, o, red)
}

// isStorageDef reports whether def is a storage connection (s3, gcs, r2, azure).
func isStorageDef(def Def) bool {
	switch def.Type {
	case "s3", "gcs", "r2", "azure":
		return true
	}
	return false
}

// checkCovered refuses, before anything is attached or read, a definition that
// reads a storage URL (catalog, data path, table location) that no required
// storage connection's own scope contains: DuckDB would read it with no secret
// (anonymously, or as the host's ambient identity).
func checkCovered(def Def, required []requiredStorage) error {
	if signsWithStorage(def) {
		return checkSignedBy(def, required)
	}
	urls := map[string]string{"location": def.Path}
	if def.Type == "ducklake" {
		urls = map[string]string{"catalog": def.Path, "data path": def.Options["data_path"]}
	} else if def.Type != "iceberg" && def.Type != "delta" {
		return nil
	}
	for _, what := range []string{"catalog", "data path", "location"} {
		u := urls[what]
		if u == "" || !IsDataURL(u) {
			continue
		}
		covered := false
		for _, r := range required {
			if !isStorageDef(r.def) || storageurl.Within(u, r.def.Scope) {
				covered = true
			}
		}
		if !covered {
			return fmt.Errorf("the %s %s is outside the scope of the storage connection(s) it would be read with; nothing was read", what, u)
		}
	}
	return nil
}

// checkSignedBy refuses an S3 Tables catalog tested with storage connections
// among which is not the one its option storage names: the catalog would be
// signed with a different identity from the one it is saved with.
func checkSignedBy(def Def, required []requiredStorage) error {
	for _, r := range required {
		if r.def.Name == def.Options["storage"] {
			return nil
		}
	}
	return fmt.Errorf("the catalog signs with the connection %q, which was not part of this test; nothing was read", def.Options["storage"])
}
