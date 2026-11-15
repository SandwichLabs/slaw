package storageglob

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExpandFromNameIsAFileOnlyWhenWrittenAsAPath(t *testing.T) {
	for _, in := range []string{`SELECT * FROM "q.csv"`, `SELECT * FROM 'q.csv'`, `SELECT * FROM "my.parquet"`} {
		out, rw := expandLocal(t, in, false)
		assert.Equal(t, in, out)
		assert.False(t, rw.Changed(), in)
	}
	out, _ := expandLocal(t, `SELECT * FROM './q.csv'`, false)
	assert.Equal(t, `SELECT * FROM '/ws/acme/q.csv'`, out)
	// A reader's argument needs no slash.
	out, _ = expandLocal(t, `SELECT * FROM read_csv('q.csv')`, false)
	assert.Equal(t, `SELECT * FROM read_csv('/ws/acme/q.csv')`, out)
}

func TestExpandSkipsARelativePathItCannotLocate(t *testing.T) {
	for _, in := range []string{`SELECT * FROM read_csv(E'data/x.csv')`, `SELECT * FROM read_csv($$data/x.csv$$)`} {
		out, rw := expandLocal(t, in, true)
		assert.Equal(t, in, out)
		assert.False(t, rw.Changed(), in)
	}
}

func TestExpandResolvesMoreLocalReadersAndCopyTargets(t *testing.T) {
	cases := []struct{ in, want string }{
		{`SELECT * FROM read_xlsx('data/x.xlsx')`, `SELECT * FROM read_xlsx('/ws/acme/data/x.xlsx')`},
		{`SELECT * FROM st_read('data/x.gpkg')`, `SELECT * FROM st_read('/ws/acme/data/x.gpkg')`},
		{`SELECT * FROM sniff_csv('data/x.csv')`, `SELECT * FROM sniff_csv('/ws/acme/data/x.csv')`},
		{`COPY t TO 'data/out.csv'`, `COPY t TO '/ws/acme/data/out.csv'`},
		{`COPY (SELECT * FROM read_csv('data/x.csv')) TO 'data/out' (FORMAT parquet)`, `COPY (SELECT * FROM read_csv('/ws/acme/data/x.csv')) TO '/ws/acme/data/out' (FORMAT parquet)`},
	}
	for _, c := range cases {
		out, _ := expandLocal(t, c.in, true)
		assert.Equal(t, c.want, out)
	}
	for _, in := range []string{`COPY t TO '../out.csv'`, `COPY t TO '/abs/out.csv'`, `COPY t TO 's3://b/o.csv'`} {
		out, _ := expandLocal(t, in, true)
		assert.Equal(t, in, out)
	}
}

func TestRedactDropsTheStatementExcerptOfALocalRewrite(t *testing.T) {
	_, rw := expandLocal(t, `SELECT * FROM read_csv('data/x.csv')`, true)
	msg := rw.Redact("Binder Error: nope\nLINE 1: SELECT * FROM read_csv('/ws/acme/da")
	assert.Equal(t, "Binder Error: nope", msg)
}
