<script setup lang="ts">
import { App } from '../lib/bindings'
// Studio Home. Backed directly by GetDashboard's live counts and the workspace's
// own tables (the default connection's, design #106 item 8), refreshed on
// "data:changed". Titled with the workspace's name.
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { libraryPath, type LibraryType } from '../lib/libraryRoutes'
import { EventsOn } from '../wailsjs/runtime/runtime'
import Icon from '../components/Icon.vue'
import { errorText } from '../lib/errors'
import { hasWailsRuntime } from '../lib/wails'

const router = useRouter()
const title = ref('Home')
const description = ref('')
const tableCount = ref(0)
const notebookCount = ref(0)
const queryCount = ref(0)
const liveCount = ref(0)
const pageCount = ref(0)
const scriptCount = ref(0)
const brokenCount = ref(0)
const localTables = ref<string[]>([])
const defaultConnection = ref('')
const readOnly = ref(false)
const loading = ref(true)
const errorMessage = ref('')

// The Library tiles: Chart scripts appear only once there is one (as the chip does).
const libraryTiles = computed(() => {
  const tiles: { type: LibraryType; label: string; count: number }[] = [
    { type: 'notebook', label: 'Notebooks', count: notebookCount.value },
    { type: 'live', label: 'Live views', count: liveCount.value },
    { type: 'page', label: 'Pages', count: pageCount.value },
  ]
  if (scriptCount.value > 0) tiles.push({ type: 'script', label: 'Chart scripts', count: scriptCount.value })
  return tiles
})

let loadGen = 0

async function load() {
  const gen = ++loadGen
  loading.value = true
  errorMessage.value = ''
  try {
    const d = await App.GetDashboard()
    if (gen !== loadGen) return
    title.value = d?.title || 'Home'
    description.value = d?.description || ''
    tableCount.value = d?.table_count ?? 0
    notebookCount.value = d?.notebook_count ?? 0
    queryCount.value = d?.query_count ?? 0
    liveCount.value = d?.artifact_counts?.live ?? 0
    pageCount.value = d?.artifact_counts?.page ?? 0
    scriptCount.value = d?.artifact_counts?.script ?? 0
    brokenCount.value = d?.artifact_counts?.broken ?? 0
    localTables.value = d?.local_tables ?? []
    defaultConnection.value = d?.default_connection ?? ''
    readOnly.value = !!d?.read_only
  } catch (e: unknown) {
    if (gen !== loadGen) return
    errorMessage.value = errorText(e)
  }
  loading.value = false
}

let unsubDataChanged: (() => void) | null = null

onMounted(() => {
  load()
  // Re-fetch the live counts when the data underneath changes (imports,
  // table create/drop, notebook edits) so the dashboard never shows stale
  // numbers until a manual reload.
  if (hasWailsRuntime()) unsubDataChanged = EventsOn('data:changed', load)
})
onUnmounted(() => {
  unsubDataChanged?.()
})
</script>

