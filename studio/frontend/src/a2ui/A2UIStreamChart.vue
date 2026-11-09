<script setup lang="ts">
// A2UIStreamChart — the KINETIC "kinetic stream": a ticker of the latest
// `window` values (label, value in time order) as bars, flagged above
// "threshold", with the newest value read out. With "refresh" its sql re-runs
// every N seconds while visible (useChartData), and each tick glides to the
// new bars with the motion preset.
import { computed } from 'vue'
import type { A2UIComponentT } from './types'
import A2UIChartFrame from './A2UIChartFrame.vue'
import { useChartData } from './useChartData'
import { useMotion } from './useMotion'
import { colorClass, isBound } from './query'
import { num } from './series'
import { formatNumber } from '../lib/formatCell'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, table, result } = useChartData(() => props.component)
const points = computed(() => table.value.rows.map((r) => ({ label: String(r[0] ?? ''), value: num(r.length > 1 ? r[1] : r[0]) })))
const { values, replay, enabled } = useMotion(() => points.value.map((p) => p.value), () => props.component.motion)

const threshold = computed(() => (typeof props.component.threshold === 'number' ? props.component.threshold : null))
const maxV = computed(() => Math.max(0, ...points.value.map((p) => p.value), threshold.value ?? 0) || 1)
function h(i: number): number {
  return Math.max(2, Math.min(((values.value[i] ?? 0) / maxV.value) * 100, 100))
}
function hot(i: number): boolean {
  return threshold.value !== null && points.value[i].value > threshold.value
}
const latest = computed(() => points.value[points.value.length - 1])
const live = computed(() => isBound(props.component) && Number(props.component.refresh) >= 5)
const ariaLabel = computed(() => `Stream of ${points.value.length} values; latest ${latest.value ? formatNumber(latest.value.value) : 'none'}`)
</script>

<template>
  <A2UIChartFrame :result="result" :title="component.label || component.id" kind="stream" :label="component.label" :sql="isBound(component)" :loading="loading" :error="error" :empty="points.length === 0" :animated="enabled" @replay="replay">
    <div class="a2ui-kchart__readout">
      <span v-if="live" class="a2ui-stream__live">● LIVE · {{ component.refresh }}s</span>
      <span v-if="latest">{{ latest.label }}</span>
      <span class="a2ui-stream__latest" :class="{ 'a2ui-stream__latest--hot': latest && hot(points.length - 1) }">{{ latest ? formatNumber(latest.value) : '' }}</span>
    </div>
    <div class="a2ui-stream" :class="colorClass(component.color)" role="img" :aria-label="ariaLabel">
      <div
        v-if="threshold !== null" class="a2ui-stream__threshold"
        :style="{ bottom: Math.min((threshold / maxV) * 100, 100) + '%' }"
      ></div>
      <div
        v-for="(p, i) in points" :key="i" class="a2ui-stream__bar" :class="{ 'a2ui-stream__bar--hot': hot(i) }"
        :style="{ height: h(i) + '%' }" :title="`${p.label}: ${formatNumber(p.value)}`"
      ></div>
    </div>
  </A2UIChartFrame>
</template>
