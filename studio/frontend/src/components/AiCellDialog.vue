<script setup lang="ts">
import { App, hasBinding } from '../lib/bindings'
// AiCellDialog — the AI-cell authoring dialog (#66 part B). All state
// machine logic lives in useAiCellDialog; this component is rendering only,
// so NotebookView.spec.ts-style tests can exercise apply/cap rules against
// the composable directly without mounting this component, while a Vitest
// component test can still drive it end to end.
import { computed, nextTick, ref, watch } from 'vue'
import Icon from './Icon.vue'
import { useModalKeys } from '../composables/useModal'
import type { useAiCellDialog } from '../composables/useAiCellDialog'

const props = defineProps<{
  dialog: ReturnType<typeof useAiCellDialog>
  // Where focus goes on close when the opener is detached (the notebook's Generate with AI button).
  fallbackFocus?: () => HTMLElement | null | undefined
}>()

// applied carries the dialog's applyNote (review fix, finding 6: e.g. "The
// cell changed while this was running, so this was added as a new cell below.") so the
// host can toast it — empty string when apply was a plain replace/insert
// with nothing noteworthy to say.
const emit = defineEmits<{ applied: [note: string] }>()

const title = computed(() => props.dialog.state.titleOverride || (props.dialog.state.mode === 'edit' ? 'Edit with AI' : 'Generate with AI'))

// preview drives the Apply button's label/explanation BEFORE the click —
// C2's "never destroy the cell" and the drift check both need to be visible
// up front, not discovered only after clicking Apply.
const preview = computed(() => props.dialog.previewApply())
const applyButtonLabel = computed(() => {
  const p = preview.value
  if (props.dialog.state.mode !== 'edit') return 'Insert cell'
  if (p && (p.typeMismatch || p.cellDrifted || p.targetMissing)) return 'Insert as new cell'
  return 'Apply'
})
const applyExplanation = computed(() => {
  const p = preview.value
  if (!p) return ''
  if (p.cellDrifted) return 'The cell changed while this was running, so it will be added as a new cell instead.'
  if (p.typeMismatch) return 'This is a different cell type from the one you were editing, so it will be added as a new cell instead.'
  if (p.targetMissing) return 'The cell you were editing is gone, so it will be added as a new cell instead.'
  return ''
})

// draftText is the textarea/input's own v-model target — kept separate from
// state.description (which is only assigned once a turn actually starts, so
// it always reflects "what the current/last turn asked", not mid-typing
// input). Cleared on every submit (review fix, finding 5: the refine/answer
// box used to start pre-filled with the ORIGINAL request text, since it was
// only ever cleared when re-entering the 'prompt' state, which a
// refine/follow-up never does).
const draftText = ref('')

function onSubmit() {
  const text = draftText.value.trim()
  if (!text) return
  draftText.value = ''
  props.dialog.submit(text)
}

function onFollowUp() {
  const text = draftText.value.trim()
  if (!text) return
  draftText.value = ''
  props.dialog.submit(text)
}

function onApply() {
  const applied = props.dialog.apply()
  if (applied) emit('applied', props.dialog.state.applyNote)
}

function onClose() {
  try {
    const taskId = props.dialog.state.taskId
    if (taskId && hasBinding('CancelAgentTask')) {
      App.CancelAgentTask(taskId)
    }
  } catch {
    // best-effort cancel; closing the dialog must never throw
  }
  props.dialog.close()
}

// Escape closes (cancelling a running task), Tab stays inside the dialog and
// focus returns to the opener (useModalKeys).
const open = computed(() => props.dialog.state.dialogState !== 'closed')
const root = ref<HTMLElement | null>(null)

