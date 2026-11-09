<script setup lang="ts">
// QueryEditorView — the Query Explorer (parity W5; mockup #8 hifi-power).
//
// Left rail: the saved-query list (the schema tree moved to the global
// Connections rail, #117). Right: the SQL editor over a result bar carrying run-status metadata
// (success badge, row count, duration, DuckDB engine label, last-run time)
// and the result grid. The subhead exposes Save, CSV/Parquet export (DuckDB
// COPY via ExportQueryResults), an Ask-agent jump (opens the W0 panel), and
// Run. All state and actions live in useQueryEditor.
import { ref, watch, nextTick } from 'vue'
import ConnectionSelect from '../components/ConnectionSelect.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import Icon from '../components/Icon.vue'
import SqlEditor from '../components/SqlEditor.vue'
import ReplTerminal from '../components/ReplTerminal.vue'
import ConflictBanner from '../components/ConflictBanner.vue'
import QueryParams from '../components/QueryParams.vue'
import { useModalKeys, shortcutLabel } from '../composables/useModal'
import { useQueryEditor } from '../composables/useQueryEditor'
import { cellClass, isNumericType } from '../lib/formatCell'

const {
  qp, queries, selectedId, sqlText, result, loading, running, cancelled, error, showSaveModal, saveTitle, errorMessage,
  statusMessage, showConfirmDelete, pendingDeleteId, hoveredId, conflict, conflictBusy,
  lastRunAt, lastRunOk, ranOn, conn, dirty, currentTitle, truncated, formattedRows,
  selectQuery, runQuery, showEvidence, stopQuery, saveQuery, resolveConflict, exportResults, askAgent, deleteQuery,
  confirmDeleteQuery, newQuery,
} = useQueryEditor()

// Stop disappears when the run ends; hand its keyboard focus to Run so it is not lost.
const runBtn = ref<HTMLButtonElement | null>(null)
let stopHadFocus = false
function onStop() { stopHadFocus = true; stopQuery() }
watch(running, async (r) => {
  if (r || !stopHadFocus) return
  stopHadFocus = false
  await nextTick()
  runBtn.value?.focus()
})

// The output area has two tabs (docs/reference/console.md): the result Grid and
// the Console (a terminal on the same connection). The console keeps its
// session while the Grid is showing.
const outputTab = ref<'grid' | 'console'>('grid')

const saveModalEl = ref<HTMLElement | null>(null)
useModalKeys(showSaveModal, () => { showSaveModal.value = false }, saveModalEl)
</script>

