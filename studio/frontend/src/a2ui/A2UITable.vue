<script setup lang="ts">
// A2UITable — inline rows/columns, or a live "sql" binding. Ported from
// renderer.js's renderTable/buildTable.
import { computed } from 'vue'
import type { A2UIComponentT } from './types'
import { isBound } from './query'
import { useLiveQuery } from './useLiveQuery'
import { formatCell, cellClass, isNumericType } from '../lib/formatCell'
import { useDrill } from './useDrill'
import { useRoving } from './useRoving'
import A2UIEvidence from './A2UIEvidence.vue'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, data } = useLiveQuery(() => props.component)

const columns = computed<string[]>(() => (isBound(props.component) ? (data.value?.columns ?? []) : (props.component.columns ?? [])))
const rows = computed<any[][]>(() => (isBound(props.component) ? (data.value?.rows ?? []) : (props.component.rows ?? [])))
// types is client-only (§4b): live queries get it from RunA2UIQuery; inline
// (model-authored) data has no column type, and synthesized notebook pages
// stamp it onto the component before render (never saved, never validated —
// see a2ui/types.ts).
const types = computed<string[]>(() => (isBound(props.component) ? (data.value?.types ?? []) : (props.component.types ?? [])))

// Drill (data artifact view only): a click on a row, or Enter on its focused
// first cell, sets the drill parameter from that row's column.
const drill = useDrill(() => props.component, () => (isBound(props.component) && data.value ? data.value : null))
const roving = useRoving(() => rows.value.length)

// One formatCell call per cell: precomputed once here rather than called
// 3x per cell (class/title/text) from the template.
const formattedRows = computed(() =>
  rows.value.map((row) => row.map((cell, ci) => formatCell(cell, types.value[ci], { context: 'report' }))),
)
</script>

<template>
  <div class="a2ui-tablewrap">
    <div v-if="component.label || data?.receipt_id || data?.receipt_note" class="a2ui-component__title">{{ component.label }}<A2UIEvidence :result="data" :title="component.label || component.id" /></div>
    <div v-if="isBound(component) && loading && !data" class="a2ui-loading">RUNNING QUERY…</div>
    <div v-else-if="isBound(component) && error" class="a2ui-error">QUERY ERROR: {{ error }}</div>
    <div v-else-if="rows.length === 0" class="a2ui-empty">NO ROWS</div>
    <template v-else>
      <div v-if="drill.problem.value" class="a2ui-error" role="alert">{{ drill.problem.value }}</div>
      <table class="a2ui-table" @keydown="drill.enabled.value && roving.onKeydown($event)" @focusin="roving.onFocusin">
        <thead>
          <tr>
            <th v-for="(c, i) in columns" :key="i" :class="{ 'cell-num': isNumericType(types[i]) }">{{ c }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(row, ri) in formattedRows" :key="ri"
            :class="{ 'a2ui-drill-row': drill.enabled.value, 'a2ui-drill-row--current': drill.isCurrent(ri) }"
            @click="drill.drill(ri)"
          >
            <td
              v-for="(fc, ci) in row"
              :key="ci"
              :class="cellClass(fc, types[ci])"
              :title="fc.title"
            >
              <button
                v-if="drill.enabled.value && ci === 0" type="button" class="a2ui-drill a2ui-drill--cell"
                :aria-current="drill.isCurrent(ri) ? 'true' : undefined" :aria-label="`${fc.text}. ${drill.action(ri)}`"
                data-roving :data-roving-i="ri" :tabindex="roving.tab(ri)"
              >{{ fc.text }}</button>
              <template v-else>{{ fc.text }}</template>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="isBound(component) && loading" class="a2ui-loading" role="status">RUNNING QUERY…</div>
      <div v-if="drill.error.value" class="a2ui-error" role="alert" data-testid="a2ui-drill-error">{{ drill.error.value }}</div>
    </template>
  </div>
</template>
