<script setup lang="ts">
// NotebookCell — one cell card of the notebook editor (F10): the toolbar (title,
// label, connection, view buttons, Run, move/delete) and the body for its three
// types. sql cells synthesize a one-component A2UI page from their result and
// render it through A2UIPage, the SAME renderer the agent-authored dashboards
// use. All state lives in the cell object NotebookView owns (the same object
// the save path serializes); this component only renders it and raises intents.
import Icon from './Icon.vue'
import A2UIPage from '../a2ui/A2UIPage.vue'
import CellDrawer from './CellDrawer.vue'
import ConnectionSelect from './ConnectionSelect.vue'
import { defaultConnectionName } from '../composables/useConnections'
import { renderSafeMarkdown } from '../lib/markdown'
import { cellMeta } from '../lib/cellSummary'
import CellMotionPicker from './CellMotionPicker.vue'
import { viewFit, CHART_VIEWS, CLASSIC_VIEWS, VIEW_LABELS, VIEW_HINTS, type ViewKind } from '../lib/notebookViews'
import {
  AI_CELL_ENABLED, CELL_ICON, CELL_KIND, ariaTitleOf, isStale, mismatchInfo, pickView, shapeOf, summaryOf,
  synthesizedPage, titleOf, type CellState,
} from '../lib/notebookCells'

defineProps<{
  cell: CellState
  // Aborts the cell's live queries when the load that rendered it is superseded.
  signal?: AbortSignal
  // Registers the card element with the lazy-mount IntersectionObserver.
  observe: (cell: CellState, el: Element | null) => void
}>()
const emit = defineEmits<{
  collapse: []
  run: []
  toggleMd: []
  toggleSource: []
  aiEdit: [titleOverride?: string]
  move: [delta: number]
  remove: []
}>()
</script>

