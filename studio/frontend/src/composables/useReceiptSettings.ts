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
  // Which limits Go moved to a bound on the last save.
  const clamped = ref<string[]>([])

  // The boxes show what is saved: a blank one is "the default" (the placeholder
  // names it), so saving one change never pins the others to today's defaults.
  function fill(i: ReceiptSettingsInfo) {
    info.value = i
    keepParams.value = i.settings.keepParams
    days.value = i.settings.days ? String(i.settings.days) : ''
    maxCount.value = i.settings.maxCount ? String(i.settings.maxCount) : ''
    maxMB.value = i.settings.maxMB ? String(i.settings.maxMB) : ''
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

  // A blank box is the default (0 in Go). Anything else must be a positive whole
  // number; null says it is not (0 and negatives are refused, never the default).
  const num = (raw: string | number): number | null => {
    const v = String(raw ?? '')
    if (v.trim() === '') return 0
    const n = Number(v)
    return Number.isInteger(n) && n > 0 ? n : null
  }

  async function save() {
    const d = num(days.value), c = num(maxCount.value), m = num(maxMB.value)
    if (d === null || c === null || m === null) {
      error.value = 'Each limit must be a positive whole number, or blank for the default.'
      message.value = ''
      return
    }
    busy.value = true
    error.value = ''
    message.value = ''
    try {
      await App.SetReceiptSettings({ keepParams: keepParams.value, days: d, maxCount: c, maxMB: m })
      await refresh()
      const st = info.value?.settings
      const rows: Array<[string, number, number, string]> = st
        ? [['Keep for', d, st.days, 'days'], ['At most', c, st.maxCount, 'receipts'], ['Using at most', m, st.maxMB, 'MB']]
        : []
      clamped.value = rows
        .filter(([, typed, saved]) => typed !== 0 && typed !== saved)
        .map(([label, typed, saved, unit]) => `${label} ${typed} was outside the allowed range; ${saved} ${unit} is in force`)
      message.value = 'Saved. A lower limit removes the oldest receipts now.'
    } catch (e) {
      error.value = errorText(e)
      await refresh() // show what is saved now (it may have changed elsewhere)
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
    const e = info.value?.settings
    if (!e) return false
    return keepParams.value !== e.keepParams || num(days.value) !== e.days || num(maxCount.value) !== e.maxCount || num(maxMB.value) !== e.maxMB
  })

  return { info, keepParams, days, maxCount, maxMB, busy, message, error, clamped, dirty, refresh, save, clearAll }
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
