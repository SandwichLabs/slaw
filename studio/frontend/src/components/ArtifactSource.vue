<script setup lang="ts">
// ArtifactSource — the Source tab of a data artifact (#162, step 6a): artifact.yaml
// and its view file (view.a2ui.json or view.js) in CodeMirror, Save (Ctrl/Cmd+S), Delete, the conflict banner
// and the policy warnings of a saved query. State and actions are
// useArtifactSource; this renders it. The editors show the file text as it is on
// disk, so a broken artifact can be fixed here. Every message below is text
// interpolation.
import { ref, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import Icon from './Icon.vue'
import ConflictBanner from './ConflictBanner.vue'
import { createSlawEditor, type SlawEditorInstance } from '../composables/useSlawEditor'
import { useArtifactSource } from '../composables/useArtifactSource'
import { shortcutLabel } from '../composables/useModal'

const props = defineProps<{ name: string; active: boolean }>()
const emit = defineEmits<{ saved: []; gone: [] }>()

const src = useArtifactSource(() => props.name, { saved: () => emit('saved'), gone: () => emit('gone') })
const { manifest, view, viewFile, loading, loadError, saving, saveError, notice, warnings, dirty, conflict, conflictBusy } = src

type FileTab = 'manifest' | 'view'
const tab = ref<FileTab>('manifest')
const manifestEl = ref<HTMLElement | null>(null)
const viewEl = ref<HTMLElement | null>(null)
const editors: Partial<Record<FileTab, SlawEditorInstance>> = {}

function mount(which: FileTab, el: HTMLElement | null, lang: 'yaml' | 'json' | 'javascript') {
  if (!el || editors[which]) return
  const text = which === 'manifest' ? manifest : view
  editors[which] = createSlawEditor(el, {
    value: text.value,
    lang,
    onChange: (v) => { text.value = v },
    extraKeymap: [{ key: 'Mod-s', run: () => { void src.save(); return true } }],
  })
}
// view.js is JavaScript, view.a2ui.json is JSON: a change of kind remounts the view editor.
const viewLang = (): 'javascript' | 'json' => (viewFile.value === 'view.js' ? 'javascript' : 'json')
let mountedViewLang = viewLang()
onMounted(() => { mount('manifest', manifestEl.value, 'yaml'); mount('view', viewEl.value, viewLang()) })
watch(viewFile, () => {
  if (viewLang() === mountedViewLang) return
  editors.view?.destroy()
  delete editors.view
  mountedViewLang = viewLang()
  mount('view', viewEl.value, mountedViewLang)
})
onBeforeUnmount(() => { for (const e of Object.values(editors)) e?.destroy() })

// A load, reload or resolution replaces the text from outside the editor.
for (const [which, text] of [['manifest', manifest], ['view', view]] as const) {
  watch(text, (v) => { const e = editors[which]; if (e && e.getValue() !== v) e.setValue(v) })
}
// A hidden editor measures nothing: re-measure when its tab or the whole tab shows.
watch([tab, () => props.active], async () => { await nextTick(); editors[tab.value]?.view.requestMeasure() })
watch(() => props.active, (a) => { if (a) void src.checkOnce() }, { immediate: true })
</script>

<template>
  <div class="col artifact-src" data-testid="artifact-source">
    <div class="row artifact-src__bar">
      <div class="seg" role="group" aria-label="Artifact file">
        <button type="button" :class="{ on: tab === 'manifest' }" :aria-pressed="tab === 'manifest'" data-testid="source-tab-manifest" @click="tab = 'manifest'">artifact.yaml</button>
        <button type="button" :class="{ on: tab === 'view' }" :aria-pressed="tab === 'view'" data-testid="source-tab-view" @click="tab = 'view'">{{ viewFile }}</button>
      </div>
      <span v-if="dirty" class="badge badge--warn" data-testid="source-dirty">Unsaved</span>
      <span style="flex: 1;" />
      <button type="button" class="btn btn-ghost btn-sm" style="color: var(--sh-error);" data-testid="source-delete" @click="src.remove()"><Icon name="trash" :size="14" /> Delete</button>
      <button type="button" class="btn btn-primary btn-sm" :disabled="saving || !!conflict || loading" :title="`Save (${shortcutLabel('S')})`" data-testid="source-save" @click="src.save()">
        <Icon name="save" :size="14" /> {{ saving ? 'Saving…' : 'Save' }}
      </button>
    </div>

    <ConflictBanner
      v-if="conflict" kind="artifact" :path="`artifacts/${name}/`" :deleted="conflict.deleted" :busy="conflictBusy"
      @reload="src.resolveConflict('reload')" @overwrite="src.resolveConflict('overwrite')" @save-copy="src.resolveConflict('saveCopy')"
    />
    <div v-if="loadError" class="artifact-src__err" role="alert" data-testid="source-load-error">{{ loadError }}</div>
    <div v-if="saveError" class="artifact-src__err" role="alert" data-testid="source-error"><pre class="artifact-src__msg">{{ saveError }}</pre></div>
    <div v-if="notice" class="sh-mono artifact-src__notice" role="status" data-testid="source-notice">{{ notice }}</div>
    <div v-if="warnings.length" class="card card-pad col artifact-src__warn" role="status" data-testid="source-warnings">
      <strong>The read-only SQL policy refuses {{ warnings.length === 1 ? 'a query' : 'some queries' }}.</strong>
      <span class="sh-mono artifact-src__hint">When the view runs it shows why the policy refused this. Fix the query and save again.</span>
      <ul class="artifact-src__list"><li v-for="w in warnings" :key="w"><pre class="artifact-src__msg">{{ w }}</pre></li></ul>
    </div>

    <div class="artifact-src__editors">
      <div v-show="tab === 'manifest'" ref="manifestEl" class="artifact-src__editor" data-testid="source-editor-manifest" />
      <div v-show="tab === 'view'" ref="viewEl" class="artifact-src__editor" data-testid="source-editor-view" />
    </div>
  </div>
</template>

<style scoped>
.artifact-src { gap: var(--sh-space-3); height: 100%; min-height: 0; }
.artifact-src__bar { align-items: center; gap: var(--sh-space-3); }
.artifact-src__editors { flex: 1; min-height: 320px; border: var(--sh-border); border-radius: var(--sh-radius); overflow: hidden; display: flex; flex-direction: column; }
.artifact-src__editor { flex: 1; min-height: 0; overflow: hidden; }
.artifact-src__editor :deep(.cm-editor) { height: 100%; }
.artifact-src__err { border: 2px solid var(--sh-error); background: var(--sh-paper-2); color: var(--sh-error); padding: var(--sh-space-3); border-radius: var(--sh-radius); }
.artifact-src__msg { white-space: pre-wrap; margin: 0; font-family: var(--sh-font-mono); font-size: var(--sh-fs-xs); }
.artifact-src__hint { font-size: var(--sh-fs-xs); color: var(--sh-graphite); }
.artifact-src__notice { font-size: var(--sh-fs-xs); color: var(--sh-graphite); }
.artifact-src__warn { gap: var(--sh-space-2); border-color: var(--sh-warn); }
.artifact-src__list { margin: 0; padding-left: var(--sh-space-4); }
</style>