<template>
  <div style="height: 100%; overflow-y: auto; padding: 32px;">
    <div class="page-head">
      <div>
        <div class="crumb">{{ title }} <span class="sep">/</span> Home</div>
        <h1>{{ title }}</h1>
      </div>
      <div class="row">
        <button class="btn" @click="router.push('/import')">
          <Icon name="download" :size="16" /> Import data
        </button>
        <button class="btn btn-primary" @click="router.push(libraryPath(undefined, 'notebook'))">
          <Icon name="plus" :size="16" /> New notebook
        </button>
      </div>
    </div>

    <p v-if="description" class="sh-lead" style="margin-bottom: 24px; max-width: 720px;">
      {{ description }}
    </p>

    <p v-if="errorMessage" class="sh-mono" role="alert" style="color: var(--sh-error);">
      {{ errorMessage }}
    </p>

    <div v-if="loading" class="sh-mono" style="color: var(--sh-graphite);">Loading…</div>

    <div v-else class="dashboard-stats" data-testid="dashboard-stats" style="display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 16px; margin-bottom: 8px;">
      <div class="card" style="padding: 20px;">
        <div class="sh-meta">Tables</div>
        <div style="font-family: var(--sh-font-display); font-weight: 600; font-size: 32px;">{{ tableCount }}</div>
      </div>
      <button
        v-for="t in libraryTiles"
        :key="t.type"
        type="button"
        class="card"
        style="padding: 20px; text-align: left; cursor: pointer; color: var(--sh-ink);"
        :data-testid="`library-tile-${t.type}`"
        @click="router.push(libraryPath(t.type))"
      >
        <div class="sh-meta">{{ t.label }}</div>
        <div style="font-family: var(--sh-font-display); font-weight: 600; font-size: 32px;">{{ t.count }}</div>
      </button>
      <div class="card" style="padding: 20px;">
        <div class="sh-meta">Saved queries</div>
        <div style="font-family: var(--sh-font-display); font-weight: 600; font-size: 32px;">{{ queryCount }}</div>
      </div>
    </div>
    <p v-if="!loading && brokenCount > 0" class="help" data-testid="library-broken" style="margin-bottom: 8px;">
      {{ brokenCount }} {{ brokenCount === 1 ? 'artifact needs' : 'artifacts need' }} fixing.
      <a href="#" @click.prevent="router.push(libraryPath())">Open the Library</a>
    </p>

    <div v-if="!loading" class="sec-title" style="margin-top: 32px;">
      <h2>Your tables</h2>
      <span class="label">{{ defaultConnection || 'local' }} · the workspace's own database</span>
    </div>
    <div v-if="!loading && localTables.length === 0" class="card" style="padding: 18px 20px;" data-testid="home-no-tables">
      <div class="sh-body" style="color: var(--sh-graphite);">
        <template v-if="readOnly">The workspace's database is in use by another program, so its tables can't be listed yet.</template>
        <template v-else>No tables yet. Import a CSV, or ask the Slaw assistant to build one.</template>
      </div>
    </div>
    <div v-else-if="!loading" style="display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: 12px; margin-bottom: 8px;" data-testid="home-tables">
      <button
        v-for="t in localTables"
        :key="t"
        type="button"
        class="card"
        style="padding: 12px 14px; box-shadow: var(--sh-shadow-sm); text-align: left; cursor: pointer; color: var(--sh-ink);"
        data-testid="home-table"
        @click="router.push({ path: '/tables', query: { table: t } })"
      >
        <span class="row" style="gap: 8px;"><Icon name="database" :size="14" /><b style="font-family: var(--sh-font-mono); font-size: 13px; overflow-wrap: anywhere;">{{ t.split('.').pop() }}</b></span>
        <span class="help">{{ t }}</span>
      </button>
    </div>

    <div class="sec-title" style="margin-top: 32px;">
      <h2>Quick actions</h2>
      <span class="label">Jump back in</span>
    </div>
    <div style="display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px;">
      <div class="card" style="padding: 20px 20px 18px 28px; cursor: pointer; border-left: 6px solid var(--sh-teal);" @click="router.push(libraryPath())" data-testid="library-card">
        <div style="width: 40px; height: 40px; border: 1.5px solid var(--sh-ink); border-radius: var(--sh-radius-sm); background: var(--sh-paper-2); display: inline-flex; align-items: center; justify-content: center;">
          <Icon name="layers" :size="22" />
        </div>
        <div style="font-family: var(--sh-font-display); font-weight: 600; font-size: 20px; letter-spacing: -0.015em; margin-top: 12px;">Library</div>
        <div class="sh-meta" style="margin-top: 4px;">Notebooks, live views and pages</div>
      </div>
      <div class="card" style="padding: 20px 20px 18px 28px; cursor: pointer; border-left: 6px solid var(--sh-subj-science);" @click="router.push('/tables')">
        <div style="width: 40px; height: 40px; border: 1.5px solid var(--sh-ink); border-radius: var(--sh-radius-sm); background: var(--sh-paper-2); display: inline-flex; align-items: center; justify-content: center;">
          <Icon name="database" :size="22" />
        </div>
        <div style="font-family: var(--sh-font-display); font-weight: 600; font-size: 20px; letter-spacing: -0.015em; margin-top: 12px;">Tables</div>
        <div class="sh-meta" style="margin-top: 4px;">Browse and edit your data</div>
      </div>
      <div class="card" style="padding: 20px 20px 18px 28px; cursor: pointer; border-left: 6px solid var(--sh-subj-math);" @click="router.push('/queries')">
        <div style="width: 40px; height: 40px; border: 1.5px solid var(--sh-ink); border-radius: var(--sh-radius-sm); background: var(--sh-paper-2); display: inline-flex; align-items: center; justify-content: center;">
          <Icon name="terminal" :size="22" />
        </div>
        <div style="font-family: var(--sh-font-display); font-weight: 600; font-size: 20px; letter-spacing: -0.015em; margin-top: 12px;">Queries</div>
        <div class="sh-meta" style="margin-top: 4px;">Run and save SQL</div>
      </div>
    </div>
  </div>
</template>
