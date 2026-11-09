<script setup lang="ts">
// A2UIHeatmap — the KINETIC "quantized matrix": a grid of (row, column, value)
// cells, each quantized into one of four heat levels (class-based colors from
// the chart palette) and scaled up from its value with the motion preset,
// staggered cell by cell. Hover or focus a cell for its readout.
import { computed, ref } from 'vue'
import type { A2UIComponentT } from './types'
import { isBound } from './query'
import A2UIChartFrame from './A2UIChartFrame.vue'
import { useChartData } from './useChartData'
import { useMotion } from './useMotion'
import { heatLevel, toMatrix } from './series'
import { formatNumber } from '../lib/formatCell'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, table, result } = useChartData(() => props.component)
const m = computed(() => toMatrix(table.value))
const ncols = computed(() => m.value.colLabels.length)
const flat = computed(() => m.value.cells.flat().map((v) => v ?? 0))
const { values, replay, enabled } = useMotion(() => flat.value, () => props.component.motion)

function cell(r: number, c: number): number | null {
  return m.value.cells[r][c]
}
function shown(r: number, c: number): string {
  const v = cell(r, c)
  return v === null ? '' : formatNumber(v)
}
function level(r: number, c: number): number {
  return heatLevel(cell(r, c) ?? 0, m.value.min, m.value.max)
}
// Cell fill scale 0.55..1 by value, from the animated frame.
function scale(r: number, c: number): number {
  const span = m.value.max - m.value.min || 1
  const v = values.value[r * ncols.value + c] ?? 0
  const t = Math.max(0, Math.min((v - m.value.min) / span, 1))
  return 0.55 + t * 0.45
}
const gridStyle = computed(() => ({ gridTemplateColumns: `minmax(48px, max-content) repeat(${ncols.value}, minmax(28px, 1fr))` }))
const hover = ref<[number, number] | null>(null)
const ariaLabel = computed(
  () => `Heatmap of ${m.value.rowLabels.length} rows by ${ncols.value} columns, ranging ${formatNumber(m.value.min)} to ${formatNumber(m.value.max)}`,
)
const readout = computed(() => {
  if (!hover.value) return `peak ${formatNumber(m.value.max)}`
  const [r, c] = hover.value
  const v = cell(r, c)
  return `${m.value.rowLabels[r]} · ${m.value.colLabels[c]}: ${v === null ? '—' : formatNumber(v)}`
})
</script>

<template>
  <A2UIChartFrame :result="result" :title="component.label || component.id" kind="heatmap" :label="component.label" :sql="isBound(component)" :loading="loading" :error="error" :empty="m.rowLabels.length === 0" :animated="enabled" @replay="replay">
    <div class="a2ui-kchart__readout" aria-live="polite">{{ readout }}</div>
    <div class="a2ui-heatmap" :style="gridStyle" role="img" :aria-label="ariaLabel">
      <template v-for="(rl, r) in m.rowLabels" :key="'r' + r">
        <span class="a2ui-heatmap__rowlabel">{{ rl }}</span>
        <span
          v-for="(cl, c) in m.colLabels" :key="'c' + c"
          class="a2ui-heatmap__cell" :class="cell(r, c) === null ? 'a2ui-heatmap__cell--empty' : `a2ui-heat-${level(r, c)}`"
          :title="`${rl} · ${cl}: ${shown(r, c) || 'no value'}`"
          @mouseenter="hover = [r, c]" @mouseleave="hover = null"
        >
          <span v-if="cell(r, c) !== null" class="a2ui-heatmap__fill" :style="{ transform: `scale(${scale(r, c).toFixed(3)})` }"></span>
          <span class="a2ui-heatmap__val">{{ shown(r, c) }}</span>
        </span>
      </template>
      <span></span>
      <span v-for="(cl, c) in m.colLabels" :key="'h' + c" class="a2ui-heatmap__collabel">{{ cl }}</span>
    </div>
  </A2UIChartFrame>
</template>
