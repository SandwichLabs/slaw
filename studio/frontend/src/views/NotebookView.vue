<script setup lang="ts">
// NotebookView — the notebook editor (#66 part A): cells rendered in order, each
// an sql / markdown / a2ui cell (NotebookCell). SQL cells synthesize a
// one-component A2UI page from their result and render it through A2UIPage — the
// SAME renderer #67's agent-authored dashboards use. Everything it does is
// useNotebookEditor (load, autosave, conflict, agent-append sync).
import Icon from '../components/Icon.vue'
import NotebookCell from '../components/NotebookCell.vue'
import AiCellDialog from '../components/AiCellDialog.vue'
import ConflictBanner from '../components/ConflictBanner.vue'
import { useNotebookEditor } from '../composables/useNotebookEditor'
import { AI_CELL_ENABLED } from '../lib/notebookCells'

const {
  nb, save, sync, cells, title, loading, notFound, errorMessage, infoMessage, statusMessage,
  loadedNotebookIdForView, backToList, exportNotebook, onAiCellApplied, handleWorkspaceClosed, reloadFromDisk,
} = useNotebookEditor()
const { saveState, conflict, conflictBusy } = save
const openerButton = () => document.querySelector<HTMLElement>('[data-testid=ai-cell-open-create]')

// Exposed for component tests (NotebookView.spec.ts) to drive cell state
// directly rather than reach through the CodeMirror mock for every interaction.
defineExpose({
  cells, title, runCell: nb.runCell, moveCell: nb.moveCell, deleteCell: nb.deleteCell, addCell: nb.addCell,
  flushSave: save.flushSave, exportNotebook,
  toggleSource: nb.toggleSource, toggleCollapsed: nb.toggleCollapsed,
  insertCellFromEnvelope: nb.insertCellFromEnvelope, replaceCellFromEnvelope: nb.replaceCellFromEnvelope,
  getLoadedNotebookId: () => save.id(),
  getLoadedSessionId: () => save.sessionId(),
  conflict, resolveConflict: save.resolveConflict, handleChangedOnDisk: sync.handleChangedOnDisk, reloadFromDisk,
  handleNotebooksChanged: sync.handleNotebooksChanged,
  handleWorkspaceClosed,
  isLoading: () => loading.value,
})
</script>

<template>
  <div class="col" style="height: 100%; gap: 0;">
    <div class="row" style="justify-content: space-between; align-items: center; padding: var(--sh-space-3) var(--sh-space-6); border-bottom: var(--sh-border);">
      <div class="row" style="gap: var(--sh-space-3); align-items: center; min-width: 0;">
        <button @click="backToList" class="btn btn-ghost btn-sm" aria-label="Back to notebooks" title="Back to notebooks"><Icon name="arrowLeft" :size="14" /></button>
        <input
          v-model="title"
          class="field-input"
          autocomplete="off"
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
          data-testid="notebook-title"
          style="font-weight: 700; font-size: var(--sh-fs-lg); border: none; background: transparent; min-width: 240px;"
          placeholder="Untitled notebook"
        />
        <span class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite);" data-testid="save-indicator">
          <template v-if="saveState === 'saving'">Saving…</template>
          <template v-else-if="saveState === 'saved'">Saved</template>
        </span>
      </div>
      <div class="row" style="gap: var(--sh-space-2);">
        <button @click="exportNotebook" class="btn btn-ghost btn-sm" data-testid="export-notebook"><Icon name="download" :size="14" /> Export</button>
        <button @click="nb.runAll" class="btn btn-ghost btn-sm"><Icon name="play" :size="14" /> Run all</button>
      </div>
    </div>

    <ConflictBanner
      v-if="conflict"
      kind="notebook"
      :path="`notebooks/${loadedNotebookIdForView}.slnb`"
      :deleted="conflict.deleted"
      :busy="conflictBusy"
      @reload="save.resolveConflict('reload')"
      @overwrite="save.resolveConflict('overwrite')"
      @save-copy="save.resolveConflict('saveCopy')"
    />

    <div style="flex: 1; overflow: auto; padding: var(--sh-space-6);">
      <div v-if="loading" class="sh-mono" style="color: var(--sh-graphite);">Loading…</div>
      <div v-else-if="notFound" class="sh-mono" style="color: var(--sh-error);">Notebook not found.</div>

      <div v-else class="col" style="gap: var(--sh-space-4); max-width: 920px; margin: 0 auto;">
        <div v-if="cells.length === 0" class="col" style="align-items: center; gap: var(--sh-space-3); margin-top: var(--sh-space-8); text-align: center;">
          <Icon name="layers" :size="28" style="color: var(--sh-graphite);" />
          <div class="sh-mono" style="color: var(--sh-graphite);">This notebook is empty. Add a cell to get started.</div>
        </div>

        <NotebookCell
          v-for="cell in cells"
          :key="cell.id"
          :cell="cell"
          :notebook-id="loadedNotebookIdForView"
          :signal="nb.signal()"
          :observe="nb.observeCell"
          @collapse="nb.toggleCollapsed(cell)"
          @run="nb.runCell(cell)"
          @toggle-md="nb.toggleMdPreview(cell)"
          @toggle-source="nb.toggleSource(cell)"
          @ai-edit="(t) => nb.openAiCellEdit(cell, t)"
          @move="(d) => nb.moveCell(cell, d)"
          @remove="nb.deleteCell(cell)"
        />

        <!-- Add cell toolbar -->
        <div class="row" style="gap: var(--sh-space-2); justify-content: center; padding: var(--sh-space-4);">
          <button @click="nb.addCell('sql')" class="btn btn-ghost btn-sm" data-testid="add-sql-cell"><Icon name="terminal" :size="14" /> SQL</button>
          <button @click="nb.addCell('markdown')" class="btn btn-ghost btn-sm" data-testid="add-markdown-cell"><Icon name="fileText" :size="14" /> Markdown</button>
          <button @click="nb.addCell('a2ui')" class="btn btn-ghost btn-sm" data-testid="add-a2ui-cell"><Icon name="barChart" :size="14" /> A2UI</button>
          <button v-if="AI_CELL_ENABLED" @click="nb.aiCellDialog.openCreate()" class="btn btn-ghost btn-sm" data-testid="ai-cell-open-create"><Icon name="sparkle" :size="14" /> Generate with AI</button>
          <button
            v-else
            class="btn btn-ghost btn-sm"
            disabled
            style="opacity: 0.4; cursor: not-allowed;"
            title="Assistant cell authoring lands in a later step (#66 part B)"
          ><Icon name="sparkle" :size="14" /> Generate with AI</button>
        </div>
      </div>
    </div>

    <div v-if="errorMessage" class="error-toast" @click="errorMessage = ''">{{ errorMessage }}</div>
    <div v-if="infoMessage" class="info-toast" data-testid="ai-cell-info-toast" @click="infoMessage = ''">{{ infoMessage }}</div>
    <div v-if="statusMessage" class="status-toast" data-testid="notebook-status" @click="statusMessage = ''">{{ statusMessage }}</div>

    <AiCellDialog v-if="AI_CELL_ENABLED" :dialog="nb.aiCellDialog" :fallback-focus="openerButton" @applied="onAiCellApplied" />
  </div>
</template>
