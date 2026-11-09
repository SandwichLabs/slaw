/**
 * useQueryEditor — the Query Explorer's state and actions (F10): the saved-query
 * list, the editor buffer and its "Run on" connection, run / save / export /
 * delete, the save-refused-or-changed-on-disk conflict flow (useConflict), the
 * pending-save flush for workspace switches, and the ?conn=&sql= intent from the
 * Connections rail. QueryEditorView only renders it.
 */
import { ref, shallowRef, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { App } from '../lib/bindings'
import { paramText, rememberRun, type RunMemory } from './useReceipt'
import { openEvidence } from './useEvidenceDrawer'
import { connectionList, defaultConnectionName, refreshConnections, openPromptFor, startConnectionTracking } from './useConnections'
import { errorText, isCancelled, isConflict } from '../lib/errors'
import { formatCell } from '../lib/formatCell'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { register as registerPendingSave } from './usePendingSaves'
import { hasWailsRuntime } from '../lib/wails'
import { useConflict } from './useConflict'
import { useQueryParams } from './useQueryParams'

// mentions: the statement names a connection in qualifier position (name.).
function mentions(sql: string, name: string): boolean {
  return new RegExp(`(^|[^A-Za-z0-9_"])"?${name.replace(/[^A-Za-z0-9_]/g, '')}"?\\s*\\.`).test(sql)
}

export function useQueryEditor() {
  const route = useRoute()
  const router = useRouter()

  // E2: the list and the result grid are replaced wholesale, never mutated in
  // place, so they need no deep reactivity (a result holds up to the row cap).
  const qp = useQueryParams()
  const queries = shallowRef<QuerySummary[]>([])
  const selectedId = ref<string | null>(null)
  // The store's revision of the selected query as loaded: sent back on save so a
  // copy that changed on disk meanwhile is a conflict, not an overwrite.
  const selectedRev = ref('')
  const sqlText = ref('')
  const savedSQL = ref('')
  const result = shallowRef<QueryResult | null>(null)
  const loading = ref(true)
  const running = ref(false)
  // Set when the last run was stopped with Stop: a neutral line, not an error.
  const cancelled = ref(false)
  const error = ref('')
  const showSaveModal = ref(false)
  const saveTitle = ref('')
  const errorMessage = ref('')
  const statusMessage = ref('')
  const showConfirmDelete = ref(false)
  const pendingDeleteId = ref<string | null>(null)
  const hoveredId = ref<string | null>(null)

  // The saved query's file changed on disk while there are unsaved edits (a git
  // pull, another editor, the agent): ConflictBanner offers reload / keep mine /
  // save as a copy. Typing is never blocked while it shows.
  const cf = useConflict({
    currentId: () => selectedId.value,
    fetchRev: async (id) => ((await App.GetQuery(id)) as { rev?: string } | null)?.rev,
  })
  const { conflict } = cf

  // Run-status metadata (W5): success/error badge + wall-clock of the last run.
  const lastRunAt = ref('')
  const lastRunOk = ref<boolean | null>(null)
  const ranOn = ref('')

  // The "Run on" choice of THIS editor (SPEC 08): per editor, saved with a saved
  // query, never session-global. '' is the workspace default.
  const conn = ref('')
  const savedConn = ref('')
  const tabId = Math.random().toString(36).slice(2, 8)

  const dirty = computed(() => sqlText.value.trim() !== '' && (sqlText.value !== savedSQL.value || (selectedId.value !== null && conn.value !== savedConn.value)))
  const currentTitle = computed(() => {
    const q = queries.value.find((x) => x.id === selectedId.value)
    return q?.title || 'untitled_query.sql'
  })
  // #91 P-F7: the grid only ever holds the first queryGridRowCap rows
  // (RunQuery caps server-side); total_rows carries the true count so the
  // cap bar can say what's missing.
  const truncated = computed(() => !!result.value && result.value.total_rows > result.value.row_count)
  // One formatCell call per cell, precomputed once rather than 3x per cell from the template.
  const formattedRows = computed(() => {
    const r = result.value
    if (!r) return []
    return (r.rows ?? []).map((row) => row.map((cell, j) => formatCell(cell, r.columns[j]?.type)))
  })

  async function loadQueries() {
    loading.value = true
    try {
      queries.value = await App.ListQueries()
    } catch { /* ignore */ }
    loading.value = false
  }

  async function selectQuery(id: string) {
    try {
      const q = await App.GetQuery(id)
      selectedId.value = id // only once it has loaded: a missing query must not leave a selection with another query's text
      selectedRev.value = q.rev ?? ''
      sqlText.value = q.sql
      savedSQL.value = q.sql
      conn.value = (q as { connection?: string }).connection ?? ''
      savedConn.value = conn.value
      saveTitle.value = q.title
      qp.load(q.parameters)
    } catch (e: any) {
      error.value = errorText(e)
    }
  }

  // ranOnText says where the last run went: the chosen connection plus any other
  // a qualified name pulled in ("DuckDB · ran across local + prod_pg").
  function ranOnText(sql: string): string {
    const base = conn.value || defaultConnectionName.value
    const also = connectionList.value.filter((c) => c.name !== base && mentions(sql, c.name)).map((c) => c.name)
    return also.length ? `ran across ${[base, ...also].join(' + ')}` : `ran on ${base}`
  }

  async function runQuery() {
    if (!sqlText.value.trim() || running.value) return
    // A saved query's declared $name parameters: checked here, bound in Go.
    const pv = qp.payload()
    if (!pv.ok) { error.value = pv.error; result.value = null; return }
    const bound = qp.hasParams.value && selectedId.value ? { queryId: selectedId.value, params: pv.params } : {}
    running.value = true
    cancelled.value = false
    error.value = ''
    result.value = null
    try {
      ranOn.value = ranOnText(sqlText.value)
      const ran: RunMemory = { sql: sqlText.value, connection: conn.value || undefined, params: 'params' in bound ? paramText(bound.params) : undefined }
      result.value = await App.RunQuery({ sql: sqlText.value, connection: conn.value, tabId, ...bound })
      // The drawer shows the values this run had even when the receipt keeps none.
      lastRun = ran
      rememberRun(result.value?.receipt_id, ran)
      lastRunOk.value = true
    } catch (e: any) {
      if (isCancelled(e)) {
        // Neutral: no Complete or Error badge for a run that never finished.
        cancelled.value = true
        lastRunOk.value = null
        lastRunAt.value = new Date().toLocaleTimeString()
        running.value = false
        return
      }
      error.value = errorText(e)
      lastRunOk.value = false
      // A connection the statement names may need a sign-in or an extension: say
      // so with the prompt, not only with an error line.
      await refreshConnections()
      const needy = connectionList.value.find((c) => ['needs-credentials', 'extension-missing'].includes(c.status) && (c.name === conn.value || mentions(sqlText.value, c.name)))
      if (needy) openPromptFor(needy)
    }
    lastRunAt.value = new Date().toLocaleTimeString()
    running.value = false
  }

  let lastRun: RunMemory = {}
  /** Open the Evidence drawer for the last run's result: its receipt, or why none was kept. */
  function showEvidence(e: MouseEvent) {
    const r = result.value
    if (r) openEvidence({ title: currentTitle.value, receiptId: r.receipt_id, note: r.receipt_note, memory: lastRun }, e)
  }

  /** Stop the run in flight (this tab's); the run then ends with the cancelled error. */
  function stopQuery() {
    if (running.value) void App.CancelRun(`query:${tabId}`)
  }

  async function saveQuery() {
    if (!saveTitle.value.trim()) return
    try {
      const saved = await App.SaveQueryOn({ id: selectedId.value || '', title: saveTitle.value, sql: sqlText.value, description: '', rev: selectedRev.value, connection: conn.value })
      savedConn.value = conn.value
      selectedId.value = saved.id
      // The save changed the file: its new revision goes with the next save.
      selectedRev.value = saved.rev ?? ''
      savedSQL.value = sqlText.value
      showSaveModal.value = false
      statusMessage.value = `Saved "${saveTitle.value}"`
      cf.clear()
      await loadQueries()
    } catch (e: any) {
      if (isConflict(e)) {
        showSaveModal.value = false
        cf.raise(false)
      } else {
        errorMessage.value = 'Save failed: ' + errorText(e)
      }
    }
  }

  async function resolveConflict(action: 'reload' | 'overwrite' | 'saveCopy') {
    const id = selectedId.value
    if (!id) { cf.clear(); return }
    if (action === 'reload' && conflict.value?.deleted) {
      cf.clear()
      newQuery()
      await loadQueries()
      return
    }
    await cf.resolving(async () => {
      const res = await App.ResolveConflict({
        kind: 'query', id, action,
        data: { title: saveTitle.value || currentTitle.value.replace('.sql', ''), sql: sqlText.value, description: '' },
        rev: cf.rev(),
        connection: conn.value,
      })
      cf.clear()
      if (action === 'overwrite') {
        selectedRev.value = res.rev
        savedSQL.value = sqlText.value
        statusMessage.value = 'Kept your version.'
      } else {
        await selectQuery(id)
        if (action === 'saveCopy') statusMessage.value = `Saved your version as "${res.title}".`
      }
      await loadQueries()
    }, (t) => { errorMessage.value = t })
  }

  // flushPending is registered with usePendingSaves so switching workspaces,
  // closing and quitting save an edited saved query first. A conflict, or a
  // scratch buffer that was never saved, makes it fail so the caller can ask.
  async function flushPending() {
    if (conflict.value) throw new Error('this query changed on disk and your edit is not saved yet')
    if (!dirty.value) return
    const id = selectedId.value
    if (!id) throw new Error('this query was never saved (use Save query)')
    try {
      const saved = await App.SaveQueryOn({ id, title: saveTitle.value || currentTitle.value.replace('.sql', ''), sql: sqlText.value, description: '', rev: selectedRev.value, connection: conn.value })
      selectedRev.value = saved.rev ?? ''
      savedSQL.value = sqlText.value
      savedConn.value = conn.value
    } catch (e: any) {
      if (isConflict(e)) cf.raise(false)
      throw new Error(errorText(e))
    }
  }

  // onDiskChange: another program changed queries/. With nothing unsaved the
  // editor quietly takes the new version; with unsaved edits the banner shows.
  function onDiskChange(p: { kind: string; id: string; op: string }) {
    if (p?.kind !== 'query' || p.id !== selectedId.value) return
    if (p.op === 'deleted') {
      if (!dirty.value) { newQuery(); statusMessage.value = 'That query was deleted on disk.' } else cf.raise(true)
      return
    }
    if (!dirty.value) {
      void selectQuery(p.id).then(() => { statusMessage.value = `"${currentTitle.value}" changed on disk and was reloaded.` })
      return
    }
    cf.raise(false)
  }

  // exportResults re-runs the current SQL server-side through DuckDB COPY and
  // writes CSV/Parquet to a user-picked path (empty dst = native save dialog).
  async function exportResults(format: 'csv' | 'parquet') {
    if (!sqlText.value.trim()) return
    try {
      const path = await App.ExportQueryResults(sqlText.value, format, '')
      if (path) statusMessage.value = `Exported ${format.toUpperCase()} → ${path}`
    } catch (e: any) {
      errorMessage.value = 'Export failed: ' + errorText(e)
    }
  }

  // askAgent opens the W0 wizard panel (AppShell owns it; this event is the
  // cross-component trigger) so the user can ask about the query they're writing.
  function askAgent() {
    window.dispatchEvent(new CustomEvent('slaw:open-agent'))
  }

  function deleteQuery(id: string) {
    pendingDeleteId.value = id
    showConfirmDelete.value = true
  }

  async function confirmDeleteQuery() {
    const id = pendingDeleteId.value
    showConfirmDelete.value = false
    pendingDeleteId.value = null
    if (!id) return
    try {
      await App.DeleteQuery(id)
      if (selectedId.value === id) {
        selectedId.value = null
        sqlText.value = ''
        savedSQL.value = ''
        qp.clear()
      }
      await loadQueries()
    } catch (e: any) {
      errorMessage.value = 'Failed: ' + errorText(e)
    }
  }

  function newQuery() {
    selectedId.value = null
    sqlText.value = ''
    conn.value = ''
    savedConn.value = ''
    savedSQL.value = ''
    result.value = null
    error.value = ''
    saveTitle.value = ''
    qp.clear()
    lastRunOk.value = null
    lastRunAt.value = ''
  }

  // "New query here" / "New query on this table" in the rail open a fresh editor on
  // that connection: ?conn=prod_pg&sql=SELECT…&new=<stamp>. ?open=<id> opens a saved
  // query (the Semantics view's metric card links to its query this way).
  function applyRouteIntent() {
    const open = route.query.open
    if (typeof open === 'string' && open) {
      void router.replace({ path: route.path })
      void selectQuery(open)
      return
    }
    const c = route.query.conn
    if (typeof c !== 'string' || !route.query.new) return
    newQuery()
    conn.value = c
    const q = route.query.sql
    if (typeof q === 'string') sqlText.value = q
    void router.replace({ path: route.path })
  }
  watch(() => [route.query.new, route.query.open], applyRouteIntent)

  watch(errorMessage, (msg) => {
    if (msg) setTimeout(() => { errorMessage.value = '' }, 4000)
  })
  watch(statusMessage, (msg) => {
    if (msg) setTimeout(() => { statusMessage.value = '' }, 4000)
  })

  const unsubs: Array<() => void> = []
  onMounted(() => {
    unsubs.push(registerPendingSave(() => 'The query editor', flushPending))
    loadQueries()
    startConnectionTracking()
    applyRouteIntent()
    if (hasWailsRuntime()) {
      // Re-fetch saved queries when the data underneath changes — an agent's
      // save_query tool call, another tab, etc. (#91).
      unsubs.push(EventsOn('data:changed', loadQueries))
      unsubs.push(EventsOn('workspace:changed-on-disk', onDiskChange))
    }
  })
  // Leaving the view mid-run stops it: the next mount has a new tabId, so nothing could cancel it later.
  onUnmounted(() => {
    for (const off of unsubs) off()
    if (running.value) void App.CancelRun(`query:${tabId}`)
  })

  return {
    qp, queries, selectedId, sqlText, result, loading, running, cancelled, error, showSaveModal, saveTitle, errorMessage,
    statusMessage, showConfirmDelete, pendingDeleteId, hoveredId, conflict, conflictBusy: cf.busy,
    lastRunAt, lastRunOk, ranOn, conn, dirty, currentTitle, truncated, formattedRows,
    selectQuery, runQuery, showEvidence, stopQuery, saveQuery, resolveConflict, exportResults, askAgent, deleteQuery,
    confirmDeleteQuery, newQuery,
  }
}
