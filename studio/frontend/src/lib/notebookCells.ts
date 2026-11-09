// notebookCells — the notebook editor's cell model and the pure functions over
// it (F10): the runtime CellState, the save payload, the synthesized A2UI page a
// sql cell's result renders as, and the labels the cell toolbar shows. No Vue
// lifecycle and no bindings, so NotebookView, NotebookCell and the notebook
// composables share one definition and the specs can call these directly.
import { reactive } from 'vue'
import { toPoints } from '../a2ui/query'
import type { A2UIPageT } from '../a2ui/types'
import type { MotionT } from '../a2ui/motion'
import type { SlawEditorInstance } from '../composables/useSlawEditor'
import type { EnvelopeCell } from '../composables/useAiCellDialog'
import { isSeriesShape } from '../a2ui/multiseries'
import { describeShape, viewFit, VIEW_COMPONENT, VIEW_HINTS, VIEW_LABELS, VIEWS, type Shape, type ViewKind } from './notebookViews'
import { sqlSummary, cellTitle, CELL_FALLBACK_TITLE } from './cellSummary'

export interface RawCell {
  id: string
  type: 'sql' | 'markdown' | 'a2ui'
  collapsed?: boolean
  source?: string
  view?: ViewKind
  viewPinned?: boolean
  label?: string
  // Notebook format v2: the connection whose catalog is the default for
  // this cell's unqualified table names. Unset = the session default.
  connection?: string
  // The chart animation option (a2ui's "motion"); unset = the default spring.
  motion?: MotionT
  page?: A2UIPageT
}

export interface CellState {
  id: string
  type: 'sql' | 'markdown' | 'a2ui'
  collapsed: boolean
  source: string
  view: ViewKind
  viewPinned: boolean
  label: string
  connection: string
  motion: MotionT | null
  page: A2UIPageT | null
  // Runtime-only (never saved):
  lastRunSource: string | null
  // receipt_id / receipt_note: the run's receipt, or why it has none (the Evidence button)
  result: { columns: string[]; types: string[]; rows: any[][]; error?: string; receipt_id?: string; receipt_note?: ReceiptNote } | null
  running: boolean
  mdPreview: boolean
  // SQL drawer / a2ui card definition open. Runtime-only, never saved (not
  // in buildSavePayload or the autosave key).
  sourceOpen: boolean
  editorEl?: HTMLElement | null
  editor?: SlawEditorInstance | null
  // Lazy-mount (review fix, step 4a #1): an a2ui cell's A2UIPage — and every
  // live query its data-bound components fire — is not rendered until the
  // cell has actually scrolled into view, via the IntersectionObserver set
  // up in useNotebookCells. A notebook packed with many a2ui cells (bounded,
  // but still up to 500) never fires more than a screenful of live queries
  // at once just from being opened.
  visible: boolean
  rootEl?: HTMLElement | null
}

export type SavePayload = ReturnType<typeof buildSavePayload>

export function newCellState(raw: RawCell): CellState {
  return reactive({
    id: raw.id,
    type: raw.type,
    collapsed: !!raw.collapsed,
    source: raw.source ?? '',
    view: (raw.view as ViewKind) ?? 'table',
    viewPinned: !!raw.viewPinned,
    label: raw.label ?? '',
    connection: raw.connection ?? '',
    motion: raw.motion ?? null,
    page: raw.page ?? null,
    lastRunSource: null,
    result: null,
    running: false,
    mdPreview: (raw.source ?? '').trim() !== '',
    sourceOpen: raw.type === 'sql' && !(raw.source ?? '').trim(),
    editorEl: null,
    editor: null,
    visible: raw.type !== 'a2ui', // only a2ui cells start hidden/lazy
    rootEl: null,
  }) as CellState
}

export function newCellId(): string {
  return 'cell_' + Math.random().toString(36).slice(2, 10)
}

