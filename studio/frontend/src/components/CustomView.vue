<script setup lang="ts">
// CustomView — an allowed custom artifact view (view: script, #160). The view's
// code runs in a Worker (useViewBridge); here are the parameter controls built
// from the manifest (level-1 A2UIControl, plus Reset) and what it drew: an SVG,
// shown only as <img src="blob:…">, or an A2UI page Go validated, rendered with
// the level-1 renderer and the artifact runtime. A view that stops responding
// is terminated and shows Reload. Everything the view says is text.
import { ref, watch } from 'vue'
import A2UIPage from '../a2ui/A2UIPage.vue'
import ArtifactControls from './ArtifactControls.vue'
import { useCustomView } from '../composables/useCustomView'
import type { ParamValue } from '../a2ui/artifactParams'

const props = defineProps<{ name: string; detail: ArtifactDetail; values: Record<string, ParamValue> }>()
const stage = ref<HTMLElement | null>(null)
const { phase, failure, drawError, svgUrl, page, drawing, reload } = useCustomView(() => props.name, () => props.values, stage)

const imageFailed = ref(false)
// A failed image belongs to that image: a new drawing (or none) starts clean.
watch(svgUrl, () => { imageFailed.value = false })
</script>

<template>
  <div class="custom-view" data-testid="custom-view">
    <ArtifactControls :detail="detail" testid="custom-view" />

    <div ref="stage" class="custom-view__stage" :aria-busy="phase === 'starting' || drawing">
      <div v-if="phase === 'stopped' || phase === 'failed'" class="custom-view__stopped" role="alert" data-testid="custom-view-stopped">
        <strong>{{ phase === 'stopped' ? failure : 'This view could not run.' }}</strong>
        <span v-if="phase === 'failed'" class="sh-mono custom-view__why">{{ failure }}</span>
        <button type="button" class="btn btn-sm" data-testid="custom-view-reload" @click="reload()">Reload</button>
      </div>
      <template v-else>
        <div v-if="drawError" class="custom-view__error" role="alert" data-testid="custom-view-error">{{ drawError }}</div>
        <img
          v-if="svgUrl" :key="svgUrl" :src="svgUrl" :alt="detail.title || name" class="custom-view__img" data-testid="custom-view-image"
          @load="imageFailed = false" @error="imageFailed = true"
        />
        <div v-if="svgUrl && imageFailed" class="custom-view__error" role="alert" data-testid="custom-view-image-error">The drawing couldn't be shown.</div>
        <A2UIPage v-if="page" :page="page" class="custom-view__page" data-testid="custom-view-page" />
        <div v-if="!svgUrl && !page && !drawError" class="sh-mono custom-view__wait" role="status">Drawing…</div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.custom-view { display: flex; flex-direction: column; gap: var(--sh-space-4); min-width: 0; }
.custom-view__stage { min-height: 240px; min-width: 0; }
.custom-view__img { display: block; max-width: 100%; height: auto; }
.custom-view__wait { color: var(--sh-graphite); font-size: var(--sh-fs-xs); }
.custom-view__stopped { display: flex; flex-wrap: wrap; align-items: center; gap: var(--sh-space-3); border: 2px solid var(--sh-error); background: var(--sh-paper-2); padding: var(--sh-space-3); border-radius: var(--sh-radius); }
.custom-view__why { color: var(--sh-graphite); font-size: var(--sh-fs-xs); }
.custom-view__error { border: 2px solid var(--sh-error); color: var(--sh-error); background: var(--sh-paper-2); padding: var(--sh-space-3); border-radius: var(--sh-radius); white-space: pre-wrap; margin-bottom: var(--sh-space-3); }
.custom-view__page :deep(.a2ui-page__title) { display: none; }
</style>
