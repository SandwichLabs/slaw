<script setup lang="ts">
// A2UIStat — a single labeled number, optionally live via "sql" (the query's
// first column of its first row). Ported from renderer.js's renderStat.
import { computed } from 'vue'
import type { A2UIComponentT } from './types'
import { colorClass, isBound } from './query'
import { useLiveQuery } from './useLiveQuery'
import { formatCell } from '../lib/formatCell'
import A2UIEvidence from './A2UIEvidence.vue'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, fetched, data } = useLiveQuery(() => props.component)
const liveValue = computed(() => {
  const first = data.value?.rows[0]
  if (!first) return data.value ? null : undefined
  return first[0] ?? (data.value!.columns.length ? first[0] : undefined)
})
const liveType = computed(() => data.value?.types[0])

// component.types[0] is the client-only type Studio's sql-cell
// synthesizer stamps on a synthesized stat component — never part of the
// A2UI schema, never saved (see a2ui/types.ts).
const displayValue = computed(() => {
  if (isBound(props.component)) {
    if (loading.value) return '…'
    if (error.value) return '!'
    if (!fetched.value) return ''
    if (liveValue.value === null || liveValue.value === undefined) return '—'
    return formatCell(liveValue.value, liveType.value, { context: 'report' }).text
  }
  return props.component.value !== undefined
    ? formatCell(props.component.value, props.component.types?.[0], { context: 'report' }).text
    : ''
})
</script>

<template>
  <div class="a2ui-stat" :class="colorClass(component.color)">
    <div class="a2ui-stat__label">{{ component.label }}<A2UIEvidence :result="data" :title="component.label || component.id" /></div>
    <div class="a2ui-stat__value">{{ displayValue }}</div>
    <div v-if="component.detail" class="a2ui-stat__detail">{{ component.detail }}</div>
    <div v-if="isBound(component) && error" class="a2ui-error">QUERY ERROR: {{ error }}</div>
  </div>
</template>
