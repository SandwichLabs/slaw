<script setup lang="ts">
// MetricCard — one metric: what it measures, the dimensions it can be broken
// down by, the saved query it reads (a link to the Query Explorer), how the
// disagreement over it was settled, and its assertions. "Try it" runs the metric
// by the dimensions ticked and shows the rows; "Run assertions" checks it. Both
// go through the agent policy's read-only path in Go; nothing here can change
// data. All text from the file is shown as text.
import { ref, watch } from 'vue'
import ResultGrid from './ResultGrid.vue'
import AssertionResults from './AssertionResults.vue'
import RunStatus from './RunStatus.vue'
import SourceText from './SourceText.vue'
import { describeAssertion } from '../lib/describeAssertion'
import type { DefinitionRuns } from '../composables/useDefinitionRuns'

const props = defineProps<{ detail: DefinitionDetail; runs: DefinitionRuns }>()
const dims = ref<string[]>([])
watch(() => props.detail.name, () => { dims.value = [] })
</script>

<template>
  <div v-if="detail.metric" class="col definition-card" data-testid="metric-card">
    <div class="row" style="align-items: baseline; gap: var(--sh-space-3); flex-wrap: wrap;">
      <h2 class="card-title" style="margin: 0; font-size: 28px;" data-testid="metric-title">{{ detail.metric.title || detail.metric.name }}</h2>
      <code>{{ detail.metric.name }}</code>
      <span v-if="detail.metric.format" class="badge badge--graphite">{{ detail.metric.format }}</span>
      <span v-if="detail.metric.owner" class="help" style="margin: 0;">owner {{ detail.metric.owner }}</span>
    </div>
    <p v-if="detail.metric.description" style="margin: 0;">{{ detail.metric.description }}</p>

    <dl class="definition-card__facts">
      <dt>Measure</dt><dd><code data-testid="metric-measure">{{ detail.metric.measure }}</code></dd>
      <dt>Dimensions</dt>
      <dd data-testid="metric-dimensions">
        <template v-if="detail.metric.dimensions?.length"><code v-for="d in detail.metric.dimensions" :key="d" class="definition-card__chip">{{ d }}</code></template>
        <span v-else class="help">none: only the single total</span>
      </dd>
      <dt>Saved query</dt>
      <dd>
        <router-link :to="{ path: '/queries', query: { open: detail.metric.query } }" data-testid="metric-query-link"><code>{{ detail.metric.query }}</code></router-link>
        <span v-if="detail.query_missing" class="badge badge--error" style="margin-left: 8px;" data-testid="metric-query-missing"><span class="dot"></span>no such saved query</span>
      </dd>
      <template v-if="detail.metric.settled_by">
        <dt>Settled by</dt><dd data-testid="metric-settled-by">{{ detail.metric.settled_by.text }}<span v-if="detail.metric.settled_by.ref" class="help"> ({{ detail.metric.settled_by.ref }})</span></dd>
      </template>
      <template v-if="detail.metric.assertions?.length">
        <dt>Assertions</dt><dd data-testid="metric-assertions"><code v-for="(a, i) in detail.metric.assertions" :key="i" class="definition-card__chip">{{ describeAssertion(a) }}</code></dd>
      </template>
    </dl>

    <section class="col definition-card__section">
      <h3 class="sh-label" style="margin: 0;">Try it</h3>
      <div class="row" style="gap: var(--sh-space-3); flex-wrap: wrap; align-items: center;">
        <label v-for="d in detail.metric.dimensions ?? []" :key="d" class="row" style="gap: 6px; align-items: center;">
          <input v-model="dims" type="checkbox" :value="d" :data-testid="`try-dim-${d}`"> <span>{{ d }}</span>
        </label>
        <button type="button" class="btn btn-primary btn-sm" :disabled="runs.busy" data-run-trigger data-testid="try-run" @click="runs.tryMetric(detail.metric!.name, dims)">
          Run{{ dims.length ? ` by ${dims.join(', ')}` : ' (total)' }}
        </button>
        <button v-if="detail.metric.assertions?.length" type="button" class="btn btn-sm" :disabled="runs.busy" data-run-trigger data-testid="run-assertions" @click="runs.runAssertions('metric', detail.metric!.name)">Run assertions</button>
      </div>
      <RunStatus :busy="runs.busy" :elapsed="runs.elapsed" :cancelled="runs.cancelled" @stop="runs.stop" :error="runs.error" :conflict="runs.conflict" what="Running" />
      <ResultGrid v-if="runs.tried" :result="runs.tried" :title="detail.metric?.name" />
      <AssertionResults v-if="runs.results.length" :rows="runs.results" />
    </section>

    <SourceText :text="detail.text" :path="`semantics/metrics/${detail.metric.name}.yaml`" />
  </div>
</template>
