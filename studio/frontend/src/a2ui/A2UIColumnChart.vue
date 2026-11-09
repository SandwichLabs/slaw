<script setup lang="ts">
// A2UIColumnChart — the KINETIC "stagger cascade": vertical columns, one per
// label, each split into its numeric series either stacked or grouped
// ("mode"), optionally sorted by total ("sorted"). Every segment rises with
// the motion preset, staggered column by column. Heights are computed
// percentages bound through a numeric :style, never a model string.
import { computed, nextTick, watch } from 'vue'
import type { A2UIComponentT } from './types'
import { isBound } from './query'
import A2UIChartFrame from './A2UIChartFrame.vue'
import { useChartData } from './useChartData'
import { useMotion } from './useMotion'
import { seriesClass, toSeries } from './series'
import { formatNumber } from '../lib/formatCell'
import { useDrill } from './useDrill'
import { useRoving } from './useRoving'
import { valueScale, frac as scaleFrac, formatTick } from './scale'
import A2UIAnnotations from './A2UIAnnotations.vue'
import { placeOnColumns, type Annotation } from './annotations'
import { useAnnotations, useColumnGeometry } from './useAnnotations'
import { buildColumnAxis } from './columnAxis'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, table, result } = useChartData(() => props.component)
const data = computed(() => toSeries(table.value))
const grouped = computed(() => props.component.mode === 'grouped')

// src: the result row this column was drawn from (sorting reorders columns, not rows).
interface Col { label: string; segs: number[]; total: number; src: number }
const cols = computed<Col[]>(() => {
  const out = data.value.labels.map((label, i) => {
    const segs = data.value.series.map((s) => Math.max(0, s.values[i]))
    return { label, segs, total: segs.reduce((a, b) => a + b, 0), src: i }
  })
  return props.component.sorted ? out.slice().sort((a, b) => b.total - a.total) : out
})
// Drill (data artifact view only): a column is one result row; click, or Enter
// or Space on a focused column, sets the drill parameter from that row's column.
const drill = useDrill(() => props.component, () => (isBound(props.component) && table.value.rows.length ? table.value : null))
const roving = useRoving(() => cols.value.length)

const k = computed(() => data.value.series.length)
const { values, replay, enabled } = useMotion(() => cols.value.flatMap((c) => c.segs), () => props.component.motion)

// Columns are zero-baselined (a column's height is its magnitude). Stacked
// columns scale to the tallest total, grouped bars to the tallest segment, and
// the axis tops out at the next round gridline above that.
const scale = computed(() => {
  const m = grouped.value ? Math.max(0, ...cols.value.flatMap((c) => c.segs)) : Math.max(0, ...cols.value.map((c) => c.total))
  return valueScale([0, m])
})
const fmtTick = (v: number) => formatTick(v, scale.value.step)
const axw = computed(() => Math.max(2, ...scale.value.ticks.map((t) => fmtTick(t).length)) + 1)
function h(ci: number, si: number): number {
  const v = values.value[ci * k.value + si] ?? 0
  return Math.max(0, Math.min(scaleFrac(scale.value, v) * 100, 100))
}
// Where the column's total label sits: just above its own stack (stacked), or
// above its tallest bar (grouped).
function topPct(ci: number): number {
  const hs = cols.value[ci].segs.map((_, si) => h(ci, si))
  return grouped.value ? Math.max(0, ...hs) : Math.min(100, hs.reduce((a, b) => a + b, 0))
}
// Event markers (annotations): on the column centre of the label they name, or, for dated
// columns in order, of the column they fall in. The overlay covers the plot area (the same
// pinned pieces the gridlines use); from the top so the label rows clear the tallest column's value.
// The columns scroll sideways when there are many, so markers sit at the measured centre of
// their column inside the scrolling content (and scroll with it), not at a fraction of what shows.
const { el: colsEl, geom, scrollLeft, measure, onScroll } = useColumnGeometry()
const scrollProp = computed(() => ({ width: geom.value.width, left: scrollLeft.value, view: geom.value.view }))
const place = (items: Annotation[]) => {
  const p = placeOnColumns(items, cols.value.map((c) => c.label), !props.component.sorted)
  const g = geom.value
  return g.width > 0 && g.centers.length === cols.value.length ? { ...p, markers: p.markers.map((m) => ({ ...m, frac: g.centers[m.bucket] / g.width })) } : p
}
const annot = useAnnotations(() => props.component, place, () => geom.value.width)
watch(() => cols.value.map((c) => c.label).join('\0'), () => void nextTick(measure))
// x labels: dated columns in order get time-granularity labels under the column each tick
// falls in (columnAxis.ts); anything else keeps one label per column.
const xaxis = computed(() => buildColumnAxis(cols.value.map((c) => c.label), !props.component.sorted, geom.value.centers, geom.value.width))
const annotInset = { top: '0px', bottom: 'calc(24px + var(--a2ui-col-inset) + var(--a2ui-sbh))', left: '0px', right: '0px' }
const ariaLabel = computed(() => `Column chart, ${grouped.value ? 'grouped' : 'stacked'}, ${cols.value.length} columns of ${data.value.series.map((s) => s.name).join(', ')}${annot.ariaText.value}`)
</script>

