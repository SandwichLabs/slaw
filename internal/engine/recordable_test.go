package engine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecordableStatement(t *testing.T) {
	for name, text := range map[string]string{
		"multi-line": "SELECT a,\n  b\nFROM t\nWHERE id = 1",
		"crlf":       "SELECT 1\r\nFROM t",
		"tab":        "SELECT\t1",
	} {
		assert.True(t, RecordableStatement(text), name)
	}
	for name, text := range map[string]string{
		"empty":      "",
		"oversize":   "SELECT '" + strings.Repeat("a", RecordableMax) + "'",
		"credential": "SELECT 1\nFROM t\nWHERE password = 'x'",
		"dsn line 3": "SELECT *\nFROM postgres_scan(\n'host=h password=hunter2', 'public', 't')",
		"bidi":       "SELECT 1\n-- ‮",
		"c1":         "SELECT '\u009b'",
		"escape":     "SELECT 1\n\x1b[2J",
		"lone cr":    "SELECT 1\rFROM t",
		"nul":        "SELECT 1\x00",
	} {
		assert.False(t, RecordableStatement(text), name)
	}
	// Recordable itself is unchanged: still one line.
	assert.False(t, Recordable("SELECT 1\nFROM t"))
	assert.False(t, Recordable("SELECT\t1"))
	assert.True(t, Recordable("SELECT 1"))
}

func TestRecordable_RefusesUnicodeSpaces(t *testing.T) {
	for _, sp := range []string{" ", " ", " ", " ", " ", " ", "　", " ", " ", " "} {
		text := "SELECT 1 WHERE password" + sp + "=" + sp + "'x'"
		assert.False(t, Recordable(text), "%q", sp)
		assert.False(t, RecordableStatement(text), "%q", sp)
		assert.False(t, RecordableStatement("SELECT"+sp+"1"), "%q", sp)
	}
	assert.True(t, RecordableStatement("SELECT 1\n\tFROM t"))
}

func TestRecordable(t *testing.T) {
	for _, ok := range []string{
		"SELECT 1",
		"SELECT token, password FROM users WHERE id = 5",
		"-- bearer of bad news SELECT 1",
		".mode csv",
	} {
		assert.True(t, Recordable(ok), ok)
	}
	for name, text := range map[string]string{
		"empty":      "",
		"newline":    "SELECT 1\nFROM t", // the console folds an entry to one line first
		"oversize":   "SELECT '" + strings.Repeat("a", RecordableMax) + "'",
		"escape":     "SELECT '\x1b[2J'",
		"c1 control": "SELECT '\u009b'",
		"bidi":       "SELECT '‮'",
		"password":   "SELECT 1 WHERE password = 'x'",
		"s3 key":     "FROM read_parquet('s3://b/k?s3_secret_access_key=AKIASECRET2')",
		"dsn":        "SELECT * FROM postgres_scan('host=h password=hunter2', 'public', 't')",
		"url userpw": "SELECT 'https://user:pw12345@host/x'",
		"motherduck": ".open md:?motherduck_token=SEKRETTOKEN1",
	} {
		assert.False(t, Recordable(text), name)
	}
}
