<script setup lang="ts">
// TableBrowserView — the datasheet: every table across the workspace's
// connections, paged and sortable, filtered by selection or by form, edited in
// place. The catalog / selection / filter state lives in useTableBrowser and the
// create / insert / edit / delete actions in useTableCrud; this view renders them.
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import Icon from '../components/Icon.vue'
import { useModalKeys } from '../composables/useModal'
import { useTableBrowser, FILTER_OPS } from '../composables/useTableBrowser'
import { useTableCrud } from '../composables/useTableCrud'
import { cellClass, isNumericType } from '../lib/formatCell'

const router = useRouter()
const browser = useTableBrowser()
const {
  isReadOnly, readOnlyReason, caps, catalogReadOnly, locked, lockText, tables, selectedKind, unavailable, selectedTable, tableInfo,
  tableData, loading, dataLoading, currentPage, sortCol, sortDir, errorMessage, loadError, statusMessage, showConfirmDrop,
  pendingDropTable, formattedRows, activeFilter, filteredTotal, showFbF, fbfCombinator, fbfConditions, colType,
  loadData, clearFilter, filterBySelection, openFbF, addFbFCondition, applyFbF, saveFilterAsQuery, toggleSort,
  prevPage, nextPage, dropTable, confirmDropTable,
} = browser
const {
  showNewTableModal, showAddRowModal, newTableName, newTableCols, newRowValues, showConfirmDeleteRow,
  pendingDeleteRow, editing, editValue, savingCell, cellError,
  isCellError, editorType, startEdit, cancelEdit, saveEdit, commitValue,
  createTable, addColumnDef, openAddRow, insertRow, deleteRow, confirmDeleteRow,
} = useTableCrud(browser)

const newTableEl = ref<HTMLElement | null>(null)
const addRowEl = ref<HTMLElement | null>(null)
useModalKeys(showNewTableModal, () => { showNewTableModal.value = false }, newTableEl)
useModalKeys(showAddRowModal, () => { showAddRowModal.value = false }, addRowEl)

// Autofocus + select directive for the inline cell editor. select() only exists
// on text inputs, so guard it for checkbox/date/select editors.
const vFocus = {
  mounted: (el: HTMLInputElement | HTMLSelectElement) => {
    el.focus()
    if (typeof (el as HTMLInputElement).select === 'function' &&
        (el as HTMLInputElement).type !== 'checkbox') {
      try { (el as HTMLInputElement).select() } catch { /* not selectable */ }
    }
  },
}
</script>

