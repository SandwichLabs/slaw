<script setup lang="ts">
// LibraryView: every notebook and artifact in one list (docs/plans/library.md
// L5). It wires useLibrary and renders; titles, names and errors come from
// files and reach the page by interpolation only. New opens the type
// picker (NewItemDialog), preselected from ?new= or the open chip.
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import Icon from '../components/Icon.vue'
import NewItemDialog from '../components/NewItemDialog.vue'
import { useLibrary } from '../composables/useLibrary'
import { useNotebookActions } from '../composables/useNotebookActions'
import { artifactPath, isLibraryType, libraryPath, notebookPath, type LibraryType } from '../lib/libraryRoutes'
import { CHIP_LABEL_LOWER, TYPE_LABEL, fullTime, relativeTime, type LibraryItem } from '../lib/libraryItems'

const route = useRoute()
const router = useRouter()
const type = computed<LibraryType | null>(() => (isLibraryType(route.query.type) ? route.query.type : null))
const { all, items, chips, errors, loading, filter, sortBy, reload } = useLibrary(type)
const actions = useNotebookActions({ reload, open: (id) => { void router.push(notebookPath(id)) } })
const newOpen = ref(false)
const newType = ref<LibraryType | null>(null)
const filterEl = ref<HTMLInputElement | null>(null)

function open(i: LibraryItem) { void router.push(i.source === 'notebook' ? notebookPath(i.id) : artifactPath(i.id)) }
function setType(t: LibraryType | null) { if (t !== type.value) void router.push(libraryPath(t ?? undefined)) }
function openNew(t: LibraryType | null) { newType.value = t; newOpen.value = true }
function created(to: string) { newOpen.value = false; void reload(); void router.push(to) }

// ?new=<type> (or 1: a notebook) opens the picker once; the URL then drops it so Back never reopens it.
watch(() => route.query.new, (n) => {
  if (n === undefined) return
  openNew(n === '1' ? 'notebook' : isLibraryType(n) ? n : null)
  const { new: _drop, ...rest } = route.query
  void router.replace({ path: route.path, query: rest })
}, { immediate: true })

function onSlash(e: KeyboardEvent) {
  const el = document.activeElement as HTMLElement | null
  if (document.querySelector('[aria-modal="true"]')) return
  const typing = el && (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName))
  if (e.key !== '/' || typing || e.metaKey || e.ctrlKey || e.altKey) return
  e.preventDefault()
  filterEl.value?.focus()
}
onMounted(() => document.addEventListener('keydown', onSlash))
onUnmounted(() => document.removeEventListener('keydown', onSlash))
</script>

