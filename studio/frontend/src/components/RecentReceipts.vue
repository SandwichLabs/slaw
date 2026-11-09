<script setup lang="ts">
// RecentReceipts — the newest receipts of this workspace in Settings > Workspace >
// Receipts: when, where it ran, what it was and how many rows. A line's button opens
// the Evidence drawer for that receipt by its id alone: the drawer then shows what
// was asked, never an answer. The list reloads when the number of receipts kept
// changes (a Delete all, or a new run seen on return). Text interpolation only.
import { watch } from 'vue'
import { useRecentReceipts } from '../composables/useRecentReceipts'
import { openEvidenceById } from '../composables/useEvidenceDrawer'
import { whenText, byText } from '../lib/receiptText'

const props = defineProps<{ count: number }>()
const { rows, error, capped, refresh } = useRecentReceipts()
watch(() => props.count, () => { void refresh() }, { immediate: true })

const what = (s: ReceiptSummary) => (s.sql_withheld ? '(statement not kept)' : s.sql || '(no statement)')
const who = (s: ReceiptSummary) => byText({ surface: s.surface, actor: s.actor } as ReceiptRecord)
</script>

<template>
  <section class="col" style="gap: var(--sh-space-2);" aria-labelledby="recent-receipts-h" data-testid="recent-receipts">
    <h3 id="recent-receipts-h" class="sh-label" style="margin: 0;">Recent receipts</h3>
    <div v-if="error" class="help" role="alert" style="color: var(--sh-error);" data-testid="recent-receipts-error">{{ error }}</div>
    <p v-else-if="rows.length === 0 && capped" class="help" style="margin: 0;" data-testid="recent-receipts-capped">None among the newest 2,000 receipts.</p>
    <p v-else-if="rows.length === 0" class="help" style="margin: 0;" data-testid="recent-receipts-empty">No receipts yet. A read in the Query Explorer, a notebook or a page makes one.</p>
    <ul v-else class="recent" aria-label="Recent receipts">
      <li v-for="s in rows" :key="s.id" class="recent__row" :data-testid="`recent-${s.id}`">
        <div class="recent__main">
          <code class="recent__sql" :title="what(s)">{{ what(s) }}</code>
          <span class="sh-meta">{{ whenText(s.at) }} · {{ who(s) }} · {{ s.rows.returned.toLocaleString() }} {{ s.rows.returned === 1 ? 'row' : 'rows' }}<template v-if="s.source.ref"> · {{ s.source.ref }}</template></span>
        </div>
        <button type="button" class="btn btn-ghost btn-sm" :aria-label="`Show evidence for ${s.id}`" data-testid="recent-show" @click="openEvidenceById(s.id, $event)">Evidence</button>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.recent { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; border: var(--sh-border-thin); border-radius: var(--sh-radius-sm); }
.recent__row { display: flex; align-items: center; justify-content: space-between; gap: var(--sh-space-3); padding: var(--sh-space-2) var(--sh-space-3); min-width: 0; }
.recent__row + .recent__row { border-top: 1.5px solid color-mix(in srgb, var(--sh-ink) 22%, transparent); }
.recent__main { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.recent__sql { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.recent__main .sh-meta { overflow-wrap: anywhere; }
</style>
