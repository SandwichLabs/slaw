// useReceiptSettings — Settings > Workspace > Receipts: this person's retention
// and privacy setting for run receipts, what is kept now, and Delete all. Go
// clamps every limit (the floors and ceilings it returns are only hints for the
// help line), so typing is never blocked: after a save the form shows the value
// in force.
import { ref, computed } from 'vue'
import { App } from '../lib/bindings'
import { errorText } from '../lib/errors'
import { askConfirm } from './useConfirm'

export function useReceiptSettings() {
  const info = ref<ReceiptSettingsInfo | null>(null)
  const keepParams = ref(true)
  // The number fields are strings while typing, so an empty box is not a 0.
  const days = ref('')
  const maxCount = ref('')
  const maxMB = ref('')
  const busy = ref(false)
  const message = ref('')
  const error = ref('')

  function fill(i: ReceiptSettingsInfo) {
    info.value = i
    keepParams.value = i.effective.keepParams
    days.value = String(i.effective.days)
    maxCount.value = String(i.effective.maxCount)
    maxMB.value = String(i.effective.maxMB)
  }

  async function refresh() {
    try {
      fill(await App.GetReceiptSettings())
      error.value = ''
    } catch (e) {
      info.value = null
      error.value = errorText(e)
    }
  }

  // A blank or non-numeric box means "the default" (0 is unset in Go).
  const num = (v: string) => { const n = Math.trunc(Number(v)); return Number.isFinite(n) && n > 0 ? n : 0 }

  async function save() {
    busy.value = true
    error.value = ''
    message.value = ''
    try {
      await App.SetReceiptSettings({ keepParams: keepParams.value, days: num(days.value), maxCount: num(maxCount.value), maxMB: num(maxMB.value) })
      await refresh()
      message.value = 'Saved. A lower limit removes the oldest receipts now.'
    } catch (e) {
      error.value = errorText(e)
    }
    busy.value = false
  }

  async function clearAll() {
    const n = info.value?.usage.count ?? 0
    const ok = await askConfirm({
      title: 'Delete all receipts?',
      message: `This deletes ${n} receipt${n === 1 ? '' : 's'} on this computer. It cannot be undone. Your settings stay.`,
      confirmLabel: 'Delete all receipts',
    })
    if (!ok) return
    busy.value = true
    error.value = ''
    message.value = ''
    try {
      await App.ClearReceipts()
      await refresh()
      message.value = 'All receipts deleted.'
    } catch (e) {
      error.value = errorText(e)
    }
    busy.value = false
  }

  // Unsaved edits, compared with what is in force.
  const dirty = computed(() => {
    const e = info.value?.effective
    if (!e) return false
    return keepParams.value !== e.keepParams || num(days.value) !== e.days || num(maxCount.value) !== e.maxCount || num(maxMB.value) !== e.maxMB
  })

  return { info, keepParams, days, maxCount, maxMB, busy, message, error, dirty, refresh, save, clearAll }
}

/** "12.3 MB", "4.0 KB" or "512 bytes". */
export function formatBytes(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB'
  return n + ' bytes'
}

/** The usage line: "N receipts · X MB · oldest from <date>". */
export function usageLine(u: ReceiptUsage): string {
  const parts = [`${u.count} receipt${u.count === 1 ? '' : 's'}`, formatBytes(u.bytes)]
  if (u.oldest) {
    const t = new Date(u.oldest)
    parts.push('oldest from ' + (isNaN(t.getTime()) ? u.oldest : t.toLocaleDateString()))
  }
  return parts.join(' · ')
}
