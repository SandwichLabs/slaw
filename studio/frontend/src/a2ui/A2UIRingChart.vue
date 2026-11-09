<script setup lang="ts">
// A2UIRingChart — the KINETIC "orbital gauge": up to six concentric progress
// rings (label, value[, max] per row), each arc swept in with stroke-dashoffset
// and a tip marker riding its end. The legend doubles as the readout; hovering
// or focusing a row (or its ring) brings it forward.
import { computed, ref } from 'vue'
import type { A2UIComponentT } from './types'
import { isBound } from './query'
import A2UIChartFrame from './A2UIChartFrame.vue'
import { useChartData } from './useChartData'
import { useMotion } from './useMotion'
import { seriesClass, toRings } from './series'
import { formatNumber } from '../lib/formatCell'

const props = defineProps<{ component: A2UIComponentT }>()
const { loading, error, table, result } = useChartData(() => props.component)
const rings = computed(() => toRings(table.value, props.component.max))
// Animate each ring's fill fraction, not its raw value, so mixed maxes share one sweep.
const fractions = computed(() => rings.value.map((r) => Math.max(0, Math.min(r.value / r.max, 1))))
const { values, replay, enabled } = useMotion(() => fractions.value, () => props.component.motion)

const SIZE = 200
const C = SIZE / 2
const STROKE = 10
function radius(i: number): number {
  return C - STROKE - i * (STROKE + 6)
}
function circ(i: number): number {
  return 2 * Math.PI * radius(i)
}
function frac(i: number): number {
  return Math.max(0, Math.min(values.value[i] ?? 0, 1.08))
}
function tip(i: number) {
  const a = frac(i) * 2 * Math.PI - Math.PI / 2
  return { x: C + radius(i) * Math.cos(a), y: C + radius(i) * Math.sin(a) }
}

const selected = ref(0)
const ariaLabel = computed(() => 'Ring chart: ' + rings.value.map((r) => `${r.label} ${formatNumber(r.value)} of ${formatNumber(r.max)}`).join('; '))
function pct(i: number): string {
  return Math.round(fractions.value[i] * 100) + '%'
}
</script>

<template>
  <A2UIChartFrame :result="result" :title="component.label || component.id" kind="rings" :label="component.label" :sql="isBound(component)" :loading="loading" :error="error" :empty="rings.length === 0" :animated="enabled" @replay="replay">
    <div class="a2ui-rings">
      <svg :viewBox="`0 0 ${SIZE} ${SIZE}`" class="a2ui-rings__svg" role="img" :aria-label="ariaLabel">
        <g v-for="(r, i) in rings" :key="i" :class="[seriesClass(i), { 'a2ui-rings__ring--active': selected === i }]" @mouseenter="selected = i">
          <circle :cx="C" :cy="C" :r="radius(i)" class="a2ui-rings__track" />
          <circle
            :cx="C" :cy="C" :r="radius(i)" class="a2ui-rings__arc"
            :stroke-dasharray="circ(i)" :stroke-dashoffset="circ(i) * (1 - Math.min(frac(i), 1))"
            :transform="`rotate(-90 ${C} ${C})`"
          ><title>{{ r.label }}: {{ formatNumber(r.value) }} / {{ formatNumber(r.max) }}</title></circle>
          <circle v-if="frac(i) > 0.005" :cx="tip(i).x" :cy="tip(i).y" r="3.5" class="a2ui-rings__tip" />
        </g>
        <text :x="C" :y="C + 2" text-anchor="middle" class="a2ui-rings__center">{{ rings[selected] ? pct(selected) : '' }}</text>
      </svg>
      <ul class="a2ui-rings__list">
        <li
          v-for="(r, i) in rings" :key="i" tabindex="0" class="a2ui-rings__row"
          :class="[seriesClass(i), { 'a2ui-rings__row--active': selected === i }]"
          @mouseenter="selected = i" @focus="selected = i"
        >
          <span class="a2ui-kchart__swatch"></span>
          <span class="a2ui-rings__label">{{ r.label }}</span>
          <span class="a2ui-rings__value">{{ formatNumber(r.value) }}<span class="a2ui-rings__max"> / {{ formatNumber(r.max) }}</span></span>
        </li>
      </ul>
    </div>
  </A2UIChartFrame>
</template>
