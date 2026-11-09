//go:build !unix

package receipts

// oNoFollow is zero where the platform has no such flag (Windows is not a
// release target); the folder checks in Dir still apply.
const oNoFollow = 0
