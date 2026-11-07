package session

import (
	"errors"
	"regexp"
)

// denyURLRE finds the request that reached a deny secret's endpoint in a DuckDB
// error: DuckDB 1.5 says "Could not resolve hostname error for HTTP GET to
// 'https://<bucket>.deny.invalid/<key>'" (virtual-hosted style) or, with path
// style, "https://deny.invalid/<bucket>/<key>". Only the bucket is read from it.
var denyURLRE = regexp.MustCompile(`https?://(?:([a-z0-9][a-z0-9.-]*?)\.)?` + regexp.QuoteMeta(denyEndpoint) + `(?::\d+)?/?([^/'"\s?]*)`)

// StorageDenyError says a read reached storage that no storage connection's
// scope covers. Its text is the plain-words rewrite; it names the bucket only,
// never DuckDB's message (which quotes the key and the statement). Unwrap
// returns the original for classification, not for display.
type StorageDenyError struct {
	Bucket string
	err    error
}

func (e *StorageDenyError) Error() string {
	if e.Bucket == "" {
		return "table data is outside every storage connection's scope: add one whose scope contains it"
	}
	return "table data at s3://" + e.Bucket + "/ is outside every storage connection's scope: add one whose scope contains it"
}

func (e *StorageDenyError) Unwrap() error { return e.err }

// DenyErrorText is the rewrite of one error text: the plain-words message and
// true when text is a read refused by a deny secret (a request to
// deny.invalid), otherwise "" and false. It is pure.
func DenyErrorText(text string) (string, bool) {
	var de *StorageDenyError
	if err := MapStorageDenyError(errors.New(text)); errors.As(err, &de) {
		return de.Error(), true
	}
	return "", false
}

// MapStorageDenyError rewrites a read that failed against a deny secret into a
// *StorageDenyError and returns any other error unchanged.
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
		bucket = m[2]
	}
	return &StorageDenyError{Bucket: bucket, err: err}
}