// blankCell is the RawCell a hand-added cell of `type` starts as.
export function blankCell(type: CellState['type']): RawCell {
  const id = newCellId()
  if (type === 'sql') return { id, type, source: '', view: 'table', viewPinned: false, label: '' }
  if (type === 'markdown') return { id, type, source: '' }
  // internal/a2ui.Validate rejects zero components, so a freshly added
  // a2ui cell needs a placeholder or it would fail Coerce on the very
  // next autosave.
  return { id, type, page: { title: 'Untitled', components: [{ id: 'placeholder', type: 'text', text: 'Empty A2UI cell — edit the source below.' }] } }
}

// envelopeToRaw converts a task-mode EnvelopeCell (the agent's proposal) into a
// RawCell, so an agent-authored cell is built exactly like a hand-typed one.
export function envelopeToRaw(id: string, ec: EnvelopeCell): RawCell {
  if (ec.type === 'sql') return { id, type: 'sql', source: ec.sql || '', view: (ec.view as ViewKind) || 'table', viewPinned: !!ec.view, label: ec.label || '', ...(ec.connection ? { connection: ec.connection } : {}) }
  return { id, type: 'markdown', source: ec.markdown || '' }
}

// buildSavePayload takes id/title/cellsSnapshot as EXPLICIT arguments,
// never reads live refs itself — every caller passes in values it captured at
// the moment that mattered (load time for the id, "right now" for title/cells),
// so a payload already under construction can never pick up a mutation (a route
// change, a teardown) that happens mid-build. Outputs (`result`) are never part
// of it.
export function buildSavePayload(id: string, snapshotTitle: string, snapshotCells: CellState[]) {
  return {
    id,
    title: snapshotTitle,
    cells: snapshotCells.map((c) => {
      if (c.type === 'sql') return { id: c.id, type: 'sql', collapsed: c.collapsed, source: c.source, view: c.view, viewPinned: c.viewPinned, label: c.label, ...(c.connection ? { connection: c.connection } : {}), ...(c.motion ? { motion: c.motion } : {}) }
      if (c.type === 'markdown') return { id: c.id, type: 'markdown', collapsed: c.collapsed, source: c.source }
      return { id: c.id, type: 'a2ui', collapsed: c.collapsed, page: c.page }
    }),
  }
}

// editKey is a payload with the auto-chosen view of every unpinned sql cell
// removed: running a cell picks its view on its own (and autosave persists it a
// moment later), which is not an edit worth a conflict banner.
export function editKey(p: SavePayload): string {
  return JSON.stringify({
    ...p,
    cells: p.cells.map((c: any) => (c.type === 'sql' && !c.viewPinned ? { ...c, view: undefined } : c)),
  })
}

export function shapeOf(cell: CellState): Shape | null {
  if (!cell.result || cell.result.error) return null
  return describeShape(cell.result.columns, cell.result.rows, cell.result.types)
}

export function isStale(cell: CellState): boolean {
  return cell.type === 'sql' && cell.lastRunSource !== null && cell.lastRunSource !== cell.source.trim()
}

// pickView honors the person's explicit view choice and pins it — a mismatch
// note (mismatchInfo) is shown inline instead of silently overriding it.
export function pickView(cell: CellState, view: ViewKind) {
  cell.view = view
  cell.viewPinned = true
}

