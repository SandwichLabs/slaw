/**
 * useTableCrud — creating and changing data in the table browser (F10): the
 * New table and Add row dialogs, delete row, and the inline cell editor with its
 * optimistic update and per-cell validation feedback. It works on the table
 * useTableBrowser holds.
 */
import { ref, triggerRef, onMounted, onUnmounted } from 'vue'
import { App } from '../lib/bindings'
import { errorText } from '../lib/errors'
import { cellEditText } from '../lib/formatCell'
import type { TableBrowser } from './useTableBrowser'

const blankColumn = (): ColumnInfo => ({ name: '', type: 'TEXT', not_null: false, pk: false })

export function useTableCrud(b: TableBrowser) {
  const showNewTableModal = ref(false)
  const showAddRowModal = ref(false)
  const newTableName = ref('')
  const newTableCols = ref<ColumnInfo[]>([blankColumn()])
  const newRowValues = ref<Record<string, string>>({})
  const showConfirmDeleteRow = ref(false)
  const pendingDeleteRow = ref<any[] | null>(null)

  // Inline edit-in-place state. editing identifies the cell currently being
  // edited by row index + column index; editValue holds the in-flight string.
  const editing = ref<{ row: number; col: number } | null>(null)
  const editValue = ref('')
  const savingCell = ref(false)

  // Cell-level validation feedback: which cell failed and the message to show.
  // Independent of the global error toast so the user sees WHICH cell rejected
  // without the change being persisted. Keyed by table + primary key + column name
  // (#91 M-F16), not row/col index: a row index is only stable within one page of
  // one table's current sort.
  const cellError = ref<{ table: string; pk: string; col: string; message: string } | null>(null)
  b.onSelect(() => { cellError.value = null })

  // pkColumn returns the primary-key ColumnInfo for the selected table, if any.
  function pkColumn(): ColumnInfo | undefined {
    return b.tableInfo.value?.columns.find((c) => c.pk)
  }

  // isCellError reports whether the cell at (row, j) of the currently loaded
  // page is the one cellError refers to.
  function isCellError(row: any[], j: number): boolean {
    const td = b.tableData.value
    if (!cellError.value || !b.selectedTable.value || !td) return false
    const pk = pkColumn()
    if (!pk) return false
    const pkIdx = td.columns.indexOf(pk.name)
    if (pkIdx < 0) return false
    return cellError.value.table === b.selectedTable.value &&
      cellError.value.pk === String(row[pkIdx]) &&
      cellError.value.col === td.columns[j]
  }

  // editorType resolves the inline editor to use for a column from its declared
  // SQL type. Only DATE gets the native date input: TIME/TIMESTAMP[TZ] would open
  // empty and silently drop the time on save, so they get a plain text input
  // seeded with cellEditText's DuckDB-faithful text.
  function editorType(colName: string): 'checkbox' | 'date' | 'text' {
    const col = b.tableInfo.value?.columns.find((c) => c.name === colName)
    const t = (col?.type || '').toUpperCase()
    if (t.includes('BOOL')) return 'checkbox'
    if (t === 'DATE') return 'date'
    return 'text'
  }

  // startEdit opens an inline editor for a cell. Editing is only allowed when the
  // table has a primary key (so UpdateRow has something to target) and the cell
  // being edited is not itself the primary-key column (PKs are immutable here).
  function startEdit(rowIdx: number, colIdx: number) {
    const td = b.tableData.value
    if (!td || b.locked.value) return
    // If already editing this exact cell, keep the in-flight value (avoids a
    // single-click resetting an edit the user started with double-click).
    if (editing.value && editing.value.row === rowIdx && editing.value.col === colIdx) return
    const pk = pkColumn()
    if (!pk) {
      b.errorMessage.value = "Can't edit: this table has no primary key"
      return
    }
    if (td.columns[colIdx] === pk.name) return
    // Clear any stale cell error when (re)opening an editor on a different cell.
    if (cellError.value && !isCellError(td.rows[rowIdx], colIdx)) {
      cellError.value = null
    }
    const cell = td.rows[rowIdx][colIdx]
    editValue.value = cellEditText(cell, b.colType(colIdx))
    editing.value = { row: rowIdx, col: colIdx }
  }

  function cancelEdit() {
    editing.value = null
    editValue.value = ''
    cellError.value = null
  }

  async function saveEdit() {
    const cur = editing.value
    const td = b.tableData.value
    const table = b.selectedTable.value
    if (!cur || !table || !td) return
    const pk = pkColumn()
    if (!pk) { cancelEdit(); return }
    const pkIdx = td.columns.indexOf(pk.name)
    if (pkIdx < 0) { cancelEdit(); return }

    const colName = td.columns[cur.col]
    const row = td.rows[cur.row]
    const original = row[cur.col]
    const next = editValue.value
    // No change → just close the editor; avoid a needless round-trip. Compare
    // against cellEditText's rendering, not String(original) — a DECIMAL's
    // original wire number drops trailing zeros under String() (12.5 vs the
    // seeded "12.5000"), which made every DECIMAL edit look "changed".
    if (cellEditText(original, b.colType(cur.col)) === next) { cancelEdit(); return }

    savingCell.value = true
    cellError.value = null
    const colIdx = cur.col
    // Optimistic update: reflect the new value immediately, then persist. The
    // rows are not deeply reactive (E2), so tell the page to re-render.
    row[cur.col] = next === '' ? null : next
    triggerRef(b.tableData)
    editing.value = null
    try {
      await App.UpdateRow(table, pk.name, row[pkIdx], { [colName]: next })
      await b.loadData()
    } catch (e: any) {
      // Backend validation/constraint error: revert the optimistic change so the
      // bad value is NOT persisted, and surface cell-level feedback identifying
      // exactly which cell was rejected (not just a global toast).
      row[colIdx] = original
      triggerRef(b.tableData)
      const msg = e ? errorText(e) : 'Update rejected'
      cellError.value = { table: b.selectedTable.value!, pk: String(row[pkIdx]), col: colName, message: msg }
    } finally {
      savingCell.value = false
    }
  }

  // commitValue is the explicit-value variant used by the checkbox editor, whose
  // change event carries the chosen value directly. The save is deferred to a
  // microtask so the editor element that fired the change event is not torn down
  // synchronously inside its own DOM event dispatch.
  function commitValue(value: string) {
    editValue.value = value
    return Promise.resolve().then(() => saveEdit())
  }

  // Escape clears a standing cell error when no editor is open — useModalKeys
  // (used by the New table / Add row modals) stops propagation at document, so an
  // open modal's Escape never reaches this (#91 M-F16).
  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && !editing.value) cellError.value = null
  }
  onMounted(() => window.addEventListener('keydown', onKeydown))
  onUnmounted(() => window.removeEventListener('keydown', onKeydown))

  async function createTable() {
    const cols = newTableCols.value.filter((c) => c.name.trim())
    if (!newTableName.value.trim() || cols.length === 0) return
    try {
      await App.CreateTable(newTableName.value, cols)
      showNewTableModal.value = false
      newTableName.value = ''
      newTableCols.value = [blankColumn()]
      await b.loadTables()
    } catch (e: any) {
      b.errorMessage.value = 'Failed: ' + errorText(e)
    }
  }

  function addColumnDef() {
    newTableCols.value.push(blankColumn())
  }

  function openAddRow() {
    if (!b.tableInfo.value || b.locked.value) return
    newRowValues.value = {}
    for (const col of b.tableInfo.value.columns) {
      newRowValues.value[col.name] = ''
    }
    showAddRowModal.value = true
  }

  async function insertRow() {
    if (!b.selectedTable.value) return
    const vals: Record<string, any> = {}
    for (const [k, v] of Object.entries(newRowValues.value)) {
      if (v !== '') vals[k] = v
    }
    try {
      await App.InsertRow(b.selectedTable.value, vals)
      showAddRowModal.value = false
      await b.loadData()
    } catch (e: any) {
      b.errorMessage.value = 'Failed: ' + errorText(e)
    }
  }

  function deleteRow(row: any[]) {
    if (!b.selectedTable.value || !b.tableInfo.value) return
    if (!pkColumn()) {
      b.errorMessage.value = "Can't delete: this table has no primary key"
      return
    }
    pendingDeleteRow.value = row
    showConfirmDeleteRow.value = true
  }

  async function confirmDeleteRow() {
    const row = pendingDeleteRow.value
    showConfirmDeleteRow.value = false
    pendingDeleteRow.value = null
    if (!row || !b.selectedTable.value || !b.tableInfo.value) return
    const pkCol = pkColumn()
    if (!pkCol) return
    const pkIdx = b.tableData.value?.columns.indexOf(pkCol.name) ?? -1
    if (pkIdx < 0) return
    try {
      await App.DeleteRow(b.selectedTable.value, pkCol.name, row[pkIdx])
      await b.loadData()
    } catch (e: any) {
      b.errorMessage.value = 'Failed: ' + errorText(e)
    }
  }

  return {
    showNewTableModal, showAddRowModal, newTableName, newTableCols, newRowValues, showConfirmDeleteRow,
    pendingDeleteRow, editing, editValue, savingCell, cellError,
    isCellError, editorType, startEdit, cancelEdit, saveEdit, commitValue,
    createTable, addColumnDef, openAddRow, insertRow, deleteRow, confirmDeleteRow,
  }
}
