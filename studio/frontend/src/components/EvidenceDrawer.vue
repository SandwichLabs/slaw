<script setup lang="ts">
// EvidenceDrawer — the one shared drawer that shows the evidence of a value: the
// receipt of the run that made it (docs/plans/run-receipts.md, R4a). Opening it
// from another value replaces what it shows (useEvidenceDrawer). Esc closes it
// and gives focus back to the button that opened it; focus moves into it when it
// opens. It is a panel beside the page, not a modal: Tab can leave it.
//
// It shows what a receipt holds and nothing else: no result value, ever. The
// states: a receipt (replayable, live only, not recorded; parameter values kept
// or not), "no receipt was kept for this run" (with the reason class and the
// SQL and connection the window still has), and a receipt opened from an id
// alone (no value to show). Text interpolation only.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Icon from './Icon.vue'
import EvidenceSummary from './EvidenceSummary.vue'
import EvidenceQuery from './EvidenceQuery.vue'
import EvidenceConnections from './EvidenceConnections.vue'
import EvidenceResult from './EvidenceResult.vue'
import EvidenceRun from './EvidenceRun.vue'
import { resetEvidence, useEvidenceDrawer } from '../composables/useEvidenceDrawer'
import { useReceipt } from '../composables/useReceipt'
import { noteText } from '../lib/receiptText'
import { hasWailsRuntime } from '../lib/wails'
import { EventsOn } from '../wailsjs/runtime/runtime'
import '../evidence.css'

const { target, close } = useEvidenceDrawer()
const { receipt, loading, error, load } = useReceipt()
const root = ref<HTMLElement | null>(null)
const copied = ref(false)
let copiedTimer: ReturnType<typeof setTimeout> | undefined

function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape' || e.defaultPrevented) return
  // A dialog over the page owns Escape (its own handler closes it first).
  if (document.querySelector('[aria-modal="true"]')) return
  e.preventDefault()
  close()
}

watch(target, (t, was) => {
  if (!t) {
    document.removeEventListener('keydown', onKeydown)
    return
  }
  if (!was) document.addEventListener('keydown', onKeydown)
  copied.value = false
  void load(t.receiptId)
  // Focus moves in on open, and again when another value replaces the content.
  setTimeout(() => root.value?.querySelector<HTMLElement>('[data-evidence-close]')?.focus(), 0)
}, { immediate: true })

// A receipt of the old workspace must not show over the new one: closing or opening
// a workspace closes the drawer and forgets what the windows remembered.
const offs: Array<() => void> = []
onMounted(() => {
  if (hasWailsRuntime()) offs.push(EventsOn('workspace:closed', resetEvidence), EventsOn('workspace:opened', resetEvidence))
})
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
  clearTimeout(copiedTimer)
  offs.splice(0).forEach((off) => off())
  // The drawer unmounts only with the shell: whatever closed it, the next
  // workspace starts with no target and no remembered runs.
  resetEvidence()
})

async function copyId() {
  const id = receipt.value?.id ?? target.value?.receiptId
  if (!id) return
  try {
    await navigator.clipboard.writeText(id)
    copied.value = true
    clearTimeout(copiedTimer)
    copiedTimer = setTimeout(() => { copied.value = false }, 2000)
  } catch {
    copied.value = false // the clipboard is not available here; the id is on screen to copy by hand
  }
}
</script>

<template>
  <aside
    v-if="target"
    ref="root"
    class="ev-drawer"
    role="complementary"
    aria-labelledby="ev-title"
    tabindex="-1"
    data-testid="evidence-drawer"
  >
    <header class="ev-head">
      <div style="min-width: 0;">
        <div class="ev-eyebrow">EVIDENCE</div>
        <h2 id="ev-title" class="ev-title" data-testid="evidence-title">{{ target.title }}</h2>
      </div>
      <button
        type="button"
        class="btn btn-ghost btn-sm"
        data-evidence-close
        data-testid="evidence-close"
        aria-label="Close evidence (Esc)"
        title="Close evidence (Esc)"
        @click="close()"
      ><Icon name="x" :size="16" /></button>
    </header>

    <div class="ev-body">
      <p v-if="loading" class="help" style="margin: var(--sh-space-4);" role="status" data-testid="evidence-loading">Loading the receipt…</p>
      <div v-else-if="error" class="note attn" role="alert" data-testid="evidence-error"><Icon name="alert" :size="16" /><span>{{ error }}</span></div>

      <template v-else-if="receipt">
        <EvidenceSummary :receipt="receipt" />
        <p v-if="target.fromId" class="ev-sec ev-muted" data-testid="evidence-from-id">Receipts hold what was asked, never the answer.</p>
        <EvidenceQuery :receipt="receipt" :memory="target.memory" />
        <EvidenceConnections :connections="receipt.connections ?? []" />
        <EvidenceResult :receipt="receipt" :from-id="target.fromId" />
        <EvidenceRun :receipt="receipt" />
      </template>

      <template v-else>
        <div class="note warn" role="status" data-testid="evidence-no-receipt">
          <Icon name="alert" :size="16" />
          <span><b>No receipt was kept for this run.</b> {{ noteText(target.note) }}</span>
        </div>
        <section v-if="target.memory?.sql || target.memory?.connection" class="ev-sec" data-testid="evidence-memory">
          <h3 class="ev-h">This run, from memory</h3>
          <pre v-if="target.memory?.sql" class="ev-code">{{ target.memory.sql }}</pre>
          <p v-if="target.memory?.connection" class="ev-mono"><span class="ev-muted">Connection </span>{{ target.memory.connection }}</p>
        </section>
      </template>
    </div>

    <footer v-if="receipt" class="ev-foot" data-testid="evidence-footer">
      <div class="ev-id">
        <code data-testid="evidence-id">{{ receipt.id }}</code>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          data-testid="evidence-copy"
          aria-label="Copy receipt id"
          title="Copy receipt id"
          @click="copyId"
        ><Icon :name="copied ? 'check' : 'copy'" :size="14" /></button>
        <span v-if="copied" class="ev-muted" role="status" data-testid="evidence-copied">Copied</span>
      </div>
      <p class="ev-muted">Kept on this computer, in <code>.slaw-local/</code> (never in git or a bundle). <code>slaw receipts show</code> prints it.</p>
    </footer>
  </aside>
</template>
