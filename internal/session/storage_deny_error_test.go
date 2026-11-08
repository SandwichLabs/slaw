package session

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func denyOf(t *testing.T, text string) *StorageDenyError {
	t.Helper()
	var de *StorageDenyError
	require.ErrorAs(t, MapStorageDenyError(errors.New(text)), &de, text)
	return de
}

func TestMapStorageDenyErrorFindsTheBucket(t *testing.T) {
	const want = "data at s3://other-bucket/ is outside every storage connection's scope: add one whose scope contains it"
	for name, in := range map[string]string{
		"virtual host": "IO Error: Could not resolve hostname error for HTTP GET to 'https://other-bucket.deny.invalid/data/00000-a.parquet' (HTTP 0)",
		"path style":   "IO Error: Could not resolve hostname error for HTTP GET to 'https://deny.invalid/other-bucket/data/00000-a.parquet'",
		"head, query":  "HTTP HEAD to 'https://other-bucket.deny.invalid/k?x=1'",
	} {
		assert.Equal(t, want, denyOf(t, in).Error(), name)
	}
	assert.Equal(t, "my.dotted.bucket", denyOf(t, "HTTP GET to 'https://my.dotted.bucket.deny.invalid/k'").Bucket)
	assert.Equal(t, "gcs_style_bucket", denyOf(t, "HTTP GET to 'https://gcs_style_bucket.deny.invalid/k'").Bucket, "GCS bucket names may carry an underscore")

	for _, text := range []string{
		"IO Error: Could not resolve hostname error for HTTP GET to 'https://s3.example.com/b/k'",
		"Catalog Error: Table with name x does not exist",
		"the file other-bucket.deny.invalid is mentioned but not requested", // not the anchored request shape
	} {
		raw := errors.New(text)
		assert.Same(t, raw, MapStorageDenyError(raw), text)
	}
	assert.NoError(t, MapStorageDenyError(nil))
}

func TestMapStorageDenyErrorNeverShowsTheRawError(t *testing.T) {
	raw := errors.New("failed: HTTP GET to 'https://b.deny.invalid/secret/key.parquet' SELECT max(secret_col)")
	err := MapStorageDenyError(raw)
	assert.NotContains(t, err.Error(), "secret")
	assert.NotContains(t, err.Error(), "key.parquet")
	assert.ErrorIs(t, err, raw)
}

// fakeStorageSess answers ConnectionInfos only.
type fakeStorageSess struct {
	Session
	infos []ConnectionInfo
}

func (f fakeStorageSess) ConnectionInfos() []ConnectionInfo { return f.infos }

func TestExplainStorageDenyNamesASignedOutCoveringConnection(t *testing.T) {
	raw := errors.New("HTTP GET to 'https://lake.deny.invalid/k'")
	mapped := MapStorageDenyError(raw)
	sess := fakeStorageSess{infos: []ConnectionInfo{
		{Name: "other", Catalogless: true, Scope: "s3://other/", Kind: StatusNeedsCredentials},
		{Name: "pg", Kind: StatusNeedsCredentials},
		{Name: "aws_lake", Catalogless: true, Scope: "s3://lake/raw/", Kind: StatusNeedsCredentials},
	}}
	err := ExplainStorageDeny(mapped, sess, "")
	assert.Equal(t, `data at s3://lake/ is covered by the "aws_lake" connection, but it needs credentials: fix that connection first`, err.Error())
	assert.ErrorIs(t, err, raw)

	// A ready connection is not the reason, and a bucket nobody covers says "add one".
	sess.infos[2].Kind = StatusOK
	assert.Contains(t, ExplainStorageDeny(mapped, sess, "").Error(), "outside every storage connection's scope")
	// Another error passes through, and so does a nil session.
	other := errors.New("boom")
	assert.Same(t, other, ExplainStorageDeny(other, sess, ""))
	assert.Contains(t, ExplainStorageDeny(mapped, nil, "").Error(), "s3://lake/")
}

// The scheme comes from the statement, or from the covering connection's scope.
func TestExplainStorageDenyUsesTheRightScheme(t *testing.T) {
	mapped := MapStorageDenyError(errors.New("HTTP GET to 'https://b.deny.invalid/k'"))
	assert.Contains(t, ExplainStorageDeny(mapped, nil, "SELECT * FROM read_parquet('gcs://b/x.parquet')").Error(), "gcs://b/")
	assert.Contains(t, ExplainStorageDeny(mapped, nil, "SELECT * FROM read_parquet('r2://b/x.parquet')").Error(), "r2://b/")
	assert.Contains(t, ExplainStorageDeny(mapped, nil, "SELECT * FROM read_parquet('s3a://b/x.parquet')").Error(), "s3://b/")
	sess := fakeStorageSess{infos: []ConnectionInfo{{Name: "g", Catalogless: true, Scope: "gs://b/", Kind: StatusNeedsCredentials}}}
	assert.Contains(t, ExplainStorageDeny(mapped, sess, "").Error(), "gs://b/")
	assert.Contains(t, ExplainStorageDeny(mapped, nil, "").Error(), "s3://b/", "with nothing to go on the bucket is an s3 one")
}
