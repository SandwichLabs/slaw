<script setup lang="ts">
// EvidenceResult — the shape of the result and its fingerprint, never a value.
// A column whose name could carry a value is shown as "(name withheld)" with its
// type. Text interpolation only.
import { shortHash } from '../lib/receiptText'

defineProps<{ receipt: ReceiptRecord; fromId?: boolean }>()
</script>

<template>
  <section class="ev-sec" aria-labelledby="ev-h-result" data-testid="evidence-result">
    <h3 id="ev-h-result" class="ev-h">Result</h3>
    <p class="ev-sentence" data-testid="evidence-rows">
      <strong>{{ receipt.rows.returned.toLocaleString() }}</strong> returned<template v-if="receipt.rows.total !== receipt.rows.returned"> of <strong>{{ receipt.rows.total.toLocaleString() }}</strong></template><template v-if="receipt.rows.truncated">, truncated</template>
    </p>
    <ul v-if="receipt.columns?.length" class="ev-cols" aria-label="Columns" data-testid="evidence-columns">
      <li v-for="(c, i) in receipt.columns" :key="i"><span>{{ c.withheld ? '(name withheld)' : c.name }}</span><span class="ev-type">{{ c.type }}</span></li>
    </ul>
    <p class="ev-mono" data-testid="evidence-fingerprint"><span class="ev-muted">Fingerprint </span>{{ shortHash(receipt.result_hmac) }}</p>
    <p class="ev-muted">
      The receipt keeps a fingerprint of the rows, never the rows.
      <template v-if="!fromId">The value on the card comes from this run, not from the receipt.</template>
      <template v-else>Receipts hold what was asked, never the answer.</template>
    </p>
  </section>
</template>
