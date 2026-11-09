<script setup lang="ts">
// BulkAcceptDialog — the confirmation before "Accept all" in the proposals
// inbox. One line per proposal (kind, name, the file it writes, lines added and
// removed). The list scrolls inside the dialog so Cancel and Accept stay on
// screen however many there are, and focus starts on Cancel: Enter must not
// write a long list by accident.
import { ref, toRef } from 'vue'
import { useModalKeys } from '../composables/useModal'
import type { BulkItem } from '../composables/useBulkAccept'

const props = defineProps<{ visible: boolean; items: BulkItem[] }>()
const emit = defineEmits<{ confirm: []; cancel: [] }>()
const root = ref<HTMLElement | null>(null)
const cancelBtn = ref<HTMLButtonElement | null>(null)
useModalKeys(toRef(props, 'visible'), () => emit('cancel'), root, () => cancelBtn.value)
</script>

<template>
  <div v-if="visible" class="modal-overlay" @click.self="emit('cancel')">
    <div ref="root" class="card card-pad bulk-dialog" role="dialog" aria-modal="true" aria-labelledby="bulk-dialog-title" data-testid="bulk-accept-dialog">
      <h3 id="bulk-dialog-title" class="card-title" style="font-size: 22px; margin-bottom: 8px;">Accept all proposals</h3>
      <p class="sh-body" style="margin: 0 0 12px; color: var(--sh-graphite);">
        These {{ items.length }} proposals are written exactly as you reviewed them, in this order. Nothing is overwritten; the run stops at the first one that cannot be written.
      </p>
      <ul class="bulk-dialog__list" tabindex="0" aria-label="Proposals that will be accepted" data-testid="bulk-accept-list">
        <li v-for="i in items" :key="i.id">
          <strong>{{ i.kind }} {{ i.name }}</strong>
          <span class="sh-mono bulk-dialog__meta">{{ i.target }} · +{{ i.added }} −{{ i.removed }}</span>
          <span v-if="i.stale" class="bulk-dialog__stale">changed since it was proposed; the run stops here</span>
        </li>
      </ul>
      <div class="row" style="justify-content: flex-end; margin-top: 12px;">
        <button ref="cancelBtn" type="button" class="btn btn-ghost" data-testid="bulk-accept-cancel" @click="emit('cancel')">Cancel</button>
        <button type="button" class="btn btn-primary" data-testid="bulk-accept-confirm" @click="emit('confirm')">Accept {{ items.length }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.bulk-dialog { width: 100%; max-width: 560px; max-height: 85vh; display: flex; flex-direction: column; }
.bulk-dialog__list { margin: 0; padding: 0; list-style: none; overflow: auto; min-height: 0; border: var(--sh-border-thin); border-radius: var(--sh-radius-sm); }
.bulk-dialog__list li { display: flex; flex-direction: column; gap: 2px; padding: var(--sh-space-2) var(--sh-space-3); border-bottom: var(--sh-border-thin); overflow-wrap: anywhere; }
.bulk-dialog__list li:last-child { border-bottom: 0; }
.bulk-dialog__meta { font-size: var(--sh-fs-xs); color: var(--sh-graphite); }
.bulk-dialog__stale { font-size: var(--sh-fs-xs); color: var(--sh-error); }
</style>