<template>
  <A2UIChartFrame :result="result" :title="component.label || component.id"
    kind="columns" :label="component.label" :sql="isBound(component)" :loading="loading && cols.length === 0" :error="error"
    :empty="cols.length === 0 || k === 0" :animated="enabled" :legend="data.series.map((s) => s.name)"
    @replay="replay"
  >
    <div v-if="drill.problem.value" class="a2ui-error" role="alert">{{ drill.problem.value }}</div>
    <div :style="annot.wrapStyle.value">
    <div class="a2ui-colplot" :class="{ 'a2ui-colplot--drill': drill.enabled.value }" :style="{ '--a2ui-axw': axw + 'ch', '--a2ui-sbh': geom.sbh + 'px' }">
    <div class="a2ui-colplot__yaxis" aria-hidden="true">
      <div class="a2ui-colplot__ypad a2ui-colplot__ypad--top"></div>
      <div class="a2ui-colplot__yplot">
        <div v-for="(t, i) in scale.ticks" :key="i" class="a2ui-linechart__axis-y" :style="{ bottom: scaleFrac(scale, t) * 100 + '%', transform: 'translateY(50%)' }">{{ fmtTick(t) }}</div>
      </div>
      <div class="a2ui-colplot__ypad a2ui-colplot__ypad--bottom"></div>
    </div>
    <div class="a2ui-colplot__main">
    <div class="a2ui-colplot__grid" aria-hidden="true">
      <div v-for="(t, i) in scale.ticks" :key="i" class="a2ui-colplot__line" :style="{ bottom: scaleFrac(scale, t) * 100 + '%' }"></div>
    </div>
    <div
      ref="colsEl" class="a2ui-columns" :role="drill.enabled.value ? 'group' : 'img'" :aria-label="ariaLabel"
      @keydown="drill.enabled.value && roving.onKeydown($event)" @focusin="roving.onFocusin" @scroll.passive="onScroll"
    >
      <div
        v-for="(c, ci) in cols" :key="c.label + ci" class="a2ui-columns__col"
        :class="{ 'a2ui-drill-row': drill.enabled.value, 'a2ui-drill-row--current': drill.isCurrent(c.src) }"
        v-bind="drill.enabled.value ? { role: 'button', 'data-roving': '', 'data-roving-i': ci, tabindex: roving.tab(ci), 'aria-current': drill.isCurrent(c.src) ? 'true' : undefined, 'aria-label': `${c.label}: ${formatNumber(c.total)}. ${drill.action(c.src)}` } : {}"
        @click="drill.drill(c.src)"
        @keydown="drill.enabled.value && drill.onKey($event, c.src)"
      >
        <div class="a2ui-columns__headroom" aria-hidden="true"></div>
        <div class="a2ui-columns__track">
          <div class="a2ui-columns__inner" :class="grouped ? 'a2ui-columns__track--grouped' : 'a2ui-columns__track--stacked'">
            <span class="a2ui-columns__total" :style="{ bottom: `calc(${topPct(ci)}% + 2px)` }">{{ formatNumber(c.total) }}</span>
            <div
              v-for="(seg, si) in c.segs" :key="si"
              class="a2ui-columns__seg" :class="seriesClass(si)"
              :style="{ height: h(ci, si) + '%' }"
              :title="`${c.label} · ${data.series[si]?.name}: ${formatNumber(seg)}`"
            ></div>
          </div>
        </div>
        <span
          v-if="!xaxis.time" class="a2ui-columns__label" :title="c.label"
        >{{ c.label }}</span>
        <span v-else class="a2ui-columns__label a2ui-columns__label--tick" :data-anchor="xaxis.anchors[ci] || undefined" :title="xaxis.labels[ci] ? c.label : undefined" :aria-hidden="xaxis.labels[ci] ? undefined : 'true'" data-testid="a2ui-column-tick"><span>{{ xaxis.labels[ci] }}</span></span>
      </div>
    </div>
    <A2UIAnnotations v-if="annot.markers.value.length" :markers="annot.markers.value" :inset="annotInset" :scroll="scrollProp" />
    </div>
    </div>
    </div>
    <p v-if="annot.note.value" class="a2ui-annot__note" role="status" data-testid="a2ui-annotation-note">{{ annot.note.value }}</p>
    <div v-if="isBound(component) && loading" class="a2ui-loading" role="status">RUNNING QUERY…</div>
    <div v-if="drill.error.value" class="a2ui-error" role="alert" data-testid="a2ui-drill-error">{{ drill.error.value }}</div>
  </A2UIChartFrame>
</template>
