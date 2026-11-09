// useRecentReceipts — the newest receipts of this workspace, one line each, for the
// list in Settings > Workspace > Receipts. A line opens the Evidence drawer by id
// (useEvidenceDrawer.openById): there is no value to show, only what was asked.
import { ref } from 'vue'
import { App } from '../lib/bindings'
import { errorText } from '../lib/errors'

export function useRecentReceipts(limit = 10) {
  const rows = ref<ReceiptSummary[]>([])
  const error = ref('')
  // A later refresh wins over an earlier answer that arrives late.
  let seq = 0

  async function refresh() {
    const mine = ++seq
    try {
      const got = await App.ListReceipts({ surface: '', limit })
      if (mine === seq) { rows.value = got ?? []; error.value = '' }
    } catch (e) {
      if (mine === seq) { rows.value = []; error.value = errorText(e) }
    }
  }

  return { rows, error, refresh }
}
