// useReceipt — fetch one receipt by id for the Evidence drawer, and remember what
// the run that made it had in memory.
//
// A receipt never holds a result value, and with "Keep parameter values" off it
// holds no parameter value either. The window that ran the statement still has
// them for as long as it lives, so the caller that ran it (the Query Explorer, a
// notebook cell, a component) hands them to rememberRun with the id. The drawer
// reads them back from here; it never asks Go, and a drawer opened later with no
// run in memory (from an id, or after a restart) says "not kept".
import { ref } from 'vue'
import { App } from '../lib/bindings'
import { errorText } from '../lib/errors'

/** What the run had in memory, never persisted. Values are text. */
export interface RunMemory {
  sql?: string
  connection?: string
  params?: Record<string, string>
}

const MAX_REMEMBERED = 200
const memory = new Map<string, RunMemory>()

/** Remember the in-memory side of a run by receipt id (the newest MAX_REMEMBERED). */
export function rememberRun(id: string | undefined, run: RunMemory): void {
  if (!id) return
  memory.delete(id)
  memory.set(id, run)
  while (memory.size > MAX_REMEMBERED) {
    const oldest = memory.keys().next().value
    if (oldest === undefined) break
    memory.delete(oldest)
  }
}

export function recallRun(id: string | undefined): RunMemory | undefined {
  return id ? memory.get(id) : undefined
}

/** Parameter values as text, for a RunMemory. */
export function paramText(params: Record<string, unknown> | undefined): Record<string, string> | undefined {
  if (!params || Object.keys(params).length === 0) return undefined
  return Object.fromEntries(Object.entries(params).map(([k, v]) => [k, String(v)]))
}

export function useReceipt() {
  const receipt = ref<ReceiptRecord | null>(null)
  const loading = ref(false)
  const error = ref('')
  // A later load wins: an answer for an id that is no longer wanted is dropped.
  let seq = 0

  async function load(id: string | undefined) {
    const mine = ++seq
    receipt.value = null
    error.value = ''
    if (!id) {
      loading.value = false
      return
    }
    loading.value = true
    try {
      const r = await App.GetReceipt(id)
      if (mine === seq) receipt.value = r
    } catch (e) {
      if (mine === seq) error.value = errorText(e)
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  return { receipt, loading, error, load }
}

export function _resetRunMemoryForTests() {
  memory.clear()
}
