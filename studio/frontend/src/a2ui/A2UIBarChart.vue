<script setup lang="ts">
// A2UIBarChart — zero-baselined horizontal bars: inline "bars", "series"-less
// "columns" + "rows", or a live "sql" binding. Ported from renderer.js's
// renderBarChart/appendBars.
//
// Several series (a2ui/multiseries.ts, long or wide) draw as grouped bars: one
// group per label, one bar per series in the theme's chart colours by series
// index, all on one shared value scale. A series with no value at a label has
// no bar there. More than six series fold into "Other". Drill-down is off for
// grouped bars (a bar is no longer one result row).
//
// Zero-baselined on purpose: bar length encodes magnitude, and starting the
// axis at the smallest value would make a 233-vs-208 gap look like 10x. A
// negative value draws leftward from the zero line. The value axis along the
// bottom and the faint gridlines in each track use the shared round ticks
// (a2ui/scale.ts). Bar offsets and widths are plain numbers (clamped
// percentages) bound only through numeric :style — never a raw model string.
// Bars grow out of the zero line with the component's "motion" preset.
import { computed } from 'vue'
import type { A2UIComponentT } from './types'
import { colorClass, isBound } from './query'
import { useMultiSeries } from './useMultiSeries'
import { formatNumber } from '../lib/formatCell'
import { useMotion } from './useMotion'
import { useDrill } from './useDrill'
import { useRoving } from './useRoving'
import { valueScale, frac as scaleFrac, formatTick } from './scale'
import { tickAnchor } from './xaxis'
import { seriesClass } from './series'
import A2UISeriesLegend from './A2UISeriesLegend.vue'
import A2UIEvidence from './A2UIEvidence.vue'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, data, ms } = useMultiSeries(() => props.component, 'bars')
const multi = computed(() => ms.value.series.length > 1)
const labels = computed(() => ms.value.labels)
// One row of numbers per bar, label-major: a single series is one bar per label.
const values = computed(() => ms.value.series.flatMap((s) => s.values.map((v) => v ?? 0)))
const scale = computed(() => valueScale(values.value))
const zeroPct = computed(() => scaleFrac(scale.value, 0) * 100)
// Animate from zero, so a bar grows out of the zero line.
const { values: anim } = useMotion(() => values.value, () => props.component.motion)

const fmtTick = (v: number) => formatTick(v, scale.value.step)
const tickPcts = computed(() => scale.value.ticks.map((t) => ({ pct: scaleFrac(scale.value, t) * 100, label: fmtTick(t), zero: t === 0 })))
// One shared value-column width, so every track is the same length.
const valueCh = computed(() => Math.max(4, ...values.value.map((v) => formatNumber(v).length)) + 1)

// Drill (data artifact view only): a bar is one result row; click, or Enter
// or Space on a focused bar, sets the drill parameter from that row's column.
// A grouped bar, or a result whose repeated rows were summed, is not one result
// row per bar: drill is off, and a visible note says so.
const drillOff = computed(() => multi.value || ms.value.merged)
const drill = useDrill(() => props.component, () => (isBound(props.component) && data.value && !drillOff.value ? data.value : null))
const drillNote = computed(() =>
  drill.column.value && drillOff.value
    ? `Drill-down is off: ${multi.value ? 'a grouped bar is' : 'bars of summed rows are'} not one result row.`
    : '',
)
const roving = useRoving(() => labels.value.length)

// The bar for series si at label i spans zero to the value; at least 1% wide so a tiny value is visible.
function barStyle(si: number, i: number): { left: string; width: string } {
  const k = si * labels.value.length + i
  const end = Math.min(Math.max(scaleFrac(scale.value, anim.value[k] ?? values.value[k]) * 100, 0), 100)
  const lo = Math.min(end, zeroPct.value)
  const w = Math.min(Math.max(Math.abs(end - zeroPct.value), 1), 100)
  return { left: Math.min(lo, 100 - w) + '%', width: w + '%' }
}
const valueAt = (si: number, i: number): number | null => ms.value.series[si].values[i]
const textAt = (si: number, i: number) => (valueAt(si, i) === null ? '–' : formatNumber(valueAt(si, i) as number))
const groupText = (i: number) => `${labels.value[i]}: ${ms.value.series.map((s, si) => `${s.name} ${textAt(si, i)}`).join(', ')}`
</script>

