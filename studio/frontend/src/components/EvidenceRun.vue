<script setup lang="ts">
// EvidenceRun — when it ran and how long it took, who asked, and from which file.
import { byText, durationText, fromText, whenText } from '../lib/receiptText'

defineProps<{ receipt: ReceiptRecord }>()
</script>

<template>
  <section class="ev-sec" aria-labelledby="ev-h-run" data-testid="evidence-run">
    <h3 id="ev-h-run" class="ev-h">Run</h3>
    <dl class="ev-kv">
      <dt>When</dt><dd>{{ whenText(receipt.at) }} · {{ durationText(receipt.duration_ms) }}</dd>
      <dt>By</dt><dd>{{ byText(receipt) }}</dd>
      <dt>From</dt><dd data-testid="evidence-from">{{ fromText(receipt) }}</dd>
    </dl>
  </section>
</template>