<template>
  <div class="flex h-full">
    <!-- Data area -->
    <div class="flex-1 overflow-auto flex flex-col">
      <div v-if="!selectedTable && tables.length === 0 && !loading && unavailable.length" class="flex-1 flex flex-col items-center justify-center gap-4" data-testid="tables-unavailable">
        <div class="empty-hero">
          <div class="glyph"><Icon name="lock" :size="48" /></div>
          <h2>Can't list these tables yet</h2>
          <p class="lead">{{ unavailable[0].connection }} is {{ unavailable[0].status === 'locked-by' ? 'in use by another program. Its tables can be read once it lets go.' : 'not available right now.' }}</p>
          <details v-if="unavailable[0].detail && unavailable[0].status !== 'locked-by'" class="help" style="max-width: 560px;">
            <summary style="cursor: pointer; font-weight: 600;">Show details</summary>
            <div class="details" style="margin-top: 8px; overflow-wrap: anywhere;">{{ unavailable[0].detail }}</div>
          </details>
        </div>
      </div>
      <div v-else-if="!selectedTable && tables.length === 0 && !loading" class="flex-1 flex flex-col items-center justify-center gap-4">
        <div class="empty-hero">
          <div class="glyph"><Icon name="database" :size="48" /></div>
          <h2>No tables yet</h2>
          <p class="lead">Create a table or import a CSV.</p>
          <button @click="showNewTableModal = true" class="btn btn-primary" :disabled="isReadOnly" :title="isReadOnly ? readOnlyReason : undefined"><Icon name="plus" :size="16" /> Create table</button>
        </div>
      </div>
      <div v-else-if="!selectedTable" class="flex-1 flex flex-col items-center justify-center gap-3 text-[var(--sh-graphite)]" data-testid="pick-from-rail">
        <span class="sh-mono">Choose a table in the Connections list on the left.</span>
        <button @click="showNewTableModal = true" class="btn btn-sm" :disabled="isReadOnly" :title="isReadOnly ? readOnlyReason : 'Create a table'" data-testid="new-table"><Icon name="plus" :size="14" /> New table</button>
      </div>
      <template v-else>
        <!-- Toolbar -->
        <div class="page-subhead" style="padding: 14px 24px;">
          <h1 style="font-size: 22px;">{{ selectedTable }}</h1>
          <span class="flex-1"></span>
          <button @click="openFbF" class="btn btn-sm"><Icon name="filter" :size="14" /> Filter by form</button>
          <button v-if="activeFilter" @click="saveFilterAsQuery" class="btn btn-sm"><Icon name="save" :size="14" /> Save as query</button>
          <button v-if="activeFilter" @click="(clearFilter(), loadData())" class="btn btn-ghost btn-sm"><Icon name="x" :size="14" /> Clear filter</button>
          <button v-if="!locked && selectedKind === 'table'" @click="dropTable(selectedTable!)" class="btn btn-ghost btn-sm" style="color: var(--sh-error);" data-testid="drop-table"><Icon name="trash" :size="14" /> Drop table</button>
          <span v-if="locked && caps" class="badge" data-testid="readonly-badge"><Icon name="lock" :size="11" /> Read-only</span>
          <button @click="openAddRow" class="btn btn-primary btn-sm" :disabled="locked" :title="locked ? lockText : 'Add a row'" data-testid="add-row"><Icon name="plus" :size="14" /> Add row</button>
        </div>
        <!-- Why this table can't be edited (SPEC 07 E). -->
        <div v-if="caps && !caps.editable" class="note attn" role="status" data-testid="readonly-reason" style="margin: 12px 24px 0;">
          <Icon name="lock" :size="16" />
          <div>
            <b>This table is read-only.</b>{{ ' ' }}<span>{{ caps.text }}</span>
            <span v-if="caps.reason === 'connection-read-only' && catalogReadOnly" class="help" style="display: block;" data-testid="readonly-catalog-help">This lake's catalog is in object storage, so it can only be read.</span>
            <span v-else-if="caps.reason === 'connection-read-only'" class="help" style="display: block;">To change data, turn off Read-only for the connection. For production data, use a database user that is allowed to write.</span>
          </div>
          <button v-if="caps.reason === 'connection-read-only' && !catalogReadOnly" class="btn btn-sm" style="margin-left: auto;" @click="router.push(`/connections/${caps.connection}`)">Edit connection</button>
        </div>

        <!-- Record-count bar -->
        <div class="record-count-bar px-6 py-1.5 border-b border-line sh-mono text-xs text-[var(--sh-graphite)] flex items-center gap-2">
          <span v-if="activeFilter" class="badge badge--teal">Filtered</span>
          <span>{{ (activeFilter ? (filteredTotal ?? 0) : (tableData?.total_rows ?? 0)) }} records</span>
        </div>

        <!-- Filter-by-Form panel -->
        <div v-if="showFbF" class="px-6 py-3 border-b-2 border-[var(--sh-ink)] bg-[var(--sh-paper-2)]">
          <div class="flex items-center gap-2 mb-2">
            <span class="sh-label" style="margin:0;">Filter by form</span>
            <select v-model="fbfCombinator" class="field-input fbf-combinator w-24">
              <option value="AND">AND</option>
              <option value="OR">OR</option>
            </select>
          </div>
          <div v-for="(cond, i) in fbfConditions" :key="i" class="flex gap-2 mb-2">
            <select v-model="cond.column" class="field-input fbf-column flex-1">
              <option v-for="c in (tableInfo?.columns || [])" :key="c.name" :value="c.name">{{ c.name }}</option>
            </select>
            <select v-model="cond.op" class="field-input fbf-op w-28">
              <option v-for="op in FILTER_OPS" :key="op.value" :value="op.value">{{ op.label }}</option>
            </select>
            <input
              v-model="cond.value"
              class="field-input fbf-value flex-1"
              autocomplete="off"
              autocapitalize="off"
              autocorrect="off"
              spellcheck="false"
              :disabled="cond.op === 'is_null' || cond.op === 'is_not_null'"
              placeholder="value"
            />
          </div>
          <div class="flex gap-2 justify-end">
            <button @click="addFbFCondition" class="btn btn-ghost btn-sm"><Icon name="plus" :size="14" /> Condition</button>
            <button @click="showFbF = false" class="btn btn-ghost btn-sm">Cancel</button>
            <button @click="applyFbF" class="btn btn-primary btn-sm">Apply</button>
          </div>
        </div>

        <!-- Table -->
        <div v-if="dataLoading" class="p-4 sh-mono text-[var(--sh-graphite)]">Loading…</div>
        <div v-else-if="loadError" class="note attn" role="alert" style="margin: 16px;" data-testid="table-load-error"><Icon name="alert" :size="16" /><span>{{ loadError }}</span></div>
        <div v-else-if="tableData" class="flex-1 overflow-auto">
          <table class="brutal-table">
            <thead class="sticky top-0">
              <tr>
                <th
                  v-for="(col, ci) in tableData.columns"
                  :key="col"
                  :class="{ 'cell-num': isNumericType(colType(ci)) }"
                  @click="toggleSort(col)"
                >
                  {{ col }}
                  <span v-if="sortCol === col">{{ sortDir === 'ASC' ? ' ↑' : ' ↓' }}</span>
                </th>
                <th class="w-16"></th>
              </tr>
            </thead>
            <tbody>
              <template v-if="tableData.rows && tableData.rows.length > 0">
                <tr v-for="(row, i) in tableData.rows" :key="i">
                  <td
                    v-for="(cell, j) in row"
                    :key="j"
                    :aria-readonly="locked ? 'true' : undefined"
                    @click="startEdit(i, j)"
                    @dblclick="startEdit(i, j)"
                    :class="[
                      editorType(tableData.columns[j]) === 'checkbox' ? [] : cellClass(formattedRows[i][j], colType(j)),
                      {
                        'cell-editing': editing && editing.row === i && editing.col === j,
                        'cell-error': isCellError(row, j),
                      },
                    ]"
                    :title="editorType(tableData.columns[j]) === 'checkbox' ? undefined : formattedRows[i][j].title"
                  >
                    <template v-if="editing && editing.row === i && editing.col === j">
                      <!-- Boolean checkbox editor. -->
                      <input
                        v-if="editorType(tableData.columns[j]) === 'checkbox'"
                        type="checkbox"
                        class="cell-checkbox"
                        :checked="editValue === 'true' || editValue === '1'"
                        :disabled="savingCell"
                        @change="commitValue(($event.target as HTMLInputElement).checked ? 'true' : 'false')"
                        @keydown.esc.prevent="cancelEdit"
                        v-focus
                      />
                      <!-- Date editor. -->
                      <input
                        v-else-if="editorType(tableData.columns[j]) === 'date'"
                        type="date"
                        v-model="editValue"
                        class="cell-input"
                        :disabled="savingCell"
                        @keydown.enter.prevent="saveEdit"
                        @keydown.esc.prevent="cancelEdit"
                        @blur="saveEdit"
                        v-focus
                      />
                      <!-- Default free-text editor. -->
                      <input
                        v-else
                        v-model="editValue"
                        class="cell-input"
                        autocomplete="off"
                        autocapitalize="off"
                        autocorrect="off"
                        spellcheck="false"
                        :disabled="savingCell"
                        @keydown.enter.prevent="saveEdit"
                        @keydown.esc.prevent="cancelEdit"
                        @blur="saveEdit"
                        v-focus
                      />
                    </template>
                    <template v-else>
                      <input
                        v-if="editorType(tableData.columns[j]) === 'checkbox'"
                        type="checkbox"
                        :checked="cell === true || cell === 'true' || cell === 1 || cell === '1'"
                        disabled
                        class="cell-checkbox"
                      />
                      <span v-else>{{ formattedRows[i][j].text }}</span>
                      <!-- Filter-by-Selection affordance: a funnel that filters
                           the datasheet to rows where this column equals this
                           cell's value (Access right-click "Filter by Selection").
                           Passes the raw wire value (filterBySelection needs the
                           exact value to match against, not its display text);
                           the title uses the faithfully formatted text. -->
                      <button
                        class="filter-selection-btn"
                        data-filter-selection
                        :title="`Filter by ${tableData.columns[j]} = ${formattedRows[i][j].text}`"
                        @click.stop="filterBySelection(tableData.columns[j], cell)"
                      ><Icon name="filter" :size="11" /></button>
                    </template>
                    <div
                      v-if="isCellError(row, j)"
                      class="cell-error-msg"
                      :title="cellError!.message"
                    >{{ cellError!.message.split('\n')[0] }}</div>
                  </td>
                  <td>
                    <button v-if="!locked" @click="deleteRow(row)" class="btn btn-ghost btn-sm" style="color: var(--sh-error);" aria-label="Delete row" title="Delete row"><Icon name="trash" :size="14" /></button>
                  </td>
                </tr>
              </template>
              <tr v-else>
                <td :colspan="tableData.columns.length + 1" class="text-center text-[var(--sh-graphite)] sh-mono py-8">No rows</td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Pagination -->
        <div v-if="tableData && tableData.total_pages > 1" class="px-4 py-2 border-t-2 border-[var(--sh-ink)] bg-[var(--sh-paper-2)] flex items-center justify-between text-sm">
          <button @click="prevPage" :disabled="currentPage <= 1" class="btn btn-sm"><Icon name="arrowLeft" :size="14" /> Prev</button>
          <span class="sh-mono text-xs text-[var(--sh-graphite)]">Page {{ currentPage }} of {{ tableData.total_pages }}</span>
          <button @click="nextPage" :disabled="currentPage >= tableData.total_pages" class="btn btn-sm">Next <Icon name="arrowRight" :size="14" /></button>
        </div>
      </template>
    </div>

    <!-- New table modal -->
    <div v-if="showNewTableModal" class="modal-overlay" @click.self="showNewTableModal = false" role="dialog" aria-modal="true" aria-label="Create table">
      <div ref="newTableEl" class="card card-pad w-full max-w-lg">
        <h3 class="card-title mb-4">Create table</h3>
        <input v-model="newTableName" placeholder="Table name" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" class="field-input mb-4" />
        <div class="space-y-2 mb-4">
          <div v-for="(col, i) in newTableCols" :key="i" class="flex gap-2">
            <input v-model="col.name" placeholder="Column name" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" class="field-input flex-1" />
            <select v-model="col.type" class="field-input w-28">
              <option>TEXT</option>
              <option>INTEGER</option>
              <option>REAL</option>
              <option>BLOB</option>
            </select>
            <label class="flex items-center gap-1 text-xs font-semibold sh-mono"><input type="checkbox" v-model="col.pk" style="accent-color: var(--sh-teal);" /> PK</label>
          </div>
          <button @click="addColumnDef" class="btn btn-ghost btn-sm"><Icon name="plus" :size="14" /> Add column</button>
        </div>
        <div class="flex gap-3 justify-end">
          <button @click="showNewTableModal = false" class="btn btn-ghost">Cancel</button>
          <button @click="createTable" class="btn btn-primary">Create</button>
        </div>
      </div>
    </div>

    <!-- Add row modal -->
    <div v-if="showAddRowModal && tableInfo" class="modal-overlay" @click.self="showAddRowModal = false" role="dialog" aria-modal="true" aria-label="Add row">
      <div ref="addRowEl" class="card card-pad w-full max-w-lg max-h-[80vh] overflow-auto">
        <h3 class="card-title mb-4">Add row to {{ selectedTable }}</h3>
        <div class="space-y-3 mb-4">
          <div v-for="col in tableInfo.columns" :key="col.name">
            <label class="field-label">{{ col.name }} <span class="text-[var(--sh-graphite)]">({{ col.type }})</span></label>
            <input v-model="newRowValues[col.name]" class="field-input" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" />
          </div>
        </div>
        <div class="flex gap-3 justify-end">
          <button @click="showAddRowModal = false" class="btn btn-ghost">Cancel</button>
          <button @click="insertRow" class="btn btn-primary">Insert</button>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :visible="showConfirmDrop"
      title="Drop table"
      confirm-label="Drop table"
      :message="`Drop table '${pendingDropTable}'? This cannot be undone.`"
      @confirm="confirmDropTable"
      @cancel="showConfirmDrop = false; pendingDropTable = null"
    />

    <ConfirmDialog
      :visible="showConfirmDeleteRow"
      title="Delete row"
      confirm-label="Delete row"
      message="Delete this row?"
      @confirm="confirmDeleteRow"
      @cancel="showConfirmDeleteRow = false; pendingDeleteRow = null"
    />

    <div v-if="errorMessage" class="error-toast">
      {{ errorMessage }}
    </div>
    <div v-if="statusMessage" class="status-toast" data-testid="table-status">{{ statusMessage }}</div>
  </div>
