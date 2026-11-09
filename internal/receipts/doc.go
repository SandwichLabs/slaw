// Package receipts is the record of a run (E1, docs/plans/run-receipts.md): a
// small JSON document saying that a question was asked and what it touched,
// never the answer. It holds no row value: the result is only fingerprinted, by
// a keyed HMAC over the columns and the rows as shown.
//
// A receipt is machine-local state under .slaw-local/receipts/, one file per
// receipt, pruned by a per-person Policy. It never travels in a bundle or the
// workspace's tracked files, and nothing in it is a credential: text that
// engine.Recordable refuses is replaced by its hash by the caller, and every
// field a caller cannot be trusted with (a connection's status, a source's
// ref) is an enum or a validated relative id.
//
// The package is a leaf: it imports only the standard library, jsonenc,
// filestore and filelock (TestImportsStayLeaf). Collecting the facts, and
// deciding what Recordable allows, is the callers' job.
package receipts