<template>
  <div class="a2ui-barchart" @keydown="drill.enabled.value && roving.onKeydown($event)" @focusin="roving.onFocusin">
    <div v-if="component.label || data?.receipt_id || data?.receipt_note" class="a2ui-component__title">{{ component.label }}<A2UIEvidence :result="data" :title="component.label || component.id" /></div>
    <div v-if="isBound(component) && loading && !data" class="a2ui-loading">RUNNING QUERY…</div>
    <div v-else-if="isBound(component) && error" class="a2ui-error">QUERY ERROR: {{ error }}</div>
    <div v-else-if="labels.length === 0 || ms.series.length === 0" class="a2ui-empty">NO ROWS</div>
    <template v-else>
      <div v-if="drill.problem.value" class="a2ui-error" role="alert">{{ drill.problem.value }}</div>
      <div v-if="drillNote" class="a2ui-kchart__note" role="note" data-testid="a2ui-drill-off">{{ drillNote }}</div>
      <template v-if="multi">
        <div v-for="(label, i) in labels" :key="i" class="a2ui-bargroup" role="group" :aria-label="groupText(i)" :title="groupText(i)" data-testid="a2ui-bargroup">
          <span class="a2ui-bar__label">{{ label }}</span>
          <div class="a2ui-bargroup__bars">
            <div v-for="(s, si) in ms.series" :key="si" class="a2ui-bar a2ui-bar--grouped" :class="seriesClass(si)">
              <div class="a2ui-bar__track">
                <span v-for="(t, ti) in tickPcts" :key="ti" class="a2ui-bar__grid" :class="{ 'a2ui-bar__grid--zero': t.zero && scale.min < 0 }" :style="{ left: t.pct + '%' }" aria-hidden="true"></span>
                <div v-if="valueAt(si, i) !== null" class="a2ui-bar__fill a2ui-bar__fill--series" :style="barStyle(si, i)" :data-series="s.name"></div>
              </div>
              <span class="a2ui-bar__value" :style="{ minWidth: valueCh + 'ch' }">{{ textAt(si, i) }}</span>
            </div>
          </div>
        </div>
      </template>
      <template v-else>
      <div
        v-for="(label, i) in labels" :key="i" class="a2ui-bar" :title="`${label}: ${formatNumber(values[i])}`"
        :class="{ 'a2ui-drill-row': drill.enabled.value, 'a2ui-drill-row--current': drill.isCurrent(i) }"
        v-bind="drill.enabled.value ? { role: 'button', 'data-roving': '', 'data-roving-i': i, tabindex: roving.tab(i), 'aria-current': drill.isCurrent(i) ? 'true' : undefined, 'aria-label': `${label}: ${formatNumber(values[i])}. ${drill.action(i)}` } : {}"
        @click="drill.drill(i)"
        @keydown="drill.enabled.value && drill.onKey($event, i)"
      >
        <span class="a2ui-bar__label">{{ label }}</span>
        <div class="a2ui-bar__track">
          <span v-for="(t, ti) in tickPcts" :key="ti" class="a2ui-bar__grid" :class="{ 'a2ui-bar__grid--zero': t.zero && scale.min < 0 }" :style="{ left: t.pct + '%' }" aria-hidden="true"></span>
          <div class="a2ui-bar__fill" :class="colorClass(component.color)" :style="barStyle(0, i)"></div>
        </div>
        <span class="a2ui-bar__value" :style="{ minWidth: valueCh + 'ch' }">{{ formatNumber(values[i]) }}</span>
      </div>
      </template>
      <div class="a2ui-baraxis" aria-hidden="true">
        <span class="a2ui-baraxis__label"></span>
        <div class="a2ui-bar__axis">
          <span v-for="(t, ti) in tickPcts" :key="ti" class="a2ui-linechart__axis-x" :style="tickAnchor(t.pct / 100)">{{ t.label }}</span>
        </div>
        <span class="a2ui-baraxis__value" :style="{ minWidth: valueCh + 'ch' }"></span>
      </div>
      <A2UISeriesLegend :names="ms.series.map((s) => s.name)" :folded="ms.folded" :mode="ms.foldMode" />
      <div v-if="isBound(component) && loading" class="a2ui-loading" role="status">RUNNING QUERY…</div>
      <div v-if="drill.error.value" class="a2ui-error" role="alert" data-testid="a2ui-drill-error">{{ drill.error.value }}</div>
    </template>
  </div>
</template>