// Focus management (review fix, finding 9): focus the relevant input
// whenever the dialog ENTERS a state that has one, after Vue has actually
// rendered it (nextTick — the v-if swap means the element doesn't exist
// yet on the same tick the state changes).
const promptEl = ref<HTMLTextAreaElement | null>(null)
const refineEl = ref<HTMLInputElement | null>(null)
const answerEl = ref<HTMLInputElement | null>(null)
useModalKeys(open, onClose, root, () => promptEl.value ?? refineEl.value ?? answerEl.value, () => props.fallbackFocus?.())
watch(
  // taskId too: a close-then-open in one tick leaves dialogState at 'prompt', but mints a new id.
  () => [props.dialog.state.dialogState, props.dialog.state.taskId] as const,
  async ([s]) => {
    // A fresh open (any prior state -> 'prompt') must never carry over
    // draftText from a previous dialog session — this component instance
    // stays mounted across open/close (only the v-if inside THIS template
    // toggles), so draftText is otherwise still whatever was last typed.
    if (s === 'prompt') draftText.value = props.dialog.state.prefill
    await nextTick()
    if (s === 'prompt') promptEl.value?.focus()
    else if (s === 'proposal') refineEl.value?.focus()
    else if (s === 'needs_info') answerEl.value?.focus()
  },
)
</script>