</template>

<style scoped>
.brutal-table td {
  cursor: text;
}
.brutal-table td.cell-editing {
  padding: 0;
}
.cell-input {
  width: 100%;
  box-sizing: border-box;
  border: 2px solid var(--sh-teal);
  background: var(--sh-paper);
  padding: 6px 8px;
  font: inherit;
  color: var(--sh-ink);
}
.cell-checkbox {
  accent-color: var(--sh-teal);
}
/* Filter-by-Selection affordance: an inline funnel button that only reveals
   itself on cell hover, so the datasheet stays clean until the user reaches
   for it. The cell text is rendered by the adjacent span; this button repeats
   the value purely so the funnel sits next to the value it filters on. */
.filter-selection-btn {
  display: none;
  align-items: center;
  gap: 3px;
  margin-left: 6px;
  padding: 0 4px;
  border: 1px solid var(--sh-teal);
  background: var(--sh-paper);
  color: var(--sh-teal);
  font: inherit;
  font-size: 11px;
  cursor: pointer;
  border-radius: 2px;
}
.brutal-table td:hover .filter-selection-btn {
  display: inline-flex;
}
.brutal-table td.cell-error {
  outline: 2px solid var(--sh-error);
  outline-offset: -2px;
  background: var(--sh-error-8);
}
.cell-error-msg {
  font-size: 11px;
  color: var(--sh-error);
  margin-top: 2px;
  font-family: var(--sh-font-mono);
}
</style>
