<script setup lang="ts">
// A2UIRadarChart — the KINETIC "vector polygon": one axis per row, one
// polygon per numeric column (the first solid, the rest dashed ghosts, e.g. a
// baseline), every vertex morphing outward with the motion preset. Scaled to
// "max" when set, else the largest value.
import { computed, ref } from 'vue'
import type { A2UIComponentT } from './types'
import { isBound } from './query'
import A2UIChartFrame from './A2UIChartFrame.vue'
import { useChartData } from './useChartData'
import { useMotion } from './useMotion'
import { polar, seriesClass, toSeries } from './series'
import { formatNumber } from '../lib/formatCell'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, table, result } = useChartData(() => props.component)
const data = computed(() => toSeries(table.value))
const n = computed(() => data.value.labels.length)
const flat = computed(() => data.value.series.flatMap((s) => s.values))
const { values, replay, enabled } = useMotion(() => flat.value, () => props.component.motion)

const SIZE = 240
const C = SIZE / 2
const R = 82
const max = computed(() => {
  const m = Number(props.component.max)
  return Number.isFinite(m) && m > 0 ? m : Math.max(0, ...flat.value) || 1
})

function ring(level: number): string {
  return data.value.labels.map((_, i) => polar(i, n.value, level * max.value, max.value, C, C, R)).map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ')
}
const polys = computed(() =>
  data.value.series.map((s, si) => {
    const pts = s.values.map((_, i) => polar(i, n.value, values.value[si * n.value + i] ?? 0, max.value, C, C, R))
    return { name: s.name, pts, str: pts.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ') }
  }),
)
function axisEnd(i: number) {
  return polar(i, n.value, max.value, max.value, C, C, R)
}
function labelAt(i: number) {
  return polar(i, n.value, max.value, max.value, C, C, R + 16)
}
// Axis labels are HTML over the SVG, placed by percentage, so they stay
// legible however far the SVG scales down in a narrow card.
function labelStyle(i: number): Record<string, string> {
  const p = labelAt(i)
  const dx = Math.abs(p.x - C) < 4 ? '-50%' : p.x < C ? '-100%' : '0'
  return { left: (p.x / SIZE) * 100 + '%', top: (p.y / SIZE) * 100 + '%', transform: `translate(${dx}, -50%)` }
}
const hover = ref<number | null>(null)
const ariaLabel = computed(() => `Radar chart over ${n.value} axes (${data.value.labels.join(', ')}) for ${data.value.series.map((s) => s.name).join(', ')}`)
</script>

<template>
  <A2UIChartFrame :result="result" :title="component.label || component.id"
    kind="radar" :label="component.label" :sql="isBound(component)" :loading="loading" :error="error"
    :empty="n < 3 || data.series.length === 0" :animated="enabled" :legend="data.series.map((s) => s.name)"
    @replay="replay"
  >
    <div class="a2ui-radar">
    <svg :viewBox="`0 0 ${SIZE} ${SIZE}`" class="a2ui-radar__svg" role="img" :aria-label="ariaLabel">
      <polygon v-for="lv in [0.33, 0.66, 1]" :key="lv" :points="ring(lv)" class="a2ui-kchart__grid a2ui-radar__web" />
      <g v-for="(lbl, i) in data.labels" :key="'ax' + i">
        <line :x1="C" :y1="C" :x2="axisEnd(i).x" :y2="axisEnd(i).y" class="a2ui-kchart__grid" />
      </g>
      <g v-for="(p, si) in polys.slice().reverse()" :key="p.name" :class="seriesClass(polys.length - 1 - si)">
        <polygon :points="p.str" class="a2ui-radar__poly" :class="{ 'a2ui-radar__poly--ghost': polys.length - 1 - si > 0 }" />
      </g>
      <g v-if="polys[0]" :class="seriesClass(0)">
        <circle
          v-for="(pt, i) in polys[0].pts" :key="'v' + i" :cx="pt.x" :cy="pt.y" :r="hover === i ? 5 : 3.5"
          class="a2ui-radar__vertex" @mouseenter="hover = i" @mouseleave="hover = null"
        ><title>{{ data.labels[i] }}: {{ data.series.map((s) => `${s.name} ${formatNumber(s.values[i])}`).join(', ') }}</title></circle>
      </g>
    </svg>
    <span v-for="(lbl, i) in data.labels" :key="'lb' + i" class="a2ui-radar__axis" :class="{ 'a2ui-radar__axis--active': hover === i }" :style="labelStyle(i)">{{ lbl }}</span>
    </div>
  </A2UIChartFrame>
</template>
