package session

import (
	"errors"
	"regexp"
	"strings"
)

// denyURLRE finds the request that reached a deny secret's endpoint in a DuckDB
// error: DuckDB 1.5 says "Could not resolve hostname error for HTTP GET to
// 'https://<bucket>.deny.invalid/<key>'" (virtual-hosted style) or, with path
// style, "https://deny.invalid/<bucket>/<key>". It is anchored on the "HTTP
// <verb> to '" prefix so a bucket name in some other part of a message is not
// read, and only the bucket is captured. Bucket names allow the characters of
// S3, GCS (which also has "_") and R2 buckets.
var denyURLRE = regexp.MustCompile(`HTTP (?:GET|HEAD|POST|PUT|DELETE) to 'https?://(?:([a-z0-9][a-z0-9._-]*?)\.)?` +
	regexp.QuoteMeta(denyEndpoint) + `(?::\d+)?/?([^/'"\s?]*)`)

// storageSchemes are the URL schemes the deny secrets cover (s3 also stands for
// s3a and s3n), in the form the message names them.
var storageSchemes = []string{"s3", "s3a", "s3n", "gcs", "gs", "r2"}

// StorageDenyError says a read reached storage that no ready storage
// connection's scope covers. Its text is the plain-words rewrite; it names the
// bucket only, never DuckDB's message (which quotes the key and the
// statement). Unwrap returns the original for classification, not for display.
type StorageDenyError struct {
	// Scheme is the storage scheme the bucket is written with (s3, gcs, gs or
	// r2); "s3" when nothing says otherwise.
	Scheme string
	Bucket string
	// Cover names a connection whose scope covers the bucket but which is not
	// ready, and CoverWhy says why ("it needs credentials"): the fix is to
	// sign it in, not to add another connection.
	Cover, CoverWhy string
	err             error
}

func (e *StorageDenyError) Error() string {
	where := "data"
	if e.Bucket != "" {
		scheme := e.Scheme
		if scheme == "" {
			scheme = "s3"
		}
		where = "data at " + scheme + "://" + e.Bucket + "/"
	}
	if e.Cover != "" {
		return where + " is covered by the \"" + e.Cover + "\" connection, but " + e.CoverWhy + ": fix that connection first"
	}
	return where + " is outside every storage connection's scope: add one whose scope contains it"
}

func (e *StorageDenyError) Unwrap() error { return e.err }

// MapStorageDenyError rewrites a read that failed against a deny secret into a
// *StorageDenyError and returns any other error unchanged. It is pure.
func MapStorageDenyError(err error) error {
	if err == nil {
		return nil
	}
	m := denyURLRE.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	bucket := m[1]
	if bucket == "" {
		bucket = m[2] // path style: the first path segment
	}
	return &StorageDenyError{Scheme: "s3", Bucket: bucket, err: err}
}

// ExplainStorageDeny adds to a *StorageDenyError what the session knows: the
// scheme the statement wrote the bucket with (sqlText may be empty), and a
// storage connection whose scope covers the bucket but is not ready, so the
// message names it instead of saying "add one". Any other error is returned
// unchanged.
func ExplainStorageDeny(err error, sess Session, sqlText string) error {
	var de *StorageDenyError
	if !errors.As(err, &de) {
		return err
	}
	out := *de
	if s := schemeInText(sqlText, de.Bucket); s != "" {
		out.Scheme = s
	}
	if sess == nil || de.Bucket == "" {
		return &out
	}
	for _, info := range sess.ConnectionInfos() {
		if !info.Catalogless || info.Kind == StatusOK {
			continue
		}
		if scope, scheme := strings.ToLower(info.Scope), ""; covers(scope, de.Bucket, &scheme) {
			out.Cover, out.CoverWhy = info.Name, requirementPhrase(info.Kind)
			if sqlText == "" {
				out.Scheme = scheme
			}
			break
		}
	}
	return &out
}

// schemeInText finds "<scheme>://<bucket>/" (or the bare bucket at its end) in
// a statement and returns the scheme, with s3a and s3n folded into s3.
func schemeInText(text, bucket string) string {
	if text == "" || bucket == "" {
		return ""
	}
	low := strings.ToLower(text)
	for _, s := range storageSchemes {
		if strings.Contains(low, s+"://"+bucket+"/") || strings.HasSuffix(low, s+"://"+bucket) {
			return foldScheme(s)
		}
	}
	return ""
}

func foldScheme(s string) string {
	if s == "s3a" || s == "s3n" {
		return "s3"
	}
	return s
}

// covers reports whether a connection scope lies in the bucket (any scheme), and
// sets scheme to the one the scope is written with.
func covers(scope, bucket string, scheme *string) bool {
	for _, s := range storageSchemes {
		p := s + "://" + bucket
		if scope == p || strings.HasPrefix(scope, p+"/") {
			*scheme = foldScheme(s)
			return true
		}
	}
	return false
}
