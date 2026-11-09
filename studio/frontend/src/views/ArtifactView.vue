<script setup lang="ts">
// ArtifactView — one data artifact, live (#162): parameter controls and the
// bound components of its view. The page rendered is the one Go validated and
// re-marshalled (GetArtifact), never the stored bytes parsed here. A broken
// artifact opens to its errors. The runs behind it share one pool lane that
// never starves a person's own Run (useArtifactRuns); a workspace switch drops
// them. Error text is text interpolation only.
import { computed, provide, ref, watch, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { backToLibrary, libraryPath } from '../lib/libraryRoutes'
import Icon from '../components/Icon.vue'
import A2UIPage from '../a2ui/A2UIPage.vue'
import ArtifactSource from '../components/ArtifactSource.vue'
import ArtifactViewTrust from '../components/ArtifactViewTrust.vue'
import { ARTIFACT_RUNTIME, type ArtifactRuntime } from '../a2ui/artifactRuntime'
import { useArtifact } from '../composables/useArtifact'
import { useArtifactParams } from '../composables/useArtifactParams'
import { useArtifactRuns } from '../composables/useArtifactRuns'
import { workspaceInfo } from '../composables/useWorkspace'

const route = useRoute()
const router = useRouter()
const name = computed(() => String(route.params.name ?? ''))
const { detail, loading, failure, reload } = useArtifact(() => name.value)

const params = useArtifactParams(() => detail.value)
// A fresh artifact starts from its defaults; a reload of the same one keeps the values still valid.
watch(detail, (d, before) => params.load(d ?? null, !!d && d.name === before?.name), { immediate: true, flush: 'sync' })

// Runs started in one workspace must not land in another.
const sessionId = workspaceInfo.value?.sessionId
const runs = useArtifactRuns({
  name: () => detail.value?.name ?? '',
  queries: () => detail.value?.queries ?? [],
  values: () => params.values,
  rev: () => detail.value?.rev ?? '',
  sessionOk: () => workspaceInfo.value?.sessionId === sessionId,
})
onUnmounted(runs.dispose)

const runtime: ArtifactRuntime = {
  // Asking for a query's state is what starts its run, once, for every component bound to it.
  result: (query) => { runs.demand(query); return runs.result(query) },
  params: { param: params.param, display: params.display, error: params.error, isDefault: params.isDefault, set: params.set, reset: params.reset },
}
provide(ARTIFACT_RUNTIME, runtime)

const page = computed(() => (detail.value && !detail.value.error ? detail.value.view : undefined))
// A custom view (script or HTML page) is shown as source and runs nothing until the person allows it.
const isScript = computed(() => (detail.value?.view_kind === 'script' || detail.value?.view_kind === 'html') && !detail.value.error)
// A page's frame is drawn again when the tick moves.
const pageTick = ref(0)
function refresh() { void reload(); runs.rerunAll(); pageTick.value++ }

// View | Source: the Source tab stays mounted so an unsaved edit survives a flip back.
const tab = ref<'view' | 'source'>('view')
// A save changes the files: re-read the artifact and run its queries again.
const onSaved = refresh
const onGone = () => router.push(libraryPath())
</script>

<template>
  <div class="col" style="height: 100%; gap: 0;">
    <div class="row" style="justify-content: space-between; align-items: center; padding: var(--sh-space-3) var(--sh-space-6); border-bottom: var(--sh-border);">
      <div class="row" style="gap: var(--sh-space-3); align-items: center; min-width: 0;">
        <button type="button" class="btn btn-ghost btn-sm" aria-label="Back to the library" title="Back to the library" @click="backToLibrary(router, libraryPath())"><Icon name="arrowLeft" :size="14" /></button>
        <div style="min-width: 0;">
          <h2 class="card-title" style="margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;" data-testid="artifact-title">{{ detail?.title || name }}</h2>
          <div v-if="detail?.description" class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite);">{{ detail.description }}</div>
        </div>
      </div>
      <div class="row" style="gap: var(--sh-space-3); align-items: center;">
        <div class="seg" role="group" aria-label="Artifact tab">
          <button type="button" :class="{ on: tab === 'view' }" :aria-pressed="tab === 'view'" data-testid="artifact-tab-view" @click="tab = 'view'">View</button>
          <button type="button" :class="{ on: tab === 'source' }" :aria-pressed="tab === 'source'" data-testid="artifact-tab-source" @click="tab = 'source'">Source</button>
        </div>
        <button v-show="tab === 'view'" type="button" class="btn btn-ghost btn-sm" title="Re-run every query" data-testid="artifact-refresh" @click="refresh"><Icon name="refresh" :size="14" /> Refresh</button>
      </div>
    </div>

    <div v-show="tab === 'source'" style="flex: 1; min-height: 0; overflow: auto; padding: var(--sh-space-6);">
      <ArtifactSource :key="name" :name="name" :active="tab === 'source'" @saved="onSaved" @gone="onGone" />
    </div>

    <div v-show="tab === 'view'" style="flex: 1; overflow: auto; padding: var(--sh-space-6);">
      <div v-if="failure" class="artifact-failure" role="alert" data-testid="artifact-failure">{{ failure }}</div>
      <div v-else-if="loading && !detail" class="sh-mono" style="color: var(--sh-graphite);">Loading…</div>
      <div v-else-if="detail?.error" class="card card-pad col" style="gap: var(--sh-space-2); border-color: var(--sh-error);" role="alert" data-testid="artifact-errors">
        <strong>This artifact can't be shown yet.</strong>
        <span class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite);">Fix <code>artifacts/{{ detail.name }}/artifact.yaml</code> or its view file (<code>view.a2ui.json</code>, <code>view.js</code> or <code>html/</code>). This page updates when the file is saved.</span>
        <pre class="sh-mono artifact-errors">{{ detail.error }}</pre>
      </div>
      <ArtifactViewTrust v-else-if="isScript" :key="name" :name="name" :detail="detail" :values="params.values" :refresh="pageTick" />
      <template v-else-if="page">
        <div v-if="runs.truncated.value.length" class="sh-mono artifact-note" data-testid="artifact-truncated">
          Showing the first 500 rows of {{ runs.truncated.value.join(', ') }}.
        </div>
        <A2UIPage :key="detail?.rev" :page="page" class="artifact-page" />
      </template>
    </div>
  </div>
</template>

<style scoped>
.artifact-failure { border: 2px solid var(--sh-error); background: var(--sh-paper-2); color: var(--sh-error); padding: var(--sh-space-3); border-radius: var(--sh-radius); }
.artifact-errors { white-space: pre-wrap; margin: 0; color: var(--sh-error); font-size: var(--sh-fs-xs); }
.artifact-page :deep(.a2ui-page__title) { display: none; }
.artifact-note { font-size: var(--sh-fs-xs); color: var(--sh-graphite); margin-bottom: var(--sh-space-3); }
</style>
