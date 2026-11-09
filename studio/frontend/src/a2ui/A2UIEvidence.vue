<script setup lang="ts">
// A2UIEvidence — the small icon button on a data-bound value (stat, table, every
// chart) that opens the Evidence drawer for the run behind it. It shows when the
// run has a receipt, or a note saying why it has none; a value with neither (inline
// data, a run nothing is due a receipt for) gets no button. The drawer is shared
// (useEvidenceDrawer); this only asks for it.
import { computed } from 'vue'
import Icon from '../components/Icon.vue'
import { openEvidence } from '../composables/useEvidenceDrawer'
import type { LiveQueryResult } from './query'

const props = defineProps<{
  result?: LiveQueryResult | null
  /** The title of the value: the component's label, else its id. */
  title: string
}>()

const shown = computed(() => !!props.result && !!(props.result.receipt_id || props.result.receipt_note))
function open(e: MouseEvent) {
  const r = props.result
  if (r) openEvidence({ title: props.title, receiptId: r.receipt_id, note: r.receipt_note }, e)
}
</script>

<template>
  <button
    v-if="shown"
    type="button"
    class="a2ui-evidence"
    :aria-label="`Show evidence for ${title}`"
    :title="`Show evidence for ${title}`"
    data-testid="a2ui-evidence"
    @click="open"
  ><Icon name="fileText" :size="12" /></button>
</template>
