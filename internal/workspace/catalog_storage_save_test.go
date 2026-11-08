package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Saving an s3_tables catalog is refused when its storage connection exists and is
// not AWS, in every scope; a storage connection that does not exist yet is not.
func TestSavingAnS3TablesCatalogRefusesANonAWSStorageConnection(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	require.NoError(t, ws.AddConnection(ScopeLocal, icebergS3Tables("early")), "its storage connection does not exist yet")
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "aws_lake", Type: "s3", Scope: "s3://lake/", Region: "us-east-1",
		Options: map[string]string{"endpoint": "127.0.0.1:9000", "url_style": "path"}}))
	for _, scope := range []Scope{ScopeLocal, ScopeUser, ScopeShared} {
		err := ws.AddConnection(scope, icebergS3Tables("late_"+string(scope)))
		require.Error(t, err, scope)
		assert.Contains(t, err.Error(), "S3 Tables signs with AWS: aws_lake has a custom endpoint", scope)
	}
	// An edit that points an existing catalog at it is refused too.
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "real_aws", Type: "s3", Scope: "s3://aws/", Region: "us-east-1"}))
	ok := icebergS3Tables("edited")
	ok.Options["storage"] = "real_aws"
	require.NoError(t, ws.AddConnection(ScopeLocal, ok))
	ok.Options["storage"] = "aws_lake"
	require.Error(t, ws.UpdateConnection(ScopeLocal, ok))
}