<template>
  <div class="col" style="height: 100%; gap: 0;">
    <div class="row" style="justify-content: space-between; padding: var(--sh-space-4) var(--sh-space-6); border-bottom: var(--sh-border);">
      <h2 class="sh-label" style="margin: 0;">Library</h2>
      <div class="row" style="gap: var(--sh-space-2);">
        <button type="button" class="btn btn-ghost btn-sm" :disabled="actions.importing.value" @click="actions.importNotebook"><Icon name="upload" :size="14" /> Import notebook</button>
        <button type="button" class="btn btn-primary btn-sm" data-testid="new-item" @click="openNew(type)"><Icon name="plus" :size="14" /> New</button>
      </div>
    </div>

    <div class="lib-bar">
      <div class="row" style="gap: var(--sh-space-2); flex-wrap: wrap;" role="group" aria-label="Filter by type">
        <button v-for="c in chips" :key="c.label" type="button" class="lib-chip" :aria-pressed="c.type === type" :data-testid="`lib-chip-${c.type ?? 'all'}`" @click="setType(c.type)">{{ c.label }} <span class="lib-chip__n">{{ c.count }}</span></button>
      </div>
      <div class="row" style="gap: var(--sh-space-2);">
        <input ref="filterEl" v-model="filter" class="field-input" style="width: 200px;" placeholder="Filter by name" aria-label="Filter by name" autocomplete="off" spellcheck="false" data-testid="lib-filter" />
        <button type="button" class="btn btn-ghost btn-sm" data-testid="lib-sort" @click="sortBy = sortBy === 'recent' ? 'name' : 'recent'">Sort: {{ sortBy === 'recent' ? 'Recently changed' : 'Name A-Z' }}</button>
      </div>
    </div>

    <div style="flex: 1; overflow: auto; padding: var(--sh-space-6);">
      <div v-for="e in errors" :key="e" class="lib-error" role="alert" data-testid="library-error">{{ e }}</div>
      <div v-if="loading" class="sh-mono" style="color: var(--sh-graphite);">Loading…</div>
      <div v-else-if="all.length === 0 && errors.length === 0" class="lib-empty" data-testid="library-empty">
        <Icon name="layers" :size="32" style="color: var(--sh-graphite);" />
        <div class="sh-mono" style="color: var(--sh-graphite); max-width: 420px;">Nothing in the library yet. <b>New</b> makes a notebook, a live view or a page, or ask the Slaw assistant to build one.</div>
      </div>
      <div v-else-if="items.length === 0 && (all.length > 0 || errors.length === 0)" class="lib-empty" data-testid="library-empty-filter">
        <div v-if="filter.trim()" class="sh-mono" style="color: var(--sh-graphite);">Nothing matches “{{ filter.trim() }}”.</div>
        <div v-else class="sh-mono" style="color: var(--sh-graphite);">No {{ CHIP_LABEL_LOWER[type ?? 'notebook'] }} yet</div>
        <button v-if="!filter.trim() && type" type="button" class="btn btn-primary btn-sm" data-testid="empty-new" @click="openNew(type)"><Icon name="plus" :size="14" /> New {{ TYPE_LABEL[type].toLowerCase() }}</button>
      </div>
      <ul v-else class="col" style="gap: var(--sh-space-2); list-style: none; margin: 0; padding: 0;">
        <li v-for="i in items" :key="i.key" class="card lib-row" :class="{ 'lib-row--broken': !!i.error }">
          <button type="button" class="lib-open" :data-testid="i.source === 'notebook' ? 'notebook-row' : 'artifact-row'" :data-notebook-id="i.source === 'notebook' ? i.id : undefined" :data-artifact-name="i.source === 'artifact' ? i.id : undefined" @click="open(i)">
            <span class="lib-type"><Icon :name="i.icon" :size="16" :style="{ color: i.error ? 'var(--sh-error)' : 'var(--sh-teal)', flex: 'none' }" /> {{ i.typeLabel }}</span>
            <span class="lib-title">{{ i.title }}</span>
            <span v-if="i.error" class="lib-meta lib-meta--error" :title="i.error">{{ i.error }}</span>
            <span v-else class="lib-meta">{{ i.detail }}</span>
            <span v-if="i.error" class="badge badge--warn">Needs fixing</span>
            <span v-else class="lib-meta lib-when" :title="fullTime(i.updatedAt)">{{ relativeTime(i.updatedAt) }}</span>
          </button>
          <span class="lib-actions"><button v-if="i.source === 'notebook'" type="button" class="btn btn-ghost btn-sm" style="color: var(--sh-error);" :aria-label="`Delete notebook ${i.title}`" :title="`Delete ${i.title}`" @click="actions.pendingDeleteId.value = i.id"><Icon name="trash" :size="14" /></button></span>
        </li>
      </ul>
    </div>

    <NewItemDialog :visible="newOpen" :initial-type="newType" @close="newOpen = false" @created="created" />
    <ConfirmDialog :visible="!!actions.pendingDeleteId.value" title="Delete notebook" confirm-label="Delete" message="Delete this notebook? This can't be undone." @confirm="actions.confirmDelete" @cancel="actions.pendingDeleteId.value = null" />
    <div v-if="actions.message.value" class="error-toast">{{ actions.message.value }}</div>
  </div>
</template>

<style scoped>
.lib-bar { display: flex; justify-content: space-between; align-items: center; gap: var(--sh-space-3); flex-wrap: wrap; padding: var(--sh-space-3) var(--sh-space-6); border-bottom: var(--sh-border); }
.lib-chip { font: inherit; font-size: var(--sh-fs-sm); cursor: pointer; padding: 2px var(--sh-space-3); border: 1.5px solid var(--sh-ink); border-radius: 999px; background: var(--sh-paper); color: var(--sh-ink); }
.lib-chip[aria-pressed='true'] { background: var(--sh-ink); color: var(--sh-paper); }
.lib-chip__n { font-family: var(--sh-font-mono); font-size: var(--sh-fs-xs); opacity: 0.75; }
.lib-error { border: 2px solid var(--sh-error); background: var(--sh-paper-2); color: var(--sh-error); padding: var(--sh-space-3); border-radius: var(--sh-radius); margin-bottom: var(--sh-space-3); }
.lib-empty { display: flex; flex-direction: column; align-items: center; gap: var(--sh-space-3); margin-top: var(--sh-space-10); text-align: center; }
.lib-row { display: flex; align-items: center; padding: var(--sh-space-2) var(--sh-space-3); }
.lib-row--broken { border-color: var(--sh-error); }
.lib-open { flex: 1; min-width: 0; display: grid; grid-template-columns: 9rem minmax(0, 1.2fr) minmax(0, 2fr) 8rem; align-items: center; gap: var(--sh-space-3); font: inherit; color: inherit; text-align: left; background: none; border: 0; padding: var(--sh-space-2) 0; cursor: pointer; }
.lib-type { display: flex; align-items: center; gap: var(--sh-space-2); font-size: var(--sh-fs-sm); }
.lib-title { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lib-meta { font-family: var(--sh-font-mono); font-size: var(--sh-fs-xs); color: var(--sh-graphite); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lib-meta--error { color: var(--sh-error); }
.lib-actions { flex: none; width: 2.5rem; display: flex; justify-content: flex-end; }
.lib-when { text-align: right; }
@media (max-width: 760px) { .lib-open { grid-template-columns: 1fr; gap: var(--sh-space-1); } .lib-actions { flex: none; width: 2.5rem; display: flex; justify-content: flex-end; }
.lib-when { text-align: left; } }
</style>
