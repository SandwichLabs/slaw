<script setup lang="ts">
// EvidenceConnections — each connection the statement resolved to, with the
// snapshot taken right after the run, side by side. The snapshot is a fact about
// the source's state; only a "replayable" one can be read at again. A fact that
// is live only says so, and a "not recorded" one (a table of an Iceberg catalog,
// a pinned version, a fact that timed out) never gets replay wording.
import Icon from './Icon.vue'
import { statusText } from '../lib/receiptText'

defineProps<{ connections: ReceiptConnection[] }>()
</script>

<template>
  <section class="ev-sec" aria-labelledby="ev-h-conn" data-testid="evidence-connections">
    <h3 id="ev-h-conn" class="ev-h">Connection and snapshot</h3>
    <p v-if="connections.length === 0" class="ev-muted">No connection was recorded.</p>
    <div v-for="c in connections" :key="c.name" class="ev-pair" :data-testid="`evidence-conn-${c.name}`">
      <div class="ev-cell">
        <b>{{ c.name }}</b>
        <span class="ev-mono">{{ c.type }}<template v-if="c.read_only"> · read-only</template></span>
        <span class="ev-muted" data-testid="evidence-conn-status">{{ statusText(c.status) }}</span>
      </div>
      <div class="ev-cell" data-testid="evidence-snapshot">
        <template v-if="!c.snapshot">
          <span class="ev-muted">No snapshot: the connection was not attached.</span>
        </template>
        <template v-else-if="c.snapshot.label === 'not_recorded'">
          <b>Not recorded</b>
          <span class="ev-muted">Not recorded: {{ c.snapshot.value }}</span>
        </template>
        <template v-else>
          <b class="ev-mono" data-testid="evidence-snapshot-value">{{ c.snapshot.value }}</b>
          <span class="ev-muted">{{ c.snapshot.fact }}</span>
          <span v-if="c.snapshot.label === 'live_only'" class="ev-muted ev-row" data-testid="evidence-live-only"><Icon name="clock" :size="11" style="flex: none; margin-top: 2px;" /><span>Where the source was when it answered. You can't read that moment again.</span></span>
          <span v-else class="ev-muted" data-testid="evidence-replayable">Recorded right after the run: current when the run finished.</span>
        </template>
      </div>
    </div>
  </section>
</template>
