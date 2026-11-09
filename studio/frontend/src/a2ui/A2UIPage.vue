<script setup lang="ts">
// A2UIPage — the root of the A2UI renderer (#67). Ported from renderer.js's
// renderPage: builds the parent -> children adjacency map once from the flat
// component list and renders the top-level (parentless) components, each of
// which recurses through A2UIComponent.
//
// The page itself is assumed already validated (internal/a2ui.Validate ran
// server side, in present_data or a notebook cell load) — this component
// does not re-validate, it only walks the shape Validate guarantees: unique
// ids, resolved parents, no cycles.
import { computed, provide } from 'vue'
import A2UIComponent from './A2UIComponent.vue'
import type { A2UIComponentT, A2UIPageT } from './types'
import { A2UI_SOURCE, type A2UISource } from './source'
import './a2ui.css'

const props = defineProps<{ page: A2UIPageT; signal?: AbortSignal; source?: A2UISource }>()

// Optional abort signal (review fix, step 4a #1): a caller that may reload
// or unmount out from under an in-flight live query (NotebookView switching
// notebooks) passes one down so every data-bound leaf component's
// runLiveQuery call can skip applying a result that arrives after the
// caller stopped caring. Provided once here rather than threaded through
// every component's props, since it is optional and A2UIComponent's prop
// surface is otherwise just {component}.
provide('a2uiAbortSignal', props.signal)
// Where this page lives (a notebook cell), so each component's runs say which one asked:
// the receipt of a run names its notebook, cell and component.
provide(A2UI_SOURCE, props.source)

const childMap = computed(() => {
  const map = new Map<string, A2UIComponentT[]>()
  for (const c of props.page?.components ?? []) {
    const key = c.parent ?? ''
    if (!map.has(key)) map.set(key, [])
    map.get(key)!.push(c)
  }
  return map
})
provide('a2uiChildMap', childMap)

const roots = computed(() => childMap.value.get('') ?? [])
</script>

<template>
  <div class="a2ui-page" data-testid="a2ui-page">
    <h2 v-if="page?.title" class="a2ui-page__title">{{ page.title }}</h2>
    <A2UIComponent v-for="c in roots" :key="c.id" :component="c" />
  </div>
</template>