<template>
  <div class="col" style="height: 100%; gap: 0;">
    <!-- Subhead: title, dirty badge, save/export/agent/run actions -->
    <div class="page-subhead" style="padding: 12px 24px;">
      <div>
        <div class="crumb">Studio <span class="sep">/</span> Query Explorer</div>
        <div class="row" style="gap: 10px; margin-top: 4px;">
          <h1 style="font-size: 22px; margin: 0; font-family: var(--sh-font-mono);">{{ currentTitle }}</h1>
          <span v-if="dirty" class="badge badge--warn" data-testid="query-dirty">Unsaved</span>
        </div>
      </div>
      <div class="subhead-actions">
      <ConnectionSelect v-model="conn" />
      <button @click="saveTitle = saveTitle || currentTitle.replace('.sql',''), showSaveModal = true" class="btn btn-sm">
        <Icon name="save" :size="14" /> Save query
      </button>
      <button
        class="btn btn-sm"
        data-testid="export-csv"
        :disabled="!sqlText.trim()"
        @click="exportResults('csv')"
      ><Icon name="download" :size="14" /> CSV</button>
      <button
        class="btn btn-sm"
        data-testid="export-parquet"
        :disabled="!sqlText.trim()"
        @click="exportResults('parquet')"
      ><Icon name="download" :size="14" /> Parquet</button>
      <button class="btn btn-ghost btn-sm" data-testid="ask-agent" @click="askAgent">
        <Icon name="sparkle" :size="14" /> Ask assistant
      </button>
      <button v-if="running" type="button" class="btn btn-sm" data-testid="stop-query" aria-label="Stop the running query" @click="onStop">
        <Icon name="x" :size="14" /> Stop
      </button>
      <button ref="runBtn" @click="runQuery" :disabled="running" class="btn btn-primary btn-sm" data-testid="run-query">
        <Icon name="play" :size="14" /> {{ running ? 'Running…' : `Run · ${shortcutLabel('Enter')}` }}
      </button>
      </div>
    </div>

    <ConflictBanner
      v-if="conflict"
      kind="query"
      :path="`queries/${selectedId}.sql`"
      :deleted="conflict.deleted"
      :busy="conflictBusy"
      @reload="resolveConflict('reload')"
      @overwrite="resolveConflict('overwrite')"
      @save-copy="resolveConflict('saveCopy')"
    />

    <QueryParams :qp="qp" />

    <div class="row" style="flex: 1; gap: 0; align-items: stretch; min-height: 0;">
      <!-- LEFT RAIL — saved queries (the schema tree is the global Connections rail) -->
      <div class="col rail">
        <div class="row" style="justify-content: space-between; align-items: center;">
          <div class="rail-label" style="margin: 0;">Saved queries</div>
          <button @click="newQuery" class="btn btn-ghost btn-sm" title="New query"><Icon name="plus" :size="14" /></button>
        </div>
        <div style="flex: 1; overflow: auto; min-height: 0;">
          <div v-if="loading" class="sh-mono rail-empty">Loading…</div>
          <div v-else-if="queries.length === 0" class="sh-mono rail-empty">No saved queries</div>
          <div
            v-for="q in queries"
            :key="q.id"
            data-testid="saved-query"
            @click="selectQuery(q.id)"
            @mouseenter="hoveredId = q.id"
            @mouseleave="hoveredId = null"
            class="row saved-row"
            :class="{ on: selectedId === q.id }"
          >
            <div style="min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600;">{{ q.title }}</div>
            <button :class="{ 'is-on': hoveredId === q.id }" @click.stop="deleteQuery(q.id)" class="btn btn-ghost btn-sm hover-reveal" style="color: var(--sh-error);" :aria-label="`Delete query ${q.title}`" :title="`Delete ${q.title}`"><Icon name="trash" :size="14" /></button>
          </div>
        </div>
      </div>

      <!-- EDITOR + RESULTS -->
      <div class="col" style="flex: 1; gap: 0; min-width: 0;">
        <div class="editor" style="border-bottom: var(--sh-border);">
          <SqlEditor v-model="sqlText" :connection="conn" placeholder="Write a SQL query…" @run="runQuery" />
        </div>

        <div class="output-tabs" role="tablist" aria-label="Output">
          <button
            role="tab" class="output-tab" data-testid="output-tab-grid"
            :class="{ on: outputTab === 'grid' }" :aria-selected="outputTab === 'grid'"
            @click="outputTab = 'grid'"
          >Grid</button>
          <button
            role="tab" class="output-tab" data-testid="output-tab-console"
            :class="{ on: outputTab === 'console' }" :aria-selected="outputTab === 'console'"
            @click="outputTab = 'console'"
          ><Icon name="terminal" :size="12" /> Console</button>
        </div>

        <div v-show="outputTab === 'console'" class="col" style="flex: 1; min-height: 0;" role="tabpanel">
          <ReplTerminal :connection="conn" :active="outputTab === 'console'" />
        </div>

        <div v-show="outputTab === 'grid'" class="col" style="flex: 1; gap: 0; min-height: 0;" role="tabpanel">
        <!-- Run-status metadata bar (W5) -->
        <div v-if="lastRunOk !== null" class="result-bar" data-testid="run-meta">
          <span v-if="lastRunOk" class="badge badge--success" data-testid="run-badge"><span class="dot"></span> Complete</span>
          <span v-else class="badge badge--error" data-testid="run-badge"><span class="dot"></span> Error</span>
          <span v-if="result"><span class="big ok">{{ result.total_rows.toLocaleString() }}</span> rows</span>
          <span v-if="result"><span class="big">{{ result.duration }}</span></span>
          <span v-if="result && (result.receipt_id || result.receipt_note)"><button type="button" class="ev-link" data-testid="query-evidence" aria-label="Show evidence for this result" @click="showEvidence">Evidence</button></span>
          <span data-testid="run-engine"><Icon name="zap" :size="11" style="color: var(--sh-success); vertical-align: middle;" /> DuckDB<template v-if="ranOn"> · {{ ranOn }}</template></span>
          <span class="spacer"></span>
          <span data-testid="run-last">Last run · {{ lastRunAt }}</span>
        </div>

        <div style="flex: 1; overflow: auto; min-height: 0;">
          <div v-if="error" class="sql-error" style="padding: 16px; font-size: 13px; font-weight: 600; color: var(--sh-error);">{{ error }}</div>
          <div v-else-if="running" class="sh-mono" style="padding: 16px; color: var(--sh-graphite);">Running query…</div>
          <div v-else-if="cancelled" class="sh-mono" style="padding: 16px; color: var(--sh-graphite);" role="status" data-testid="query-cancelled">Query cancelled.</div>
          <div v-else-if="result" style="overflow: auto; height: 100%;">
            <div v-if="truncated" class="result-bar" data-testid="query-row-cap">
              Showing the first {{ result.row_count.toLocaleString() }} of {{ result.total_rows.toLocaleString() }} rows. Export CSV or Parquet to get all of them.
            </div>
            <table class="table">
              <thead>
                <tr>
                  <th
                    v-for="col in result.columns"
                    :key="col.name"
                    :class="{ 'cell-num': isNumericType(col.type) }"
                  >{{ col.name }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(row, i) in formattedRows" :key="i">
                  <td
                    v-for="(fc, j) in row"
                    :key="j"
                    :class="cellClass(fc, result!.columns[j]?.type)"
                    :title="fc.title"
                  >{{ fc.text }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else class="empty-hero">
            <div class="glyph"><Icon name="terminal" :size="48" /></div>
            <h2>Write a query and press Run</h2>
            <p class="lead">Every query runs on the embedded DuckDB engine, reads and edits alike.</p>
          </div>
        </div>
        </div>
      </div>
    </div>

    <!-- Save modal -->
    <div v-if="showSaveModal" class="modal-overlay" @click.self="showSaveModal = false" role="dialog" aria-modal="true" aria-label="Save query">
      <div ref="saveModalEl" class="card card-pad" style="width: 100%; max-width: 384px;">
        <h3 class="card-title" style="margin-bottom: 16px;">Save query</h3>
        <input v-model="saveTitle" placeholder="Query name" aria-label="Query name" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" class="field-input" style="margin-bottom: 16px;" @keyup.enter="saveQuery" />
        <div class="row" style="gap: 12px; justify-content: flex-end;">
          <button @click="showSaveModal = false" class="btn btn-ghost">Cancel</button>
          <button @click="saveQuery" class="btn btn-primary">Save</button>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :visible="showConfirmDelete"
      title="Delete query"
      confirm-label="Delete"
      message="Delete this query?"
      @confirm="confirmDeleteQuery"
      @cancel="showConfirmDelete = false; pendingDeleteId = null"
    />

    <div v-if="errorMessage" class="error-toast">{{ errorMessage }}</div>
    <div v-if="statusMessage" class="status-toast" data-testid="query-status">{{ statusMessage }}</div>
  </div>
</template>

<style scoped>
.rail {
  width: 240px;
  flex: none;
  gap: 8px;
  border-right: var(--sh-border);
  background: var(--sh-paper-2);
  padding: 14px 12px;
  min-height: 0;
  overflow-y: auto;
}
.rail-label {
  font-family: var(--sh-font-mono);
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--sh-graphite);
}
.rail-empty {
  padding: 8px 10px;
  color: var(--sh-graphite);
  font-size: 12px;
}
/* The shipped .schema-tree .col rows collide with the global `.col`
   (flex-column) utility — force the row direction back. */
.schema-tree :deep(.col) {
  flex-direction: row;
  align-items: center;
}
.rail-divider {
  border-top: 1.5px dashed var(--slaw-border-subtle);
  margin: 10px 0 4px;
}
.saved-row {
  justify-content: space-between;
  padding: 6px 10px;
  cursor: pointer;
  border-radius: var(--sh-radius-sm);
  font-size: 13px;
}
.saved-row:hover { background: var(--sh-hover); }
.saved-row.on {
  background: var(--sh-surface);
  border: 1.5px solid var(--slaw-border);
  box-shadow: var(--sh-shadow-xs);
}
.output-tabs {
  display: flex;
  gap: 4px;
  padding: 4px 12px 0;
  border-bottom: var(--sh-border);
  background: var(--sh-paper-2);
}
.output-tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 12px;
  border: 1.5px solid transparent;
  border-bottom: none;
  border-radius: var(--sh-radius-sm) var(--sh-radius-sm) 0 0;
  background: transparent;
  color: var(--sh-graphite);
  font-family: var(--sh-font-mono);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}
.output-tab:hover { background: var(--sh-hover); }
.output-tab.on {
  background: var(--sh-surface);
  border-color: var(--slaw-border);
  color: inherit;
}
.sql-error {
  white-space: pre-wrap;
  font-family: var(--sh-font-mono);
  font-size: 13px;
}
</style>
