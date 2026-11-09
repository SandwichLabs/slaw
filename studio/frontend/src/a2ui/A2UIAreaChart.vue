<script setup lang="ts">
// A2UIAreaChart — the KINETIC "morphing spline": one smooth Catmull-Rom area
// per numeric column over a shared label axis, every point animated with the
// chart's motion preset. The axes are the line chart's (a2ui/scale.ts,
// a2ui/xaxis.ts): round gridlines, a zero baseline unless `zero: false`, and a
// real time axis for date labels. Hover the plot, or focus it and use the
// arrow keys, to read every series at a label.
import { computed, ref } from 'vue'
import type { A2UIComponentT } from './types'
import { isBound } from './query'
import A2UIChartFrame from './A2UIChartFrame.vue'
import A2UIPlotFrame from './A2UIPlotFrame.vue'
import { useChartData } from './useChartData'
import { useMotion } from './useMotion'
import { areaPath, seriesClass, splinePath, toSeries, type XY } from './series'
import { formatNumber } from '../lib/formatCell'
import { valueScale, frac as scaleFrac, formatTick } from './scale'
import { buildXAxis } from './xaxis'
import { useChartCursor } from './useChartCursor'
import A2UIAnnotations from './A2UIAnnotations.vue'
import { placeOnAxis } from './annotations'
import { useAnnotations } from './useAnnotations'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, table, result } = useChartData(() => props.component)
const data = computed(() => toSeries(table.value))
const flat = computed(() => data.value.series.flatMap((s) => s.values))
const { values, replay, enabled } = useMotion(() => flat.value, () => props.component.motion)

const W = 560
const H = 200
const PAD = 16
const innerW = W - PAD * 2
const innerH = H - PAD * 2
const scale = computed(() => valueScale(flat.value, { zero: props.component.zero !== false }))
const n = computed(() => data.value.labels.length)
const plotWidthPx = ref(0)
const xa = computed(() => buildXAxis(data.value.labels, plotWidthPx.value, W))
// event markers (annotations), placed on the time or category axis
const annot = useAnnotations(() => props.component, (items) => placeOnAxis(items, data.value.labels, xa.value), () => (plotWidthPx.value * innerW) / W)
const annotInset = { top: (PAD / H) * 100 + '%', bottom: (PAD / H) * 100 + '%', left: (PAD / W) * 100 + '%', right: (PAD / W) * 100 + '%' }

function x(i: number): number {
  return PAD + xa.value.frac[i] * innerW
}
function y(v: number): number {
  return PAD + innerH - scaleFrac(scale.value, v) * innerH
}
const baseline = computed(() => y(Math.max(scale.value.min, Math.min(0, scale.value.max))))

const fmtTick = (v: number) => formatTick(v, scale.value.step)
const yTicks = computed(() => scale.value.ticks.map((t) => ({ pct: (y(t) / H) * 100, label: fmtTick(t) })))
const xTicks = computed(() => xa.value.ticks.map((t) => ({ frac: (PAD + t.frac * innerW) / W, label: t.label })))
const axw = computed(() => Math.max(2, ...scale.value.ticks.map((t) => fmtTick(t).length)) + 1)

const curves = computed(() =>
  data.value.series.map((s, si) => {
    // left to right, so a time axis with out-of-order rows still draws a function
    const pts: XY[] = xa.value.order.map((i) => ({ x: x(i), y: y(values.value[si * n.value + i] ?? 0) }))
    return { name: s.name, line: splinePath(pts), area: areaPath(pts, baseline.value), pts, idx: xa.value.order }
  }),
)

const orderedFracs = computed(() => xa.value.order.map((i) => xa.value.frac[i]))
const cursor = useChartCursor(() => orderedFracs.value, { W, padL: PAD, innerW })
const lastIdx = computed(() => xa.value.order[n.value - 1] ?? 0)
const active = computed(() => (cursor.active.value === null ? lastIdx.value : (xa.value.order[cursor.active.value] ?? lastIdx.value)))
const hover = computed(() => (cursor.active.value === null ? null : active.value))
const ariaLabel = computed(
  () => `Area chart of ${data.value.series.map((s) => s.name).join(', ')} over ${n.value} labels, ranging ${formatNumber(Math.min(...flat.value))} to ${formatNumber(Math.max(...flat.value))}${annot.ariaText.value}`,
)
</script>

<template>
  <A2UIChartFrame :result="result" :title="component.label || component.id"
    kind="area" :label="component.label" :sql="isBound(component)" :loading="loading" :error="error"
    :empty="n === 0 || data.series.length === 0" :animated="enabled" :legend="data.series.map((s) => s.name)"
    @replay="replay"
  >
    <div class="a2ui-kchart__readout" aria-live="polite">
      <span>{{ data.labels[active] }}</span>
      <span v-for="(s, si) in data.series" :key="si" class="a2ui-kchart__readval" :class="seriesClass(si)">{{ formatNumber(s.values[active]) }}</span>
      <span v-for="(note, k) in annot.notesAt(hover)" :key="'note' + k" class="a2ui-linechart__tip-note" data-testid="a2ui-tip-annotation">&#9656; {{ note }}</span>
    </div>
    <div :style="annot.wrapStyle.value">
    <A2UIPlotFrame
      :y-ticks="yTicks" :x-ticks="xTicks" :axw="axw"
      tabindex="0" role="group" aria-label="Chart values. Use the left and right arrow keys to read each label."
      @measure="plotWidthPx = $event"
      @mousemove="cursor.onMove" @mouseleave="cursor.onLeave" @keydown="cursor.onKey" @focus="cursor.onFocus" @blur="cursor.clear"
    >
      <svg :viewBox="`0 0 ${W} ${H}`" class="a2ui-kchart__svg" role="img" :aria-label="ariaLabel">
        <line
          v-for="(t, i) in scale.ticks" :key="'g' + i" :x1="PAD" :x2="W - PAD" :y1="y(t)" :y2="y(t)"
          class="a2ui-kchart__grid" :class="{ 'a2ui-kchart__grid--zero': t === 0 && scale.min < 0 }"
        />
        <line v-if="hover !== null" :x1="x(hover)" :x2="x(hover)" :y1="PAD" :y2="H - PAD" class="a2ui-kchart__cursor" />
        <g v-for="(c, si) in curves" :key="si" :class="seriesClass(si)">
          <path :d="c.area" class="a2ui-area__fill" />
          <path :d="c.line" class="a2ui-area__line" :class="{ 'a2ui-area__line--secondary': si > 0 }" />
          <circle v-for="(p, k) in c.pts" :key="k" :cx="p.x" :cy="p.y" :r="hover === c.idx[k] ? 5 : 3" class="a2ui-area__dot" />
        </g>
      </svg>
      <A2UIAnnotations v-if="annot.markers.value.length" :markers="annot.markers.value" :inset="annotInset" />
    </A2UIPlotFrame>
    </div>
    <p v-if="annot.note.value" class="a2ui-annot__note" role="status" data-testid="a2ui-annotation-note">{{ annot.note.value }}</p>
  </A2UIChartFrame>
</template>
