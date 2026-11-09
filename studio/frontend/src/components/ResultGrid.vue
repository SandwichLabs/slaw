<script setup lang="ts">
// ResultGrid — a query result as a table: the Query Explorer's grid for a
// result shown elsewhere (a metric's "Try it"). Values go through formatCell, the
// one cell formatter, and every cell is text. A result that stopped at the row
// cap says so (`truncated`), without inventing a count of the rest.
import { computed } from 'vue'
import { cellClass, formatCell, isNumericType } from '../lib/formatCell'
import { openEvidence } from '../composables/useEvidenceDrawer'

const props = defineProps<{ result: QueryResult; title?: string }>()
const rows = computed(() => (props.result.rows ?? []).map((row) => row.map((cell, j) => formatCell(cell, props.result.columns[j]?.type))))
</script>

<template>
  <div class="result-grid" data-testid="result-grid">
    <div v-if="result.truncated" class="note warn" role="status" data-testid="result-truncated">
      Showing the first {{ result.row_count.toLocaleString() }} rows; there are more. Open the saved query in the Query Explorer to export all of them.
    </div>
    <div class="result-grid__scroll">
      <table class="table">
        <thead>
          <tr>
            <th v-for="col in result.columns" :key="col.name" :class="{ 'cell-num': isNumericType(col.type) }">{{ col.name }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, i) in rows" :key="i">
            <td v-for="(fc, j) in row" :key="j" :class="cellClass(fc, result.columns[j]?.type)" :title="fc.title">{{ fc.text }}</td>
          </tr>
          <tr v-if="!rows.length"><td :colspan="Math.max(result.columns.length, 1)" class="help">No rows.</td></tr>
        </tbody>
      </table>
    </div>
    <p class="help result-grid__meta" data-testid="result-meta">{{ result.row_count.toLocaleString() }} {{ result.row_count === 1 ? 'row' : 'rows' }} in {{ result.duration }}
      <button v-if="result.receipt_id || result.receipt_note" type="button" class="ev-link" data-testid="result-evidence" :aria-label="`Show evidence for ${title ?? 'this result'}`" @click="openEvidence({ title: title ?? 'Result', receiptId: result.receipt_id, note: result.receipt_note }, $event)">Evidence</button>
    </p>
  </div>
</template>

<style scoped>
.result-grid { display: flex; flex-direction: column; gap: var(--sh-space-2); min-width: 0; }
.result-grid__scroll { overflow: auto; max-height: 320px; border: var(--sh-border-thin); border-radius: var(--sh-radius-sm); }
.result-grid__meta { margin: 0; }
</style>
