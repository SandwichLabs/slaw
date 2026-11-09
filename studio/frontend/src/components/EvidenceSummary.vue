<script setup lang="ts">
// EvidenceSummary — the drawer's first section: one sentence about what was
// read, then a badge for whether it can be read again and one for the verdict.
// "Replayable" is said only when every connection has a snapshot that can be
// read at; nothing here reads "verified". Text interpolation only.
import { computed } from 'vue'
import Icon from './Icon.vue'
import { replayState, summaryParts, verdictText } from '../lib/receiptText'

const props = defineProps<{ receipt: ReceiptRecord }>()
const replay = computed(() => replayState(props.receipt))
const parts = computed(() => summaryParts(props.receipt))
</script>

<template>
  <section class="ev-sec" aria-labelledby="ev-h-summary" data-testid="evidence-summary">
    <h3 id="ev-h-summary" class="ev-h">Summary</h3>
    <p class="ev-sentence">
      <template v-if="parts.names.length">
        One query on
        <template v-for="(n, i) in parts.names" :key="n"><strong>{{ n }}</strong><template v-if="i < parts.names.length - 1">, </template></template><template v-if="parts.snapshot">, read at snapshot <strong>{{ parts.snapshot }}</strong>. Run it at that snapshot and you read the same data.</template><template v-else>.</template>
      </template>
      <template v-else>One query, on no connection that was recorded.</template>
    </p>
    <div class="ev-badges">
      <span v-if="replay === 'replayable'" class="badge badge--success" data-testid="evidence-replay"><span class="dot"></span>Replayable</span>
      <span v-else-if="replay === 'live_only'" class="badge badge--warn" data-testid="evidence-replay"><Icon name="clock" :size="12" />Live only</span>
      <span v-else class="badge badge--graphite" data-testid="evidence-replay">Not recorded</span>
      <span class="badge" :class="receipt.verdict === 'read' ? 'badge--teal' : 'badge--graphite'" data-testid="evidence-verdict">{{ verdictText(receipt) }}</span>
    </div>
  </section>
</template>
