// useEvidenceDrawer — the one app-level open/close state of the Evidence drawer.
// Opening it from another value replaces what it shows. The element that opened
// it is kept so Esc (or Close) hands focus back to it.
import { ref, readonly } from 'vue'
import { clearRunMemory, recallRun, type RunMemory } from './useReceipt'

export interface EvidenceTarget {
  /** The title of the value: a stat label, a cell or chart title, or the id when opened from one. */
  title: string
  /** The receipt to show. Empty when none was kept (then `note` says why). */
  receiptId?: string
  /** Why no receipt was kept, when `receiptId` is empty: "not_kept" or "unavailable". */
  note?: string
  /** What the run had in memory (parameter values, the SQL). Defaults to what rememberRun has for the id. */
  memory?: RunMemory
  /** Opened from an id alone: there is no value to show, only what was asked. */
  fromId?: boolean
}

const target = ref<EvidenceTarget | null>(null)
let trigger: HTMLElement | null = null

function elementOf(from: Event | HTMLElement | null | undefined): HTMLElement | null {
  if (!from) return null
  if (from instanceof HTMLElement) return from
  const el = (from.currentTarget ?? from.target) as HTMLElement | null
  return el instanceof HTMLElement ? el : null
}

/** Show the evidence of a value. `from` is the click (or the button) that asked, for the focus return. */
export function openEvidence(t: EvidenceTarget, from?: Event | HTMLElement | null): void {
  trigger = elementOf(from) ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null)
  target.value = { ...t, memory: t.memory ?? recallRun(t.receiptId) }
}

/** Show a receipt by its id alone: no value, only what was asked. */
export function openEvidenceById(id: string, from?: Event | HTMLElement | null): void {
  openEvidence({ title: id, receiptId: id, fromId: true }, from)
}

/**
 * Close the drawer and give focus back to the button that opened it. When that
 * button has left the page (its value re-rendered, or the view changed), focus goes
 * to the page's heading or main region, never to the body.
 */
export function closeEvidence(): void {
  const back = trigger
  trigger = null
  target.value = null
  if (back && back !== document.body && document.contains(back)) {
    back.focus()
    return
  }
  const fallback = document.querySelector<HTMLElement>('main h1') ?? document.querySelector<HTMLElement>('main')
  if (fallback) {
    if (!fallback.hasAttribute('tabindex')) fallback.setAttribute('tabindex', '-1')
    fallback.focus()
  }
}

/** A workspace was closed or another opened: what the drawer shows and what the windows remembered belong to the old one. */
export function resetEvidence(): void {
  trigger = null
  target.value = null
  clearRunMemory()
}

export function useEvidenceDrawer() {
  return { target: readonly(target), open: openEvidence, openById: openEvidenceById, close: closeEvidence }
}

export function _resetEvidenceForTests() {
  target.value = null
  trigger = null
}
