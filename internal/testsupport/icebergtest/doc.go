// Package icebergtest is the integration tests' Iceberg lakehouse: a throwaway
// MinIO and the apache/iceberg-rest-fixture REST catalog wired together in
// containers, seeded with DuckDB's own iceberg extension, plus helpers that
// craft hostile tables (data files that point at local paths, URLs or other
// buckets), a bearer-token and vended-credential proxy in front of the catalog,
// and a request-logging file server for SSRF assertions. Its code is built only
// with the integration tag; nothing in a normal build imports it.
package icebergtest