// synthesizedPage builds the one-component A2UI page a sql cell's result
// renders as. Review fix, step 4a #10: the label is set on EITHER the page
// title OR the component itself, never both — every leaf component
// (A2UIStat/Table/BarChart/LineChart) already renders `component.label` as
// its own caption, so also setting `page.title` to the same text rendered
// it a second time, right above the first, on every sql cell that had a
// label. The page itself never needs its own title here: it holds exactly
// one component, whose own caption is enough.
export function synthesizedPage(cell: CellState): A2UIPageT | null {
  if (!cell.result || cell.result.error) return null
  const { columns, rows, types } = cell.result
  const componentType = VIEW_COMPONENT[cell.view]
  const base = { id: 'c', type: componentType, label: cell.label || undefined }
  // Every view but table and stat is a chart, and charts take the cell's motion.
  const motion = cell.motion && cell.view !== 'table' && cell.view !== 'stat' ? { motion: cell.motion } : {}
  let fields: Record<string, any> = {}
  switch (cell.view) {
    case 'table':
      // types is client-only: stamped on the synthesized component so
      // A2UITable can format each column with its real DuckDB type (DECIMAL
      // scale, BIGINT grouping, …). Never part of the A2UI schema and never
      // persisted — synthesizedPage's output is render-only (a2ui/types.ts).
      fields = { columns, rows, types }
      break
    case 'bar':
    case 'line': {
      // Several series (a long or wide result) go to the chart whole, as
      // columns + rows with their types; one series stays a plain point list.
      const several = isSeriesShape({ columns, rows, types })
      if (several) fields = { columns, rows, types }
      else fields = cell.view === 'bar' ? { bars: toPoints(columns, rows) } : { points: toPoints(columns, rows) }
      break
    }
    case 'stat':
      // a2ui's "stat" type requires a label — fall back to a column name or
      // a generic "Result" rather than an empty required field.
      fields = { label: cell.label || columns[0] || 'Result', value: rows[0]?.[0] ?? null, types: [types[0]] }
      break
    default:
      // The KINETIC charts read the result as is: columns + rows, the same
      // shape their own sql binding returns.
      fields = { columns, rows }
  }
  return { title: '', components: [{ ...base, ...fields, ...motion }] }
}

// mismatchInfo describes why the cell's current (pinned) view does not fit
// its last result, for the mismatch note: a VIEW_LABEL-prefixed reason, the
// shape actually returned, and which OTHER views would fit, each offered as a
// one-click switch (review fix, step 4a #9).
export function mismatchInfo(cell: CellState): { label: string; hint: string; shapeLine: string; fits: ViewKind[] } | null {
  const shape = shapeOf(cell)
  if (!shape) return null
  const fit = viewFit(cell.view, shape)
  if (fit.ok) return null
  const numeric = shape.numeric.length ? shape.numeric.join(', ') : 'none'
  return {
    label: VIEW_LABELS[cell.view],
    hint: VIEW_HINTS[cell.view],
    shapeLine: `this query returned ${shape.rowCount} row${shape.rowCount === 1 ? '' : 's'}, ${shape.cols.length} column${shape.cols.length === 1 ? '' : 's'} (numeric: ${numeric})`,
    fits: VIEWS.filter((v) => v !== cell.view && viewFit(v, shape).ok),
  }
}

export const CELL_ICON = { sql: 'terminal', markdown: 'fileText', a2ui: 'barChart' } as const
export const CELL_KIND = { sql: 'Query cell', markdown: 'Text cell', a2ui: 'Card cell' } as const

export function titleOf(cell: CellState): string {
  return cellTitle(cell) || CELL_FALLBACK_TITLE[cell.type]
}

// ariaTitleOf caps titleOf at 60 chars for the minimize chevron's
// aria-label — an unlabelled sql cell's title falls back to its first SQL
// line, which can run well past what a screen reader should read as a
// button name.
export function ariaTitleOf(cell: CellState): string {
  const t = titleOf(cell)
  return t.length > 60 ? t.slice(0, 60).trimEnd() + '…' : t
}

// summaryOf is the "Show SQL · N lines · first line" text.
export function summaryOf(cell: CellState): string {
  const s = sqlSummary(cell.source)
  return `Show SQL · ${s.lines} line${s.lines === 1 ? '' : 's'}${s.firstLine ? ' · ' + s.firstLine : ''}`
}

// Part B seam: the "Generate with AI" affordance is on now that task-mode
// authoring (add_query_cell/create_dashboard tools + the AgentTask binding,
// #66 part B) has landed.
export const AI_CELL_ENABLED = true
