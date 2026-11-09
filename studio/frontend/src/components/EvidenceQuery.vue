<script setup lang="ts">
// EvidenceQuery — the statement as written and as run (a segmented control), the
// parameters, and the file it came from. A receipt keeps parameter values by
// default; with "Keep parameter values" off it keeps only names and a hash, and
// the values shown here are the open run's own, from memory. A drawer opened
// later, with no run in memory, says "not kept". Text interpolation only.
import { computed, ref } from 'vue'
import Icon from './Icon.vue'
import { shortHash, fromText } from '../lib/receiptText'
import { closeEvidence } from '../composables/useEvidenceDrawer'
import type { RunMemory } from '../composables/useReceipt'

const props = defineProps<{ receipt: ReceiptRecord; memory?: RunMemory }>()
const mode = ref<'written' | 'run'>('written')

const sql = computed(() => (mode.value === 'written' ? props.receipt.sql : props.receipt.run_sql) ?? '')
const withheld = computed(() => (mode.value === 'written' ? props.receipt.sql_withheld : props.receipt.run_sql_withheld))

type Row = { name: string; text: string; kept: boolean }
const rows = computed<Row[]>(() => {
  const r = props.receipt
  const fromRun = props.memory?.params ?? {}
  return Object.keys(r.params ?? {}).sort().map((name): Row => {
    const p = r.params?.[name]
    if (r.params_withheld) return name in fromRun ? { name, text: fromRun[name], kept: false } : { name, text: 'not kept', kept: false }
    if (p?.withheld) return { name, text: 'withheld', kept: false }
    return { name, text: (p?.value ?? '') + (p?.truncated ? '…' : ''), kept: true }
  })
})
const shownFromRun = computed(() => !!props.receipt.params_withheld && rows.value.some((r) => r.text !== 'not kept'))
</script>

<template>
  <section class="ev-sec" aria-labelledby="ev-h-query" data-testid="evidence-query">
    <h3 id="ev-h-query" class="ev-h">Query</h3>
    <div class="ev-seg" role="group" aria-label="Statement">
      <button type="button" :aria-pressed="mode === 'written'" data-testid="evidence-as-written" @click="mode = 'written'">As written</button>
      <button type="button" :aria-pressed="mode === 'run'" data-testid="evidence-as-run" @click="mode = 'run'">As run</button>
    </div>
    <pre v-if="sql" class="ev-code" data-testid="evidence-sql">{{ sql }}</pre>
    <p v-else-if="withheld" class="ev-muted" data-testid="evidence-sql-withheld">Not kept: this text looked like it held a credential. The receipt keeps only a fingerprint of it.</p>
    <p v-else class="ev-muted">No statement was recorded.</p>

    <dl v-if="rows.length" class="ev-kv" data-testid="evidence-params">
      <template v-for="r in rows" :key="r.name"><dt>${{ r.name }}</dt><dd :class="{ 'ev-muted': r.text === 'not kept' || r.text === 'withheld' }">{{ r.text }}</dd></template>
    </dl>
    <p v-if="receipt.params_withheld" class="ev-muted ev-lock" data-testid="evidence-params-note">
      <Icon name="lock" :size="12" style="flex: none; margin-top: 2px;" />
      <span>
        <template v-if="shownFromRun">Shown from this run. </template>The receipt keeps only the names and a hash
        <template v-if="receipt.params_hash">({{ shortHash(receipt.params_hash) }})</template>.
        Change in <router-link to="/settings" class="ev-link" data-testid="evidence-settings-link" @click="closeEvidence()">Settings &gt; Workspace &gt; Receipts</router-link>.
      </span>
    </p>
    <p v-if="receipt.source.ref" class="ev-muted" data-testid="evidence-source">From {{ fromText(receipt) }}</p>
  </section>
</template>
