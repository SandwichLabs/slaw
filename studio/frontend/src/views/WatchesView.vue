<script setup lang="ts">
// WatchesView (E7 W2): the workspace's standing questions. Each watch is a file
// under watches/ (a saved query or a metric and a condition). Nothing runs by
// itself: Check runs a watch once through the read-only policy and records the
// outcome in a machine-local history; Check all runs every watch and Stop ends it.
// A fired watch shows its reason and, for a "rows" check, the first rows it read.
// State and actions live in useWatches, useWatchChecks, useWatchHistory and
// useWatchForm; the nav badge is useWatchBadge.
import { computed, nextTick, reactive } from 'vue'
import Icon from '../components/Icon.vue'
import WatchRow from '../components/WatchRow.vue'
import WatchAlerts from '../components/WatchAlerts.vue'
import WatchHistoryPanel from '../components/WatchHistoryPanel.vue'
import WatchForm from '../components/WatchForm.vue'
import { useWatches } from '../composables/useWatches'
import { useWatchChecks } from '../composables/useWatchChecks'
import { useWatchHistory } from '../composables/useWatchHistory'
import { useWatchForm } from '../composables/useWatchForm'
import { refreshWatchBadge } from '../composables/useWatchBadge'
import { askConfirm } from '../composables/useConfirm'
import { workspaceInfo } from '../composables/useWorkspace'
import { App } from '../lib/bindings'
import { errorText } from '../lib/errors'
import { newest } from '../lib/watchText'
import { ref } from 'vue'

const { list, loading, error, reload } = useWatches()
const history = reactive(useWatchHistory())
const checks = reactive(useWatchChecks(async () => { await reload(); await history.refresh(); await refreshWatchBadge() }))
// A saved watch is a different question: what this window remembers of the old file's check goes.
const form = reactive(useWatchForm(async (name) => { checks.forget(name); await reload(); await history.refresh() }))
const readOnly = computed(() => !!workspaceInfo.value?.readOnly)
const actionError = ref('')

const watches = computed(() => list.value?.watches ?? [])
// A watch whose newest check fired: this window's result wins over the history's.
const fired = computed(() => watches.value.filter((w) => {
  return !w.error && (newest(w, checks.results[w.name]).entry?.status ?? w.last_status) === 'alert'
}))
const checkable = computed(() => watches.value.some((w) => !w.error))

// Closing the history gives focus back to the button that opened it.
async function closeHistory() {
  const name = history.name
  history.close()
  await nextTick()
  document.querySelector<HTMLElement>(`[data-testid="history-${name}"]`)?.focus()
}
function toggleHistory(name: string) {
  if (history.name === name) void closeHistory()
  else void history.open(name)
}

async function remove(w: WatchSummary) {
  const ok = await askConfirm({
    title: `Delete ${w.name}?`,
    message: 'The watch file is removed from the workspace folder. Its recorded checks stay on this computer.',
    variant: 'destructive',
    confirmLabel: 'Delete watch',
  })
  if (!ok) return
  actionError.value = ''
  try {
    await App.DeleteWatch(w.name, w.rev ?? '')
    checks.forget(w.name)
    if (history.name === w.name) history.close()
  } catch (e) {
    actionError.value = errorText(e)
  }
  await reload()
  await refreshWatchBadge()
}
</script>

