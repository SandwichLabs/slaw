// Words for a receipt, shared by the Evidence drawer's sections (the CLI's
// `slaw receipts show` says the same things, cmd/receipts_show.go). Pure
// functions over the receipt record: nothing here reads a result value, because
// a receipt holds none.

export type ReplayState = 'replayable' | 'live_only' | 'not_recorded'

/**
 * What can be said about reading the same data again: replayable only when every
 * connection has a snapshot that can be read at; live only when any snapshot is
 * a live fact (where the source was when it answered); otherwise nothing was
 * recorded (a table of an Iceberg catalog, a timed-out fact, a pinned version).
 */
export function replayState(r: ReceiptRecord): ReplayState {
  const conns = r.connections ?? []
  if (conns.length === 0) return 'not_recorded'
  const labels = conns.map((c) => c.snapshot?.label)
  if (labels.some((l) => l === 'live_only')) return 'live_only'
  if (labels.every((l) => l === 'replayable')) return 'replayable'
  return 'not_recorded'
}

/** The connection names, and the snapshot value when one connection was read at a replayable snapshot. */
export function summaryParts(r: ReceiptRecord): { names: string[]; snapshot: string } {
  const conns = r.connections ?? []
  const one = conns.length === 1 ? conns[0].snapshot : undefined
  return { names: conns.map((c) => c.name), snapshot: one && one.label === 'replayable' ? one.value : '' }
}

/** One word for the policy's verdict. */
export function verdictText(r: ReceiptRecord): string {
  return r.verdict === 'read' ? 'Read-only check passed' : 'Your own query'
}

const PERSON_BY: Record<string, string> = {
  query_editor: 'You, in the Query Explorer',
  console: 'You, in the console',
  cli_query: 'You, with slaw query',
  render: 'You, opening a page',
  notebook_cell: 'You, opening the notebook',
  a2ui: 'You, opening the page',
  artifact: 'You, opening the artifact',
  metric: 'You, trying a metric',
  assertion: 'You, checking a definition',
  watch: 'You, checking a watch',
}

/** Who ran it: "You, opening the notebook" or "The agent (run_query)". */
export function byText(r: ReceiptRecord): string {
  if (r.actor === 'agent') return `The agent (${r.surface.replace(/^agent_/, '')})`
  return PERSON_BY[r.surface] ?? `You, from ${r.surface.replace(/_/g, ' ')}`
}

/** Where the statement came from: its file-relative ref, or a plain description. */
export function fromText(r: ReceiptRecord): string {
  if (r.source.ref) return r.source.ref
  if (r.source.kind === 'sql') return 'SQL typed in place'
  return 'No file'
}

/** A hash as its first six and last four digits: 9c1e04…b7a4. */
export function shortHash(h: string): string {
  return h.length <= 12 ? h : `${h.slice(0, 6)}…${h.slice(-4)}`
}

export function durationText(ms: number): string {
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`
}

export function whenText(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

/** The status of a connection when the statement ran, in words. */
export function statusText(s: ReceiptConnection['status']): string {
  return s === 'not_connected' ? 'not connected' : s
}

/** The reason class of a missing receipt, in a sentence (never an error message). */
export function noteText(note: string | undefined): string {
  if (note === 'unavailable') return "This workspace keeps no receipts: its receipts folder can't be used."
  return "The receipt couldn't be written on this computer."
}
