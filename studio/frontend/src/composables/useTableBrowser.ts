/**
 * useTableBrowser — what the table browser knows and shows (F10): the catalog of
 * tables across connections, the selected table with its description,
 * capabilities (why it can't be edited) and page of rows, sorting, paging, drop,
 * and the structured filter (Filter-by-Selection and Filter-by-Form). Row and
 * cell edits are in useTableCrud, which works on the data held here.
 */
import { ref, shallowRef, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { App } from '../lib/bindings'
import { isReadOnly, workspaceInfo } from './useWorkspace'
import { connect } from './useConnections'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { errorText } from '../lib/errors'
import { formatCell } from '../lib/formatCell'
import { catalogInStorage } from '../lib/connections'
import { hasWailsRuntime } from '../lib/wails'

export const FILTER_OPS = [
  { value: 'eq', label: '=' },
  { value: 'neq', label: '≠' },
  { value: 'lt', label: '<' },
  { value: 'gt', label: '>' },
  { value: 'lte', label: '≤' },
  { value: 'gte', label: '≥' },
  { value: 'like', label: 'like' },
  { value: 'is_null', label: 'is null' },
  { value: 'is_not_null', label: 'is not null' },
]

export function useTableBrowser() {
  // #82: opening /tables?table=orders (e.g. "Open table →" after an import)
  // pre-selects that table once the table list has loaded.
  const route = useRoute()

  // Opened read-only (another program owns the database): nothing here can write,
  // and the controls say why instead of failing later.
  const readOnlyReason = computed(() => `Opened read-only: ${workspaceInfo.value?.holder?.text || 'another program'} is using this workspace's database. Take over the workspace to make changes.`)
  // #117: why the selected table can't be edited (a read-only connection, a view,
  // a locked file), from GetTableCapabilities. Null means editable.
  const caps = ref<TableCapabilities | null>(null)
  // The read-only connection is a lake whose catalog is in object storage: it can
  // only be read, so the banner does not offer to turn Read-only off.
  const catalogReadOnly = ref(false)
  const locked = computed(() => isReadOnly.value || (caps.value !== null && !caps.value.editable))
  const lockText = computed(() => {
    if (caps.value && !caps.value.editable) return caps.value.text
    return readOnlyReason.value
  })

  // E2: the catalog and the page of rows are replaced wholesale on every load,
  // so they are shallow (a page can hold thousands of cells). A cell edited in
  // place calls triggerRef (useTableCrud).
  // Every table across the workspace's attached connections, as qualified names
  // (connection.schema.table) the datasheet bindings accept, plus the groups the
  // list renders (grouped by connection). A connection that is not connected yet
  // is listed without tables and is never dialled from here.
  const tables = shallowRef<string[]>([])
  const groups = shallowRef<CatalogGroup[]>([])
  const selectedTable = ref<string | null>(null)
  const tableInfo = ref<TableInfo | null>(null)
  const tableData = shallowRef<TableData | null>(null)
  const loading = ref(true)
  const dataLoading = ref(false)
  const currentPage = ref(1)
  const pageSize = ref(50)
  const sortCol = ref('')
  const sortDir = ref('ASC')
  const errorMessage = ref('')
  // Why the open table's rows could not be read (Go has already put it in plain words:
  // data outside every storage scope, a connection that needs a sign-in, ...). Unlike
  // errorMessage it stays until the next load, so the grid never reads as an empty table.
  const loadError = ref('')
  const statusMessage = ref('')
  const showConfirmDrop = ref(false)
  const pendingDropTable = ref<string | null>(null)

  // base table or view, for the Drop table button (a view is dropped elsewhere)
  const selectedKind = computed(() => {
    for (const g of groups.value) for (const t of g.tables) if (t.qualified === selectedTable.value) return t.kind
    return 'table'
  })
  // Connections that could not be listed (locked by another program, an error); a
  // plain "not connected yet" is not a problem.
  const unavailable = computed(() => groups.value.filter((g) => g.status !== 'ok' && g.status !== 'not-connected'))

  // colType returns the DuckDB type name for a column of the currently loaded
  // table's data (ScanRows-sourced, e.g. "DECIMAL(18,4)"), for formatCell/
  // cellEditText — distinct from the DDL-sourced tableInfo type, which drives
  // which input control to show.
  function colType(colIdx: number): string | undefined {
    return tableData.value?.types?.[colIdx]
  }

  // One formatCell call per cell: computed once and indexed (formattedRows[i][j])
  // from the template. The template still reads the raw cell value directly where
  // it needs it (checkbox state, filter-by-selection's exact-match value, the
  // editor seed) — formatCell's job is display only.
  const formattedRows = computed(() => {
    const td = tableData.value
    if (!td) return []
    return td.rows.map((row) => row.map((cell, j) => formatCell(cell, colType(j))))
  })

  // --- structured filter (P2.9) ---
  // activeFilter is the applied FilterSpec (null = no filter). When set, loadData
  // routes through GetTableDataFiltered so both the rows AND the record-count bar
  // reflect the filtered total (filteredTotal).
  const activeFilter = ref<FilterSpec | null>(null)
  const filteredTotal = ref<number | null>(null)
  // Filter-by-Form panel state: a list of editable condition rows.
  const showFbF = ref(false)
  const fbfCombinator = ref<'AND' | 'OR'>('AND')
  const fbfConditions = ref<{ column: string; op: string; value: string }[]>([])

  async function loadTables() {
    loading.value = true
    try {
      groups.value = (await App.ListCatalogGroups()) || []
      // A deep link (/tables?table=prod_pg.public.orders) to a connection that has
      // not been used yet connects it first (the rail does the same on expand).
      const want = route.query.table
      const cn = typeof want === 'string' && want.includes('.') ? want.split('.')[0] : ''
      if (cn && groups.value.some((g) => g.connection === cn && g.status === 'not-connected')) {
        await connect(cn)
        groups.value = (await App.ListCatalogGroups()) || []
      }
      tables.value = groups.value.flatMap((g) => g.tables.map((t) => t.qualified))
    } catch { /* ignore */ }
    loading.value = false
    applyRouteTable()
  }

  // A qualified table of a connection in a schema other than its default
  // (lake.warehouse.dim_students, from the rail) is not in the group lists,
  // which cover each connection's default schema only: it is still openable.
  function inOtherSchema(t: string): boolean {
    const parts = t.split('.')
    return parts.length === 3 && groups.value.some((g) => g.connection === parts[0] && g.status === 'ok')
  }

  // applyRouteTable selects the table named by ?table= (once it's in the loaded
  // list), without disturbing a selection the user already made.
  function applyRouteTable() {
    const t = route.query.table
    if (typeof t !== 'string' || !t) return
    if (t === selectedTable.value) return
    // A bare name (e.g. "Open table →" after an import) means the default
    // connection's table of that name.
    const qualified = tables.value.includes(t) || inOtherSchema(t)
      ? t
      : groups.value.find((g) => g.default)?.tables.find((x) => x.name === t)?.qualified
    if (qualified && qualified !== selectedTable.value) selectTable(qualified)
  }
  watch(() => route.query.table, applyRouteTable)

  // Re-fetch the table list (and the open table's rows) when the data underneath
  // changes — an agent-driven create_table/create_record/import, another tab, etc.
  async function onDataChanged() {
    await loadTables()
    if (selectedTable.value) {
      if (!tables.value.includes(selectedTable.value) && !inOtherSchema(selectedTable.value)) {
        // The table the agent dropped/renamed out from under us.
        selectedTable.value = null
        tableInfo.value = null
        tableData.value = null
        return
      }
      await loadData()
    }
  }

  // onSelect lets useTableCrud drop its per-table state (a stale cell error)
  // whenever a table is (re)selected.
  const selectHooks: Array<() => void> = []
  const onSelect = (fn: () => void) => { selectHooks.push(fn) }

  async function selectTable(name: string) {
    selectedTable.value = name
    currentPage.value = 1
    sortCol.value = ''
    sortDir.value = 'ASC'
    for (const h of selectHooks) h()
    clearFilter()
    try {
      tableInfo.value = await App.DescribeTable(name)
    } catch { tableInfo.value = null }
    try {
      caps.value = await App.GetTableCapabilities(name)
    } catch { caps.value = null }
    catalogReadOnly.value = false
    if (caps.value?.reason === 'connection-read-only') {
      try { catalogReadOnly.value = catalogInStorage((await App.GetConnection(caps.value.connection)).def) } catch { /* the generic advice stands */ }
    }
    await loadData()
  }

  async function loadData() {
    if (!selectedTable.value) return
    dataLoading.value = true
    loadError.value = ''
    try {
      if (activeFilter.value) {
        tableData.value = await App.GetTableDataFiltered(
          selectedTable.value, activeFilter.value, currentPage.value, pageSize.value, sortCol.value, sortDir.value,
        )
        filteredTotal.value = tableData.value?.total_rows ?? null
      } else {
        tableData.value = await App.GetTableData(
          selectedTable.value, currentPage.value, pageSize.value, sortCol.value, sortDir.value,
        )
      }
    } catch (e: unknown) {
      tableData.value = null
      loadError.value = errorText(e)
    }
    dataLoading.value = false
  }

  // applyFilter sets the active FilterSpec and reloads from page 1.
  async function applyFilter(spec: FilterSpec) {
    activeFilter.value = spec
    currentPage.value = 1
    await loadData()
  }

  // clearFilter removes any active filter and reverts to the unfiltered view.
  function clearFilter() {
    activeFilter.value = null
    filteredTotal.value = null
    showFbF.value = false
    fbfConditions.value = []
  }

  // filterBySelection applies a single eq condition on the clicked cell's column
  // and value (Access Filter-by-Selection).
  async function filterBySelection(colName: string, value: any) {
    await applyFilter({
      combinator: 'AND',
      conditions: [{ column: colName, op: 'eq', value }],
    })
  }

  // openFbF opens the Filter-by-Form panel, seeding one empty condition row per
  // the first table columns so the builder can pick column/op/value.
  function openFbF() {
    if (!tableInfo.value) return
    const cols = tableInfo.value.columns
    fbfCombinator.value = 'AND'
    fbfConditions.value = [
      { column: cols[0]?.name ?? '', op: 'eq', value: '' },
      { column: cols[1]?.name ?? cols[0]?.name ?? '', op: 'eq', value: '' },
    ]
    showFbF.value = true
  }

  function addFbFCondition() {
    const cols = tableInfo.value?.columns ?? []
    fbfConditions.value.push({ column: cols[0]?.name ?? '', op: 'eq', value: '' })
  }

  // applyFbF builds a FilterSpec from the panel's condition rows, dropping any
  // rows with no column or (for value-bearing ops) no value.
  async function applyFbF() {
    const conditions = fbfConditions.value
      .filter((c) => c.column && (c.op === 'is_null' || c.op === 'is_not_null' || c.value !== ''))
      .map((c) => (c.op === 'is_null' || c.op === 'is_not_null')
        ? { column: c.column, op: c.op }
        : { column: c.column, op: c.op, value: c.value })
    if (conditions.length === 0) return
    showFbF.value = false
    await applyFilter({ combinator: fbfCombinator.value, conditions })
  }

  // saveFilterAsQuery persists the active filter as a named query (P2.6).
  async function saveFilterAsQuery() {
    if (!activeFilter.value || !selectedTable.value) return
    const columns = tableData.value?.columns ?? []
    const title = `${selectedTable.value} (filtered)`
    try {
      await App.SaveFilterAsQuery(selectedTable.value, activeFilter.value, columns, title)
      statusMessage.value = `Saved query "${title}". Find it under Queries.`
    } catch (e: any) {
      errorMessage.value = 'Failed: ' + errorText(e)
    }
  }

  function toggleSort(col: string) {
    if (sortCol.value === col) {
      sortDir.value = sortDir.value === 'ASC' ? 'DESC' : 'ASC'
    } else {
      sortCol.value = col
      sortDir.value = 'ASC'
    }
    loadData()
  }

  function prevPage() {
    if (currentPage.value > 1) {
      currentPage.value--
      loadData()
    }
  }

  function nextPage() {
    if (tableData.value && currentPage.value < tableData.value.total_pages) {
      currentPage.value++
      loadData()
    }
  }

  function dropTable(name: string) {
    pendingDropTable.value = name
    showConfirmDrop.value = true
  }

  async function confirmDropTable() {
    const name = pendingDropTable.value
    showConfirmDrop.value = false
    pendingDropTable.value = null
    if (!name) return
    try {
      await App.DropTable(name)
      if (selectedTable.value === name) {
        selectedTable.value = null
        tableData.value = null
        tableInfo.value = null
      }
      await loadTables()
    } catch (e: any) {
      errorMessage.value = 'Failed: ' + errorText(e)
    }
  }

  // Holds EventsOn's own unsubscribe function: EventsOff('data:changed') would
  // remove DashboardView's listener too.
  let unsubDataChanged: (() => void) | null = null
  onMounted(loadTables)
  onMounted(() => { if (hasWailsRuntime()) unsubDataChanged = EventsOn('data:changed', onDataChanged) })
  onUnmounted(() => { unsubDataChanged?.() })

  watch(errorMessage, (msg) => {
    if (msg) setTimeout(() => { errorMessage.value = '' }, 3000)
  })
  watch(statusMessage, (msg) => {
    if (msg) setTimeout(() => { statusMessage.value = '' }, 4000)
  })

  return {
    isReadOnly, readOnlyReason, caps, catalogReadOnly, locked, lockText, tables, selectedKind, unavailable, selectedTable, tableInfo,
    tableData, loading, dataLoading, currentPage, sortCol, sortDir, errorMessage, loadError, statusMessage, showConfirmDrop,
    pendingDropTable, formattedRows, activeFilter, filteredTotal, showFbF, fbfCombinator, fbfConditions, colType,
    onSelect, loadTables, loadData, clearFilter, filterBySelection, openFbF, addFbFCondition, applyFbF,
    saveFilterAsQuery, toggleSort, prevPage, nextPage, dropTable, confirmDropTable,
  }
}

export type TableBrowser = ReturnType<typeof useTableBrowser>
