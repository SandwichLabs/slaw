// A2UI live-query helpers — ported from schoolwork-workspace's
// src/a2ui/renderer.js (runLiveQuery/toPoints/formatCell — the latter now
// lives in lib/formatCell.ts), adapted to the
// column-array + row-array shape RunA2UIQuery returns (matching every other
// tabular Wails binding in this app — RunQuery, GetTableData, ...) rather
// than the row-object shape the browser-local DuckDB binding used there.
//
// "Live" is the point: a data-bound component's "sql" re-runs every time it
// mounts, so the same chart/table/stat re-reflects whatever data is in the
// container now, rather than freezing the values the agent saw when it wrote
// the component.
import { errorText } from '../lib/errors'
import { App } from '../lib/bindings'
import { runPooled, _resetQueryPoolForTests, type QueryPriority } from './pool'

export interface LiveQueryResult {
  columns: string[]
  types: string[]
  rows: any[][]
  error?: string
  // the artifact query hit Go's 500-row cap
  truncated?: boolean
  // the run's receipt (docs/plans/run-receipts.md); absent when none was kept
  receipt_id?: string
}

// runLiveQuery executes sql through RunA2UIQuery (RunA2UIQueryIn when a
// connection is given) — the sandbox-gated Wails
// binding that re-enters the exact same per-statement SQL classifier
// run_query uses (internal/agents' checkSQLClass), never a bypass. A denied
// or failed query resolves with `error` set rather than throwing, so callers
// render an inline error state instead of an unhandled rejection.
//
// Queries queue behind the shared pool (pool.ts); `priority` ('low' by default —
// pass 'high' for a directly user-initiated run) decides which lane. An
// optional AbortSignal lets a caller that is about to unmount or reload
// (NotebookView switching notebooks, a component leaving the DOM) skip a
// query still waiting for a slot, and mark its result `aborted: true` if it
// resolves after the caller stopped caring — callers check this instead of
// applying a stale result to state that has since moved on to different
// content.
export const EMPTY_QUERY_MESSAGE = 'Write a query first.'

export async function runLiveQuery(sql: string, signal?: AbortSignal, priority: QueryPriority = 'low', connection?: string): Promise<LiveQueryResult & { aborted?: boolean }> {
  // A blank statement is not sent: the read-only gate would refuse it with a
  // message about live bindings, not about the missing query (#3).
  if (!sql.trim()) return { columns: [], types: [], rows: [], error: EMPTY_QUERY_MESSAGE }
  const aborted = { columns: [], types: [], rows: [], aborted: true }
  try {
    // Notebook format v2: an optional `connection` makes that connection the
    // default catalog for the statement's unqualified names. Unset keeps the
    // original binding (the session default).
    const out = await runPooled(
      () => (connection ? App.RunA2UIQueryIn(sql, connection) : App.RunA2UIQuery(sql)),
      signal,
      priority,
    )
    if (out.aborted) return aborted
    return { columns: out.value?.columns ?? [], types: out.value?.types ?? [], rows: out.value?.rows ?? [], receipt_id: out.value?.receipt_id }
  } catch (e: any) {
    if (signal?.aborted) return aborted
    return { columns: [], types: [], rows: [], error: errorText(e) }
  }
}

export { _resetQueryPoolForTests }

// formatNumber (chart values/axis labels) and formatCell (table cells) moved
// to lib/formatCell.ts — the one shared cell formatter used by every
// table/grid in the app, not just A2UI's. Import them from there directly.

export interface Point {
  label: string
  value: number
}

// toPoints coerces query rows (column-array + row-array) into {label, value}
// points. Prefers explicit "label"/"value" (or "name"/"total") columns, then
// falls back to the first/second column — the convention a model writing
// `SELECT school, AVG(score) ...` produces without being told about it.
export function toPoints(columns: string[], rows: any[][]): Point[] {
  const keys = columns.length ? columns : (rows[0] ? rows[0].map((_, i) => String(i)) : [])
  const labelIdx = firstIndexOf(keys, ['label', 'name'])
  const valueIdx = firstIndexOf(keys, ['value', 'total'])
  const li = labelIdx >= 0 ? labelIdx : 0
  const vi = valueIdx >= 0 ? valueIdx : firstValueColumn(rows, li, keys.length)
  return rows.map((r) => ({
    label: String(r[li] ?? ''),
    value: Number(r[vi] ?? 0) || 0,
  }))
}

