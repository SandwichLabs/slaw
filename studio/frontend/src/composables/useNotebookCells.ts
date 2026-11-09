/**
 * useNotebookCells — the notebook editor's cell list and everything done to a
 * cell (F10): add / delete / move / collapse, the lazy CodeMirror editors, the
 * IntersectionObserver that lazy-mounts a2ui cells, running a sql cell, and the
 * AI-cell dialog that inserts or replaces cells. It never touches the save path:
 * the view's autosave watches the cells, and `onDelete` lets the save side
 * tombstone a removed id.
 */
import { ref, nextTick, type Ref } from 'vue'
import { App } from '../lib/bindings'
import { runLiveQuery, EMPTY_QUERY_MESSAGE } from '../a2ui/query'
import { createSlawEditor } from './useSlawEditor'
import { useAiCellDialog, type EnvelopeCell } from './useAiCellDialog'
import { bestView, describeShape } from '../lib/notebookViews'
import { cellTitle } from '../lib/cellSummary'
import { hasWailsRuntime } from '../lib/wails'
import { blankCell, envelopeToRaw, newCellId, newCellState, type CellState } from '../lib/notebookCells'
import { closeCompletion } from '@codemirror/autocomplete'
import { sqlCompletion } from '../lib/sqlComplete'

export function useNotebookCells(opts: { onDelete: (id: string) => void; notebookId?: () => string }) {
  // Cast: ref() would unwrap the CodeMirror EditorView type inside each cell's editor handle.
  const cells = ref<CellState[]>([]) as Ref<CellState[]>

  // loadAbort is this component's current "generation" — a fresh
  // AbortController per load. Aborting it tells every in-flight runLiveQuery
  // call (sql cells' own runs, and every a2ui cell's data-bound components, via
  // A2UIPage's `signal` prop) that this load no longer matters, so a result that
  // resolves after a notebook switch or unmount is discarded.
  let loadAbort: AbortController | null = null
  let cellObserver: IntersectionObserver | null = null

  const signal = () => loadAbort?.signal

  // beginLoad starts a new generation: the previous one's queries and observer
  // are dropped.
  function beginLoad() {
    loadAbort?.abort()
    loadAbort = new AbortController()
    cellObserver?.disconnect()
    cellObserver = null
  }

  function destroyEditors() {
    for (const cell of cells.value) cell.editor?.destroy()
  }

  // teardown: editors, observer and in-flight queries (a notebook switch or unmount).
  function teardown() {
    destroyEditors()
    cellObserver?.disconnect()
    loadAbort?.abort()
  }

  function observeCell(cell: CellState, el: Element | null) {
    cell.rootEl = el as HTMLElement | null
    if (cell.type !== 'a2ui' || cell.visible) return
    if (!el || !cellObserver) return
    cellObserver.observe(el)
  }

  function setupCellObserver() {
    cellObserver?.disconnect()
    if (typeof IntersectionObserver === 'undefined') {
      // jsdom (Vitest) and some embedded webviews have no IntersectionObserver
      // — degrade to "always visible" rather than leaving a2ui cells stuck
      // permanently unmounted.
      for (const cell of cells.value) cell.visible = true
      return
    }
    cellObserver = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue
          const id = (entry.target as HTMLElement).dataset.cellId
          const cell = cells.value.find((c) => c.id === id)
          if (cell) cell.visible = true
          cellObserver?.unobserve(entry.target)
        }
      },
      { rootMargin: '200px' },
    )
    for (const cell of cells.value) {
      if (cell.type === 'a2ui' && !cell.visible && cell.rootEl) cellObserver.observe(cell.rootEl)
    }
  }

  function mountEditors() {
    for (const cell of cells.value) {
      if (cell.type === 'a2ui') continue
      if (cell.collapsed) continue
      if (cell.type === 'sql' && !cell.sourceOpen) continue
      if (!cell.editorEl || cell.editor) continue
      cell.editor = createSlawEditor(cell.editorEl, {
        value: cell.source,
        lang: cell.type === 'sql' ? 'sql' : 'markdown',
        onChange: (v) => { cell.source = v },
        extraKeymap: cell.type === 'sql'
          ? [{ key: 'Mod-Enter', run: (view) => { closeCompletion(view); runCell(cell); return true } }]
          : [],
        extensions: cell.type === 'sql' ? sqlCompletion({ connection: () => cell.connection }) : [],
      })
    }
  }

  async function runCell(cell: CellState) {
    if (cell.type !== 'sql') return
    // An empty cell has nothing to run: say so here instead of sending a blank
    // statement through the read-only gate (which would refuse it, #3).
    if (!cell.source.trim()) {
      cell.result = { columns: [], types: [], rows: [], error: EMPTY_QUERY_MESSAGE }
      return
    }
    cell.running = true
    const signal = loadAbort?.signal
    // 'high' priority (review fix, step 4a round 2 #5): a notebook sql cell's
    // Run button/Ctrl-Enter is a direct, user-initiated action — it must
    // never wait behind a background a2ui component storm's auto-fetches
    // (which stay 'low', the default), which is exactly what "returns in
    // under 1s regardless of how many a2ui components are mounted" requires.
    // The source names the cell, so its receipt says which notebook and cell asked (Go checks it).
    const nbId = opts.notebookId?.()
    const source: RenderSource | undefined = nbId ? { kind: 'cell', notebook: nbId, cell: cell.id } : undefined
    const res = await runLiveQuery(cell.source, signal, 'high', cell.connection || undefined, source)
    if (res.aborted) return // this load was superseded (notebook switch / unmount) — discard
    cell.running = false
    cell.lastRunSource = cell.source.trim()
    if (res.error) {
      cell.result = { columns: [], types: [], rows: [], error: res.error }
      // Any run that errors opens the drawer so the failing SQL is visible —
      // it never auto-closes afterward, even once a later run succeeds.
      cell.sourceOpen = true
      nextTick(mountEditors)
      return
    }
    cell.result = { columns: res.columns, types: res.types, rows: res.rows, receipt_id: res.receipt_id, receipt_note: res.receipt_note }
    if (!cell.viewPinned) {
      cell.view = bestView(describeShape(res.columns, res.rows, res.types))
    }
  }

  async function runAll() {
    for (const cell of cells.value) {
      if (cell.type === 'sql' && cell.source.trim()) {
        await runCell(cell)
      }
    }
  }

  // --- cell list ops ---

  function addCell(type: CellState['type'], atEnd = true) {
    const cell = newCellState(blankCell(type))
    if (type === 'markdown') cell.mdPreview = false
    if (atEnd) cells.value.push(cell)
    else cells.value.unshift(cell)
    // sourceOpen is already true for a hand-added sql cell (its source starts
    // empty — see newCellState), so mounting it right away and focusing the
    // editor lets the person start typing immediately.
    nextTick(() => { mountEditors(); if (type === 'sql') cell.editor?.view.focus() })
  }

  function deleteCell(cell: CellState) {
    cell.editor?.destroy()
    cells.value = cells.value.filter((c) => c.id !== cell.id)
    opts.onDelete(cell.id)
  }

  function moveCell(cell: CellState, delta: number) {
    const i = cells.value.findIndex((c) => c.id === cell.id)
    const j = i + delta
    if (i === -1 || j < 0 || j >= cells.value.length) return
    const arr = cells.value.slice()
    ;[arr[i], arr[j]] = [arr[j], arr[i]]
    cells.value = arr
  }

  function toggleMdPreview(cell: CellState) {
    if (!cell.mdPreview) {
      // Leaving edit mode: tear down the editor so its DOM node (about to be
      // removed by the v-if swap) doesn't leak a live CodeMirror instance.
      cell.editor?.destroy()
      cell.editor = null
    }
    cell.mdPreview = !cell.mdPreview
    if (!cell.mdPreview) nextTick(mountEditors)
  }

  function toggleCollapsed(cell: CellState) {
    cell.collapsed = !cell.collapsed
    if (!cell.collapsed) nextTick(mountEditors)
    else { cell.editor?.destroy(); cell.editor = null }
  }

  // toggleSource opens/closes the SQL drawer (or the a2ui "card definition"
  // drawer). It does NOT destroy the editor on close — nothing writes
  // cell.source while an editor is unmounted-but-alive (merge only appends
  // cells; replace destroys and rebuilds), so keeping it mounted preserves
  // undo history across a peek.
  function toggleSource(cell: CellState) {
    cell.sourceOpen = !cell.sourceOpen
    if (cell.sourceOpen) nextTick(mountEditors)
  }

  // --- AI cell authoring (#66 part B) ---

  // insertCellFromEnvelope inserts right after afterCellId's CURRENT position
  // (re-looked-up here, not trusted from a stale index the caller might have
  // captured earlier) when given, so the drift/C2 "added as a new cell below"
  // path lands the new cell where "below" actually means. With no afterCellId
  // (create mode; no live target to be below) it appends at the end.
  // AI-authored sql cells stay closed (newCellState's sourceOpen is only true
  // for an EMPTY source, and the agent always proposes actual SQL) — if the
  // follow-up auto-run below errors, runCell's own error-open path takes care
  // of it, which is exactly the "any run that errors" rule.
  function insertCellFromEnvelope(ec: EnvelopeCell, afterCellId?: string) {
    const cell = newCellState(envelopeToRaw(newCellId(), ec))
    if (ec.type === 'markdown') cell.mdPreview = false
    const afterIdx = afterCellId ? cells.value.findIndex((c) => c.id === afterCellId) : -1
    if (afterIdx !== -1) cells.value.splice(afterIdx + 1, 0, cell)
    else cells.value.push(cell)
    nextTick(mountEditors)
    if (cell.type === 'sql') nextTick(() => runCell(cell))
  }

  // replaceCellFromEnvelope destroys the target cell's editor first (so a
  // stale CodeMirror instance never lingers bound to the old source) then
  // swaps its whole entry in place — never removes+re-adds, so its position
  // in the notebook is preserved.
  function replaceCellFromEnvelope(cellId: string, ec: EnvelopeCell) {
    const idx = cells.value.findIndex((c) => c.id === cellId)
    if (idx === -1) { insertCellFromEnvelope(ec); return } // target vanished mid-task — never silently drop the proposal
    const previousSourceOpen = cells.value[idx].sourceOpen
    // A proposal that names no connection replaces the cell in place, so the cell
    // keeps the one it had (#117): the preview ran there, and Apply must match.
    const prevConnection = cells.value[idx].connection
    if (ec.type === 'sql' && !ec.connection && prevConnection) ec = { ...ec, connection: prevConnection }
    cells.value[idx].editor?.destroy()
    const replacement = newCellState(envelopeToRaw(cellId, ec))
    if (ec.type === 'markdown') replacement.mdPreview = false
    // A drawer the person had open to read/compare the SQL must not snap shut
    // just because the AI replaced its content — "never auto-closes" applies
    // here too, not just to a run error. A closed drawer stays closed
    // (newCellState's own default for non-empty SQL already covers that).
    replacement.sourceOpen = previousSourceOpen || replacement.sourceOpen
    cells.value.splice(idx, 1, replacement)
    nextTick(mountEditors)
    if (replacement.type === 'sql') nextTick(() => runCell(replacement))
  }

  // snapshotAiCell/loadAiCellTables/aiCellNotebookSummary feed the AI-cell
  // dialog's drift detection (review fix, finding 6) and request context (finding 7).
  const aiCellDialog = useAiCellDialog({
    cells,
    insertCell: insertCellFromEnvelope,
    replaceCell: replaceCellFromEnvelope,
    snapshotCell(id: string) {
      const c = cells.value.find((c) => c.id === id)
      if (!c) return null
      return { source: c.source, label: c.label, view: c.view, connection: c.connection }
    },
    async getTables(): Promise<string[]> {
      if (!hasWailsRuntime()) return []
      try {
        return (await App.ListTables()) || []
      } catch {
        return []
      }
    },
    getNotebookSummary: () => cells.value.map(cellTitle).filter((s) => s.trim() !== ''),
  })

  function openAiCellEdit(cell: CellState, titleOverride?: string) {
    aiCellDialog.openEdit({ id: cell.id, type: cell.type }, titleOverride)
  }

  return {
    cells, signal, beginLoad, destroyEditors, teardown, observeCell, setupCellObserver, mountEditors,
    runCell, runAll, addCell, deleteCell, moveCell, toggleMdPreview, toggleCollapsed, toggleSource,
    insertCellFromEnvelope, replaceCellFromEnvelope, aiCellDialog, openAiCellEdit,
  }
}