<template>
  <div
    :ref="(el) => observe(cell, el as Element | null)"
    class="card"
    :data-testid="`cell-${cell.type}`"
    :data-cell-id="cell.id"
    style="overflow: hidden;"
  >
    <!-- Cell toolbar -->
    <div class="row" style="justify-content: space-between; align-items: center; padding: var(--sh-space-2) var(--sh-space-3); background: var(--sh-paper-2); border-bottom: var(--sh-border-thin); flex-wrap: wrap; gap: var(--sh-space-2);">
      <div class="row" style="flex: 1 1 0; gap: var(--sh-space-2); align-items: center; min-width: 0; flex-wrap: nowrap; overflow: hidden;">
        <button
          @click="emit('collapse')"
          class="btn btn-ghost btn-sm"
          style="padding: 2px; flex: none;"
          :aria-label="`${cell.collapsed ? 'Expand' : 'Minimize'} ${ariaTitleOf(cell)}`"
          :aria-expanded="!cell.collapsed"
          data-testid="cell-minimize"
        >
          <Icon :name="cell.collapsed ? 'chevronRight' : 'chevronDown'" :size="14" />
        </button>
        <Icon :name="CELL_ICON[cell.type]" :size="14" role="img" :aria-label="CELL_KIND[cell.type]" :title="CELL_KIND[cell.type]" style="flex: none;" />
        <template v-if="cell.collapsed">
          <span class="sh-mono" style="min-width: 0; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; cursor: pointer;" data-testid="cell-min-title" @click="emit('collapse')">{{ titleOf(cell) }}</span>
          <span v-if="cellMeta(cell)" class="sh-mono" data-testid="cell-min-meta" style="color: var(--sh-graphite); font-size: var(--sh-fs-xs); white-space: nowrap; flex: none;">{{ cellMeta(cell) }}</span>
        </template>
        <template v-else>
          <input
            v-if="cell.type === 'sql'"
            v-model="cell.label"
            placeholder="label"
            autocomplete="off"
            autocapitalize="off"
            autocorrect="off"
            spellcheck="false"
            class="sh-mono"
            title="Label"
            style="border: none; background: transparent; font-size: var(--sh-fs-xs); color: var(--sh-graphite); flex: 1 1 110px; min-width: 40px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;"
          />
          <ConnectionSelect v-if="cell.type === 'sql'" v-model="cell.connection" variant="cell" style="flex: none;" />
          <span
            v-if="isStale(cell)"
            class="badge"
            style="background: var(--slaw-highlight); color: var(--slaw-highlight-fg); white-space: nowrap; flex: none;"
            data-testid="stale-badge"
          >EDITED · RUN TO UPDATE</span>
        </template>
      </div>
      <div class="row" style="gap: var(--sh-space-1); flex: none;">
        <template v-if="!cell.collapsed">
          <template v-if="cell.type === 'sql'">
            <button
              v-for="v in CLASSIC_VIEWS"
              :key="v"
              @click="pickView(cell, v)"
              class="btn btn-ghost btn-sm"
              :class="{ 'row-selected': cell.view === v }"
              :style="shapeOf(cell) && !viewFit(v, shapeOf(cell)!).ok ? { opacity: 0.45 } : {}"
              :title="shapeOf(cell) && !viewFit(v, shapeOf(cell)!).ok ? viewFit(v, shapeOf(cell)!).why : VIEW_HINTS[v]"
              :data-testid="`view-${v}`"
            >{{ VIEW_LABELS[v] }}</button>
            <select
              class="btn btn-ghost btn-sm sh-mono"
              :class="{ 'row-selected': CHART_VIEWS.includes(cell.view) }"
              :value="CHART_VIEWS.includes(cell.view) ? cell.view : ''"
              aria-label="Chart view"
              title="More chart views"
              data-testid="view-charts"
              @change="pickView(cell, ($event.target as HTMLSelectElement).value as ViewKind)"
            >
              <option value="" disabled>CHARTS</option>
              <option
                v-for="v in CHART_VIEWS" :key="v" :value="v"
                :title="shapeOf(cell) && !viewFit(v, shapeOf(cell)!).ok ? viewFit(v, shapeOf(cell)!).why : VIEW_HINTS[v]"
              >{{ VIEW_LABELS[v] }}{{ shapeOf(cell) && !viewFit(v, shapeOf(cell)!).ok ? ' (won\'t fit)' : '' }}</option>
            </select>
            <button @click="emit('run')" :disabled="cell.running || !cell.source.trim()" :title="cell.source.trim() ? undefined : 'Write a query first'" class="btn btn-primary btn-sm" data-testid="run-cell">
              <Icon name="play" :size="12" /> {{ cell.running ? 'Running…' : 'Run' }}
            </button>
          </template>
          <template v-else-if="cell.type === 'markdown'">
            <button @click="emit('toggleMd')" class="btn btn-ghost btn-sm">{{ cell.mdPreview ? 'Edit' : 'Preview' }}</button>
          </template>
          <button
            v-if="AI_CELL_ENABLED && (cell.type === 'sql' || cell.type === 'markdown')"
            @click="emit('aiEdit')"
            class="btn btn-ghost btn-sm"
            title="Edit with AI"
            aria-label="Edit with AI"
            :data-testid="`ai-cell-edit-${cell.id}`"
          ><Icon name="sparkle" :size="14" /></button>
        </template>
        <button @click="emit('move', -1)" class="btn btn-ghost btn-sm" title="Move up" aria-label="Move cell up"><Icon name="chevronUp" :size="14" /></button>
        <button @click="emit('move', 1)" class="btn btn-ghost btn-sm" title="Move down" aria-label="Move cell down"><Icon name="chevronDown" :size="14" /></button>
        <button @click="emit('remove')" class="btn btn-ghost btn-sm" style="color: var(--sh-error);" title="Delete cell" aria-label="Delete cell"><Icon name="trash" :size="14" /></button>
      </div>
    </div>

    <div v-show="!cell.collapsed">
      <!-- SQL cell -->
      <template v-if="cell.type === 'sql'">
        <CellDrawer :open="cell.sourceOpen" testid="sql-drawer" @toggle="emit('toggleSource')">
          <template #summary>
            <template v-if="cell.sourceOpen">Hide SQL</template>
            <template v-else>{{ summaryOf(cell) }}</template>
          </template>
          <div :ref="(el) => { cell.editorEl = el as HTMLElement }" style="min-height: 90px; border-top: var(--sh-border-thin); border-bottom: var(--sh-border-thin);"></div>
          <div
            v-if="AI_CELL_ENABLED && !cell.source.trim()"
            class="sh-mono"
            style="font-size: var(--sh-fs-xs); color: var(--sh-graphite); padding: var(--sh-space-1) var(--sh-space-3); display: flex; align-items: center; gap: 6px;"
          >
            write SQL or
            <button @click="emit('aiEdit', 'Generate with AI')" class="btn btn-ghost btn-sm" style="padding: 2px 6px;" :data-testid="`ai-cell-generate-empty-${cell.id}`">
              <Icon name="sparkle" :size="12" /> generate it with AI
            </button>
          </div>
          <!-- Standing explanation of the selected view (review fix,
               step 4a round 2 #6 — ported from notebook.js's
               nb-cell__viewhint), now inside the drawer alongside the
               editor it explains. -->
          <div class="sh-mono" data-testid="view-hint" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite); padding: var(--sh-space-1) var(--sh-space-3);">
            {{ VIEW_LABELS[cell.view] }} · {{ VIEW_HINTS[cell.view] }}<template v-if="!cell.viewPinned"> · auto</template>
          </div>
          <div v-if="cell.view !== 'table' && cell.view !== 'stat'" style="padding: 0 var(--sh-space-3) var(--sh-space-1);">
            <CellMotionPicker v-model="cell.motion" />
          </div>
          <div class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite); padding: 0 var(--sh-space-3) var(--sh-space-1);">
            Notebook cells are read-only queries — use the Query editor to change data. (Ctrl/Cmd+Enter to run.)
          </div>
        </CellDrawer>
        <div style="padding: var(--sh-space-3);">
          <div v-if="cell.result?.error" class="sh-mono" style="color: var(--sh-error); font-size: var(--sh-fs-sm);" data-testid="cell-error">
            {{ cell.result.error }}
          </div>
          <template v-else-if="cell.result">
            <div v-if="mismatchInfo(cell)" class="sh-mono" style="font-size: var(--sh-fs-xs); margin-bottom: var(--sh-space-2);" data-testid="mismatch-note">
              <div style="font-weight: 700; color: var(--sh-error);">{{ mismatchInfo(cell)!.label }} doesn't fit — wants {{ mismatchInfo(cell)!.hint }}</div>
              <div style="color: var(--sh-graphite); margin-top: 2px;">{{ mismatchInfo(cell)!.shapeLine }}</div>
              <div v-if="mismatchInfo(cell)!.fits.length" class="row" style="gap: var(--sh-space-1); align-items: center; margin-top: var(--sh-space-2);">
                <span style="color: var(--sh-graphite);">SHOWS AS</span>
                <button
                  v-for="v in mismatchInfo(cell)!.fits"
                  :key="v"
                  @click="pickView(cell, v)"
                  class="btn btn-ghost btn-sm"
                  :data-testid="`shows-as-${v}`"
                >{{ VIEW_LABELS[v] }}</button>
              </div>
            </div>
            <A2UIPage v-else-if="synthesizedPage(cell)" :page="synthesizedPage(cell)!" :signal="signal" />
          </template>
          <div v-else class="sh-mono" style="color: var(--sh-graphite); font-size: var(--sh-fs-xs);">Not run yet.</div>
          <div v-if="cell.result && !cell.result.error && !cell.connection && defaultConnectionName !== 'local'" class="sh-mono" style="color: var(--sh-graphite); font-size: var(--sh-fs-xs); margin-top: var(--sh-space-2);" data-testid="cell-default-note">
            No connection chosen, so this ran on the workspace default ({{ defaultConnectionName }}).
          </div>
        </div>
      </template>

      <!-- Markdown cell -->
      <template v-else-if="cell.type === 'markdown'">
        <div v-if="cell.mdPreview" class="prose slaw-content slaw-md-content" style="padding: var(--sh-space-4);" v-html="renderSafeMarkdown(cell.source)"></div>
        <div v-else :ref="(el) => { cell.editorEl = el as HTMLElement }" style="min-height: 90px;"></div>
      </template>

      <!-- A2UI cell (lazy-mounted: review fix, step 4a #1 — the page,
           and every live query its data-bound components fire, is not
           rendered until this cell has scrolled into view) -->
      <template v-else-if="cell.type === 'a2ui'">
        <div style="padding: var(--sh-space-4); min-height: 48px;">
          <A2UIPage v-if="cell.page && cell.visible" :page="cell.page" :signal="signal" />
          <div v-else class="sh-mono" style="color: var(--sh-graphite); font-size: var(--sh-fs-xs);">Scroll into view to render…</div>
        </div>
        <div style="border-top: var(--sh-border-thin);">
          <CellDrawer :open="cell.sourceOpen" testid="a2ui-drawer" @toggle="emit('toggleSource')">
            <template #summary>Advanced: card definition</template>
            <!-- Kept behind v-if (rather than always rendering) so the
                 view doesn't stringify every a2ui page on every
                 keystroke re-render — the cost is that closing this
                 one snaps instead of sliding, which goes away once
                 #88 makes the body an editor instead of a <pre>. -->
            <pre v-if="cell.sourceOpen" class="sh-mono" style="font-size: var(--sh-fs-xs); padding: var(--sh-space-3); overflow: auto; background: var(--sh-paper-2); margin: 0;">{{ JSON.stringify(cell.page, null, 2) }}</pre>
          </CellDrawer>
        </div>
      </template>
    </div>
  </div>
</template>