// firstValueColumn: the second column, unless it holds text (an attribute of
// the label, as in `SELECT day, top_product, sales`) and a later column holds
// numbers; then that one.
function firstValueColumn(rows: any[][], label: number, n: number): number {
  const fallback = n > 1 ? 1 : 0
  const sample = (i: number) => rows.find((r) => r[i] !== null && r[i] !== undefined)?.[i]
  const numeric = (i: number) => {
    const v = sample(i)
    return typeof v === 'number' || (typeof v === 'string' && v.trim() !== '' && Number.isFinite(Number(v)))
  }
  if (numeric(fallback)) return fallback
  for (let i = 0; i < n; i++) if (i !== label && numeric(i)) return i
  return fallback
}

function firstIndexOf(keys: string[], names: string[]): number {
  for (const n of names) {
    const i = keys.indexOf(n)
    if (i >= 0) return i
  }
  return -1
}

// colorClass maps a validated A2UI color token to its CSS class. The token
// itself was already checked against the closed enum server side
// (internal/a2ui.Validate); this only ever emits `a2ui--<token>` — never the
// token interpolated into a style attribute, per CLAUDE.md's "model output
// never reaches a style attribute".
export function colorClass(token?: string): string {
  return token ? `a2ui--${token}` : ''
}

// pickLabelIndices decides which of n x-axis positions (evenly spaced
// across [0, viewBoxWidth] the way A2UILineChart.vue's x(i) lays out
// points) get a visible label, given the labels' text and the REAL,
// measured pixel width the row renders at. Review fix (#67 step 2 round 3,
// item 3 follow-up): the old thinning picked "at most 8, evenly by index"
// with no idea how wide the labels or the container actually were — caught
// live in QA, a 6-point monthly series ("2026-01".."2026-06") already
// overlapped in the ~300px-wide agent-panel card, well under the old cap of
// 8. This instead greedily keeps a label only once the previous kept one is
// at least one label-width + padding away in real pixels, so two shown
// labels can never overlap regardless of n, container width, or label
// length. Index 0 is always kept; the last index is kept too unless it
// would crowd the previous pick, in which case it replaces it (so the
// series' end is still represented instead of two labels doubling up).
//
// charPx/padPx default to the axis label's fixed 11px monospace font
// (a2ui.css's .a2ui-linechart__axis-x, JetBrains Mono, ~0.6em per digit)
// plus a small breathing-room pad; callers with a different label font
// pass their own.
export function pickLabelIndices(
  labels: string[],
  containerWidthPx: number,
  viewBoxWidth: number,
  charPx = 7.2,
  padPx = 12,
): number[] {
  const n = labels.length
  if (n === 0) return []
  if (n === 1) return [0]

  const widthPx = containerWidthPx > 0 ? containerWidthPx : viewBoxWidth
  const scale = widthPx / viewBoxWidth
  const innerW = viewBoxWidth
  const xPx = (i: number) => (n === 1 ? innerW / 2 : (i / (n - 1)) * innerW) * scale
  const maxLabelWidthPx = Math.max(...labels.map((l) => l.length * charPx)) + padPx

  const picked: number[] = [0]
  for (let i = 1; i < n; i++) {
    if (xPx(i) - xPx(picked[picked.length - 1]) >= maxLabelWidthPx) {
      picked.push(i)
    }
  }
  const last = n - 1
  if (picked[picked.length - 1] !== last) {
    if (xPx(last) - xPx(picked[picked.length - 1]) < maxLabelWidthPx && picked.length > 1) {
      picked[picked.length - 1] = last
    } else {
      picked.push(last)
    }
  }
  return picked
}

// clampGap bounds a container's "gap" field to a sane numeric pixel range.
// The value feeds a :style binding (container.gap is the one A2UI field that
// legitimately needs one — flex/grid gap has no safe class-based
// alternative), so it is clamped to a plain finite number here, never passed
// through as a raw string, closing off any injection vector through that
// binding.
export function clampGap(gap: unknown): number {
  const n = typeof gap === 'number' ? gap : Number(gap)
  if (!Number.isFinite(n)) return 0
  return Math.min(Math.max(n, 0), 64)
}

// isBound: a data-bound component reads its rows from a query (a notebook or
// chat `sql`, or an artifact view's named `query`) instead of inline data.
export function isBound(c: { [key: string]: any }): boolean {
  return !!(c.sql || c.query)
}
