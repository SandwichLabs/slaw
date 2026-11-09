<script setup lang="ts">
// ApprovalCard — the inline "OK this change?" card for a pending agent
// write (#79). Sits under its matching ToolStepChip (see that component),
// correlated by tool-call id, so the transcript keeps explaining why the
// agent is asking right next to the ask itself — a modal dialog (useModal)
// would block that context instead.
import { computed, ref, onMounted, nextTick } from 'vue'
import type { PendingApproval } from '../composables/useAgentChat'
import ConnTypeTile from './ConnTypeTile.vue'
import Icon from './Icon.vue'
import { connectionByName } from '../composables/useConnections'

// done: true once the matching ToolStep has left 'running' (set by
// ToolStepChip from step.status), so the state line can say "Approved."
// once the write has actually finished instead of sitting on "running…"
// forever.
const props = defineProps<{ approval: PendingApproval; done?: boolean }>()
const emit = defineEmits<{ resolve: [approved: boolean] }>()

// A change to anything that is not a local file names where it goes (#117,
// SPEC 10): the header, a connection chip, and the button all say so. The
// target comes from the policy (the approval's connections), never model prose.
const targets = computed(() => props.approval.connections ?? [])
const target = computed(() => targets.value.join(', '))
const targetInfo = computed(() => (targets.value.length === 1 ? connectionByName(targets.value[0]) : undefined))
const isDelete = computed(() => props.approval.destructive && /\b(delete|drop|truncate)\b/i.test(`${props.approval.sql ?? ''} ${props.approval.summary}`))
const title = computed(() => {
  if (!target.value) return 'Allow this change?'
  return isDelete.value ? `Allow delete in ${target.value}?` : `Allow this change to ${target.value}?`
})
const doLabel = computed(() => {
  if (!target.value) return 'Do it'
  return isDelete.value ? `Allow delete in ${target.value}` : `Allow change to ${target.value}`
})
// The row count is the policy's estimate (never model prose); without one the
// card says nothing about counts.
const rowNote = computed(() => {
  const n = props.approval.rows
  return props.approval.destructive && typeof n === 'number' && n >= 0 ? `It affects about ${n.toLocaleString()} row${n === 1 ? '' : 's'}. Check that this is what you expect before you allow it.` : ''
})

// Focus starts on Decline, the safe default, never on Allow. Escape declines.
const cancelBtn = ref<HTMLButtonElement | null>(null)
onMounted(() => {
  if (props.approval.state === 'pending') void nextTick(() => cancelBtn.value?.focus({ preventScroll: true }))
})
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && props.approval.state === 'pending') { e.stopPropagation(); emit('resolve', false) }
}
</script>

<template>
  <div class="approval-card" role="group" :aria-label="target ? `Approval needed for ${target}` : 'Approval needed'" data-testid="approval-card" @keydown="onKey">
    <div class="approval-card__title"><Icon v-if="target" name="shield" :size="16" /> {{ title }}</div>
    <span v-if="target" class="ap-conn" data-testid="approval-connection">
      <ConnTypeTile v-if="targetInfo" :type="targetInfo.type" />
      {{ target }} <span class="small">not your local data</span>
    </span>
    <p class="approval-card__summary">{{ props.approval.summary }}</p>
    <!-- where is already folded into the summary sentence (see describeWrite
         in internal/agents/approval.go) — the <pre> only adds anything new
         for run_query, where sql is the full statement, not a paraphrase. -->
    <pre v-if="props.approval.sql" class="approval-card__detail">{{ props.approval.sql }}</pre>
    <p v-if="target" class="approval-card__hint" data-testid="approval-undo-note">This is outside your workspace's local data, so Slaw can't undo it for you.<template v-if="rowNote"> {{ rowNote }}</template></p>

    <div class="approval-card__actions">
      <button
        ref="cancelBtn"
        type="button"
        class="btn btn-sm"
        :disabled="props.approval.state !== 'pending'"
        data-testid="approval-cancel"
        @click="emit('resolve', false)"
      >
        Decline
      </button>
      <button
        type="button"
        class="btn btn-sm"
        :class="props.approval.destructive ? 'btn-destructive' : 'btn-primary'"
        :disabled="props.approval.state !== 'pending'"
        data-testid="approval-do-it"
        @click="emit('resolve', true)"
      >
        {{ doLabel }}
      </button>
    </div>

    <p class="approval-card__state" role="status" aria-live="polite">
      <template v-if="props.approval.state === 'approved'">{{ props.done ? 'Approved.' : 'Approved, running…' }}</template>
      <template v-else-if="props.approval.state === 'declined'">Declined, nothing changed.</template>
      <template v-else-if="props.approval.state === 'expired'">No answer in time, nothing changed.</template>
      <template v-else-if="props.approval.state === 'cancelled'">Stopped before it ran, nothing changed.</template>
      <template v-else-if="props.approval.state === 'gone'">No longer waiting, nothing changed.</template>
      <template v-else-if="props.approval.error">{{ props.approval.error }}</template>
    </p>
  </div>
</template>

<style scoped>
.approval-card {
  margin: 6px 0;
  padding: 10px 12px;
  border: 1.5px solid var(--sh-coral);
  border-radius: var(--sh-radius-sm);
  background: var(--sh-coral-8);
}
.approval-card__title {
  font-weight: 700;
  margin-bottom: 4px;
  display: flex;
  align-items: center;
  gap: 8px;
  font-family: var(--sh-font-display);
}
.ap-conn {
  display: inline-flex; align-items: center; gap: 8px; margin: 0 0 8px; padding: 4px 10px;
  border: 1.5px solid var(--slaw-border); border-radius: var(--sh-radius-sm); background: var(--sh-paper-2); font-weight: 600;
}
.ap-conn .small { font-weight: 400; color: var(--sh-graphite); font-size: 12px; }
.approval-card__summary {
  margin: 0 0 6px;
}
.approval-card__detail {
  margin: 0 0 8px;
  padding: 6px 8px;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: var(--sh-font-mono);
  font-size: 12px;
  background: var(--sh-surface);
  border: var(--sh-border-thin);
  border-radius: var(--sh-radius-sm);
}
.approval-card__hint {
  margin: 0 0 8px;
  font-size: 12px;
  color: var(--sh-graphite);
}
.approval-card__actions {
  display: flex;
  gap: 8px;
}
.approval-card__state {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--sh-graphite);
}
.approval-card__state:empty {
  display: none;
}
</style>