<template>
  <div v-if="dialog.state.dialogState !== 'closed'" class="modal-overlay" role="dialog" aria-modal="true" :aria-label="title" @click.self="onClose" data-testid="ai-cell-dialog">
    <div ref="root" class="card card-pad" style="width: 100%; max-width: 560px; max-height: 80vh; overflow: auto;">
      <div class="row" style="justify-content: space-between; align-items: center; margin-bottom: var(--sh-space-3);">
        <h3 class="card-title" style="font-size: 18px; display: flex; align-items: center; gap: var(--sh-space-2);">
          <Icon name="sparkle" :size="16" />
          {{ title }}
        </h3>
        <button @click="onClose" class="btn btn-ghost btn-sm" data-testid="ai-cell-close" aria-label="Close"><Icon name="x" :size="14" /></button>
      </div>

      <!-- prompt -->
      <div v-if="dialog.state.dialogState === 'prompt'" class="col" style="gap: var(--sh-space-3);">
        <div v-if="dialog.state.tables.length" class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite);" data-testid="ai-cell-loaded-tables">
          Loaded tables: {{ dialog.state.tables.join(', ') }}
        </div>
        <textarea
          ref="promptEl"
          v-model="draftText"
          class="field-input sh-mono"
          data-testid="ai-cell-prompt"
          placeholder="What do you want this cell to show? e.g. “revenue by region”"
          style="min-height: 90px; resize: vertical;"
        ></textarea>
        <div class="row" style="justify-content: flex-end;">
          <button @click="onSubmit" class="btn btn-primary" data-testid="ai-cell-generate" :disabled="!draftText.trim()">Generate</button>
        </div>
      </div>

      <!-- working -->
      <div v-else-if="dialog.state.dialogState === 'working'" class="col" style="gap: var(--sh-space-3);" data-testid="ai-cell-working">
        <div class="sh-mono" style="color: var(--sh-graphite); display: flex; align-items: center; gap: var(--sh-space-2);">
          <Icon name="refresh" :size="14" /> Working…
        </div>
        <ul v-if="dialog.state.checked.length" class="col" style="gap: 4px; list-style: none; padding: 0; margin: 0;">
          <li v-for="c in dialog.state.checked" :key="c" class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite); display: flex; align-items: center; gap: 6px;">
            <Icon name="check" :size="12" style="color: var(--sh-teal);" /> {{ c }}
          </li>
        </ul>
      </div>

      <!-- proposal -->
      <div v-else-if="dialog.state.dialogState === 'proposal'" class="col" style="gap: var(--sh-space-3);" data-testid="ai-cell-proposal">
        <div v-if="dialog.state.envelope?.cell?.type === 'sql'" class="col" style="gap: var(--sh-space-2);">
          <div class="sh-mono" style="font-weight: 700;">{{ dialog.state.envelope.cell.label || 'Untitled query' }}</div>
          <pre class="sh-mono" style="font-size: var(--sh-fs-xs); background: var(--sh-paper-2); padding: var(--sh-space-3); overflow: auto; margin: 0;" data-testid="ai-cell-proposal-sql">{{ dialog.state.envelope.cell.sql }}</pre>
          <div class="badge badge--graphite" style="width: fit-content;">{{ dialog.state.envelope.cell.view || 'table' }}</div>
        </div>
        <div v-else-if="dialog.state.envelope?.cell?.type === 'markdown'" class="col" style="gap: var(--sh-space-2);">
          <div class="sh-mono" style="font-weight: 700;">Markdown</div>
          <pre class="sh-mono" style="font-size: var(--sh-fs-xs); background: var(--sh-paper-2); padding: var(--sh-space-3); overflow: auto; margin: 0;">{{ dialog.state.envelope.cell.markdown }}</pre>
        </div>
        <p v-if="dialog.state.envelope?.explanation" class="sh-body" style="color: var(--sh-graphite);" data-testid="ai-cell-explanation">{{ dialog.state.envelope.explanation }}</p>
        <div v-if="dialog.state.checked.length" class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite);">
          Checked: {{ dialog.state.checked.join(', ') }}
        </div>
        <p v-if="applyExplanation" class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-warn);" data-testid="ai-cell-apply-note">{{ applyExplanation }}</p>
        <div class="row" style="gap: var(--sh-space-2);">
          <input ref="refineEl" v-model="draftText" class="field-input sh-mono" placeholder="Refine (e.g. “make it monthly for East only”)" data-testid="ai-cell-refine-input" @keyup.enter="onFollowUp" />
          <button @click="onFollowUp" class="btn btn-ghost btn-sm" data-testid="ai-cell-refine" :disabled="!draftText.trim()">Refine</button>
        </div>
        <div class="row" style="justify-content: flex-end; gap: var(--sh-space-2);">
          <button @click="onClose" class="btn btn-ghost">Discard</button>
          <button @click="onApply" class="btn btn-primary" data-testid="ai-cell-apply">{{ applyButtonLabel }}</button>
        </div>
      </div>

      <!-- needs_info -->
      <div v-else-if="dialog.state.dialogState === 'needs_info'" class="col" style="gap: var(--sh-space-3);" data-testid="ai-cell-needs-info">
        <div class="sh-mono" style="color: var(--sh-error); font-weight: 700;">Needs more information</div>
        <ul v-if="dialog.state.envelope?.questions?.length" class="col" style="gap: 4px; padding-left: 18px; margin: 0;">
          <li v-for="(q, i) in dialog.state.envelope.questions" :key="i" class="sh-body">{{ q }}</li>
        </ul>
        <p v-if="dialog.state.envelope?.missingData?.length" class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite);">
          Missing: {{ dialog.state.envelope.missingData.join('; ') }}
        </p>
        <p v-if="dialog.state.envelope?.notes" class="sh-body" style="color: var(--sh-graphite);">{{ dialog.state.envelope.notes }}</p>
        <div v-if="dialog.state.checked.length" class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite);">
          Checked: {{ dialog.state.checked.join(', ') }}
        </div>
        <div class="row" style="gap: var(--sh-space-2);">
          <input ref="answerEl" v-model="draftText" class="field-input sh-mono" placeholder="Answer the questions above" data-testid="ai-cell-answer-input" @keyup.enter="onFollowUp" />
          <button @click="onFollowUp" class="btn btn-primary btn-sm" data-testid="ai-cell-answer" :disabled="!draftText.trim()">Answer</button>
        </div>
      </div>

      <!-- unresolved -->
      <div v-else-if="dialog.state.dialogState === 'unresolved'" class="col" style="gap: var(--sh-space-3);" data-testid="ai-cell-unresolved">
        <div class="sh-mono" style="color: var(--sh-error); font-weight: 700;">Couldn't finish this one</div>
        <p class="sh-body" style="color: var(--sh-graphite);">{{ dialog.state.envelope?.notes || dialog.state.error || 'The Slaw assistant stopped without producing a cell.' }}</p>
        <div v-if="dialog.state.checked.length" class="sh-mono" style="font-size: var(--sh-fs-xs); color: var(--sh-graphite);">
          Checked: {{ dialog.state.checked.join(', ') }}
        </div>
        <div class="row" style="justify-content: flex-end; gap: var(--sh-space-2);">
          <button @click="onClose" class="btn btn-ghost">Close</button>
          <button @click="() => { draftText = dialog.state.description; onFollowUp() }" class="btn btn-primary" data-testid="ai-cell-retry">Try again</button>
        </div>
      </div>
    </div>
  </div>
</template>