<template>
  <div class="watches" data-testid="watches-view">
    <header class="watches__head">
      <div class="watches__title">
        <h2 class="card-title" style="margin: 0; font-size: 28px;">Watches</h2>
        <p class="help" style="margin: 0;">Standing questions with a condition. Nothing runs until you press Check. Each watch is a file in <code>watches/</code>.</p>
      </div>
      <div class="watches__actions">
        <button class="btn" type="button" :disabled="readOnly" data-testid="new-watch" @click="form.openNew()"><Icon name="plus" :size="16" /> New watch</button>
        <button v-if="!checks.busy" class="btn btn-primary" type="button" :disabled="!checkable" data-testid="check-all" @click="checks.check()"><Icon name="play" :size="16" /> Check all</button>
        <button v-else class="btn" type="button" data-testid="stop-checks" @click="checks.stop()"><Icon name="x" :size="16" /> Stop</button>
      </div>
    </header>

    <!-- The live region changes twice per run (started, finished); the seconds counter beside it is not announced. -->
    <div class="live">
      <span role="status" aria-live="polite" data-testid="watch-run-status">{{ checks.busy ? `Checking ${checks.checkingAll ? 'all watches' : 'a watch'}…` : checks.summary }}</span>
      <span v-if="checks.busy" class="live__elapsed" data-testid="watch-elapsed">{{ checks.elapsed }} s</span>
      <span v-else-if="checks.cancelled" data-testid="watch-stopped-note">The watches checked before it keep their results.</span>
    </div>
    <div v-if="checks.error" class="note attn" role="alert" data-testid="watch-check-error"><Icon name="alert" :size="16" /><span>{{ checks.error }}</span></div>
    <div v-if="actionError" class="note attn" role="alert" data-testid="watch-action-error"><Icon name="alert" :size="16" /><span>{{ actionError }}</span></div>
    <div v-if="list?.history_error" class="note warn" role="status" data-testid="watch-history-warning"><Icon name="alert" :size="16" /><span>The history could not be read, so last checks are not shown: {{ list.history_error }}</span></div>

    <div v-if="loading" class="sh-mono help">Loading…</div>
    <div v-else-if="error" class="note attn" role="alert" data-testid="watches-error"><Icon name="alert" :size="16" /><span>{{ error }}</span></div>
    <div v-else-if="!watches.length" class="col watches__empty" data-testid="watches-empty">
      <Icon name="eye" :size="40" />
      <h3>No watches yet</h3>
      <p>A watch asks a saved query or a metric a question and fires when the answer meets a condition: orders stuck in pending, or a table that should never have rows.</p>
      <p><button class="btn btn-primary" type="button" :disabled="readOnly" @click="form.openNew()">New watch</button> or write <code>watches/stuck_orders.yaml</code> yourself; it appears here when it is saved. <code>docs/reference/watches.md</code> has the format.</p>
    </div>
    <template v-else>
      <WatchAlerts v-if="fired.length" :alerts="fired" :results="checks.results" />
      <ul class="watches__list" aria-label="Watches" data-testid="watch-list">
        <WatchRow
          v-for="w in watches" :key="w.name" :watch="w" :result="checks.results[w.name]" :phase="checks.phase(w.name)" :busy="checks.busy"
          :read-only="readOnly" :history-open="history.name === w.name"
          @check="checks.check([w.name])" @edit="form.openEdit(w.name)" @history="toggleHistory(w.name)" @remove="remove(w)"
        />
      </ul>
      <WatchHistoryPanel v-if="history.name" :name="history.name" :entries="history.entries" :loading="history.loading" :error="history.error" @close="closeHistory()" />
    </template>

    <WatchForm :form="form" />
  </div>
</template>

<style scoped>
.watches { display: flex; flex-direction: column; gap: var(--sh-space-4); padding: var(--sh-space-6); height: 100%; overflow: auto; min-width: 0; }
.watches__head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--sh-space-4); flex-wrap: wrap; }
.watches__title { display: flex; flex-direction: column; gap: var(--sh-space-1); min-width: 0; flex: 1 1 320px; }
.watches__actions { display: flex; gap: var(--sh-space-2); flex-wrap: wrap; }
.watches__list { list-style: none; margin: 0; padding: 0; border-top: var(--sh-border-thin); }
.watches__empty { max-width: 640px; margin: var(--sh-space-8) auto 0; color: var(--sh-graphite); gap: var(--sh-space-3); }
.watches__empty h3 { color: var(--sh-ink); margin: 0; }
.live { min-height: 1.4em; font-size: var(--sh-fs-sm); color: var(--sh-graphite); display: flex; gap: var(--sh-space-3); flex-wrap: wrap; }
</style>
