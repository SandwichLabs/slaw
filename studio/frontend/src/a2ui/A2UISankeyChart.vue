<script setup lang="ts">
// A2UISankeyChart — the KINETIC "Bézier conduit": sources on the left,
// targets on the right, one ribbon per (source, target, weight) row whose
// thickness grows in with the motion preset. Packets (a dashed overlay) flow
// along each ribbon at the chart's speed unless the person prefers reduced
// motion. Hovering a ribbon isolates it.
import { computed, ref } from 'vue'
import type { A2UIComponentT } from './types'
import { isBound } from './query'
import A2UIChartFrame from './A2UIChartFrame.vue'
import { useChartData } from './useChartData'
import { useMotion } from './useMotion'
import { resolveMotion } from './motion'
import { seriesClass, toFlows } from './series'
import { formatNumber } from '../lib/formatCell'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, table, result } = useChartData(() => props.component)
const data = computed(() => toFlows(table.value))
const { values, replay, enabled } = useMotion(() => data.value.flows.map((f) => f.weight), () => props.component.motion)

// Node labels are HTML beside the SVG (positioned by percentage of its
// height), not SVG <text>: scaled into a narrow card, 11-unit SVG text
// shrinks to a few pixels (the line chart's axis labels hit the same thing).
// The SVG stretches horizontally (preserveAspectRatio="none") and its height
// is H real pixels, so node and label positions line up 1:1 vertically.
const W = 300
const PAD = 12
const GAP = 10
const NODE_W = 8
const LEFT = NODE_W
const RIGHT = W - NODE_W
const H = computed(() => Math.max(120, Math.max(data.value.sources.length, data.value.targets.length) * 40))
function topPct(box: { y: number; h: number }): string {
  return ((box.y + box.h / 2) / H.value) * 100 + '%'
}
const total = computed(() => data.value.flows.reduce((a, f) => a + f.weight, 0) || 1)

// Node heights are proportional to their total weight, sharing the column's height.
function layout(nodes: { total: number }[]) {
  const usable = H.value - PAD * 2 - GAP * Math.max(0, nodes.length - 1)
  let y = PAD
  return nodes.map((nd) => {
    const h = Math.max(4, (nd.total / total.value) * usable)
    const box = { y, h }
    y += h + GAP
    return box
  })
}
const srcBoxes = computed(() => layout(data.value.sources))
const tgtBoxes = computed(() => layout(data.value.targets))

const ribbons = computed(() => {
  const usable = H.value - PAD * 2 - GAP * Math.max(0, Math.max(data.value.sources.length, data.value.targets.length) - 1)
  const sOff = data.value.sources.map(() => 0)
  const tOff = data.value.targets.map(() => 0)
  return data.value.flows.map((f, i) => {
    const full = (f.weight / total.value) * usable
    const thick = Math.max(1.5, ((values.value[i] ?? 0) / total.value) * usable)
    const sy = srcBoxes.value[f.from].y + sOff[f.from] + full / 2
    const ty = tgtBoxes.value[f.to].y + tOff[f.to] + full / 2
    sOff[f.from] += full
    tOff[f.to] += full
    const c1 = LEFT + (RIGHT - LEFT) * 0.45
    const c2 = LEFT + (RIGHT - LEFT) * 0.55
    const d = `M ${LEFT} ${sy.toFixed(1)} C ${c1.toFixed(1)} ${sy.toFixed(1)}, ${c2.toFixed(1)} ${ty.toFixed(1)}, ${RIGHT} ${ty.toFixed(1)}`
    return { ...f, d, thick, i }
  })
})
const hover = ref<number | null>(null)
// Seconds per packet loop, from the validated (and clamped) speed: a number, never a model string.
const flowStyle = computed(() => ({ '--a2ui-flow-dur': (1.4 / resolveMotion(props.component.motion).speed).toFixed(2) + 's' }))
const ariaLabel = computed(() => `Flow chart of ${data.value.flows.length} flows from ${data.value.sources.map((s) => s.name).join(', ')} to ${data.value.targets.map((t) => t.name).join(', ')}`)
</script>

<template>
  <A2UIChartFrame :result="result" :title="component.label || component.id" kind="sankey" :label="component.label" :sql="isBound(component)" :loading="loading" :error="error" :empty="data.flows.length === 0" :animated="enabled" @replay="replay">
    <div class="a2ui-sankey__wrap" :style="{ height: H + 'px' }">
      <div class="a2ui-sankey__col a2ui-sankey__col--src">
        <span v-for="(s, i) in data.sources" :key="'s' + i" class="a2ui-sankey__label" :style="{ top: topPct(srcBoxes[i]) }">{{ s.name }} <b>{{ formatNumber(s.total) }}</b></span>
      </div>
      <svg :viewBox="`0 0 ${W} ${H}`" preserveAspectRatio="none" class="a2ui-sankey" :class="{ 'a2ui-sankey--flowing': enabled }" :style="flowStyle" role="img" :aria-label="ariaLabel">
        <g
          v-for="r in ribbons" :key="r.i" :class="[seriesClass(r.from), { 'a2ui-sankey__ribbon--dim': hover !== null && hover !== r.i, 'a2ui-sankey__ribbon--hot': hover === r.i }]"
          class="a2ui-sankey__ribbon" @mouseenter="hover = r.i" @mouseleave="hover = null"
        >
          <path :d="r.d" :stroke-width="r.thick" class="a2ui-sankey__band" />
          <path v-if="r.thick > 3" :d="r.d" :stroke-width="Math.max(1.5, r.thick * 0.28)" class="a2ui-sankey__packets" />
          <title>{{ data.sources[r.from].name }} → {{ data.targets[r.to].name }}: {{ formatNumber(r.weight) }}</title>
        </g>
        <rect v-for="(s, i) in data.sources" :key="'s' + i" :class="seriesClass(i)" x="0" :y="srcBoxes[i].y" :width="NODE_W" :height="srcBoxes[i].h" rx="3" class="a2ui-sankey__node" />
        <rect v-for="(t, i) in data.targets" :key="'t' + i" :class="seriesClass(i + 1)" :x="RIGHT" :y="tgtBoxes[i].y" :width="NODE_W" :height="tgtBoxes[i].h" rx="3" class="a2ui-sankey__node" />
      </svg>
      <div class="a2ui-sankey__col a2ui-sankey__col--tgt">
        <span v-for="(t, i) in data.targets" :key="'t' + i" class="a2ui-sankey__label" :style="{ top: topPct(tgtBoxes[i]) }">{{ t.name }} <b>{{ formatNumber(t.total) }}</b></span>
      </div>
    </div>
    <div class="a2ui-kchart__readout" aria-live="polite">
      <template v-if="hover !== null && ribbons[hover]">{{ data.sources[ribbons[hover].from].name }} → {{ data.targets[ribbons[hover].to].name }}: {{ formatNumber(ribbons[hover].weight) }}</template>
      <template v-else>{{ formatNumber(total) }} total · hover a ribbon to isolate it</template>
    </div>
  </A2UIChartFrame>
</template>
