// Package icebergtest is the integration tests' Iceberg lakehouse: a throwaway
// MinIO and the apache/iceberg-rest-fixture REST catalog wired together in
// containers, seeded with DuckDB's own iceberg extension, plus helpers that
// craft hostile tables (data files that point at local paths, URLs or other
// buckets), a bearer-token and vended-credential proxy in front of the catalog,
// and a request-logging file server for SSRF assertions. Its code is built only
// with the integration tag; nothing in a normal build imports it.
//
// Threat and refusal tests must read a column (max(id), sum(amount)), never
// count(*): DuckDB answers count(*) from the manifest's record counts without
// opening a data file, so it proves nothing about what a table's file paths
// can reach.
package icebergtest
