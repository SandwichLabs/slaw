// The Evidence drawer across a workspace switch and its focus fallbacks: a receipt of
// the old workspace must not show over the new one, and focus never falls to the body.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'

const handlers: Record<string, Array<() => void>> = {}
vi.mock('../wailsjs/runtime/runtime', () => ({
  EventsOn: (name: string, fn: () => void) => {
    ;(handlers[name] ??= []).push(fn)
    return () => { handlers[name] = (handlers[name] ?? []).filter((f) => f !== fn) }
  },
  EventsOff: () => {},
}))

import EvidenceDrawer from './EvidenceDrawer.vue'
import { openEvidence, useEvidenceDrawer, closeEvidence, _resetEvidenceForTests } from '../composables/useEvidenceDrawer'
import { rememberRun, recallRun, _resetRunMemoryForTests } from '../composables/useReceipt'

const receipt = { id: 'r_0123456789abcdef0123', at: '2026-11-09T10:00:00Z', duration_ms: 1, surface: 'query_editor', actor: 'person', source: {}, verdict: 'unchecked', rows: { returned: 1, total: 1, truncated: false }, result_hmac: 'a'.repeat(64) }
const emit = (name: string) => (handlers[name] ?? []).forEach((f) => f())

let w: VueWrapper | null = null
beforeEach(() => {
  _resetEvidenceForTests(); _resetRunMemoryForTests()
  for (const k of Object.keys(handlers)) delete handlers[k]
  ;(window as any).runtime = {}
  ;(window as any).go = { main: { App: { GetReceipt: vi.fn(async () => receipt) } } }
})
afterEach(() => { w?.unmount(); w = null; delete (window as any).runtime; document.body.innerHTML = '' })

describe('EvidenceDrawer across a workspace switch', () => {
  it('closes on workspace:closed and on workspace:opened, and forgets the remembered runs', async () => {
    w = mount(EvidenceDrawer, { attachTo: document.body, global: { stubs: { Icon: true } } })
    for (const ev of ['workspace:closed', 'workspace:opened']) {
      rememberRun('r_0123456789abcdef0123', { sql: 'SELECT 1', params: { a: '1' } })
      openEvidence({ title: 'Old workspace value', receiptId: 'r_0123456789abcdef0123' })
      await flushPromises()
      expect(w.find('[data-testid="evidence-drawer"]').exists()).toBe(true)
      emit(ev)
      await flushPromises()
      expect(w.find('[data-testid="evidence-drawer"]').exists(), ev).toBe(false)
      expect(useEvidenceDrawer().target.value).toBeNull()
      expect(recallRun('r_0123456789abcdef0123')).toBeUndefined()
    }
  })

  it('stops listening for Esc and for the events once unmounted', async () => {
    w = mount(EvidenceDrawer, { attachTo: document.body, global: { stubs: { Icon: true } } })
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    w.unmount(); w = null
    expect(handlers['workspace:closed']).toHaveLength(0)
    // An Esc after unmount must not close what a new mount shows.
    w = mount(EvidenceDrawer, { attachTo: document.body, global: { stubs: { Icon: true } } })
    await flushPromises()
    // The state is app-level, so the new mount shows it at once and its Esc listener is live.
    expect(w.find('[data-testid="evidence-drawer"]').exists()).toBe(true)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(w.find('[data-testid="evidence-drawer"]').exists()).toBe(false)
  })
})

describe('closeEvidence focus', () => {
  it('falls back to the page heading, not the body, when the opener has left the page', () => {
    const main = document.createElement('main')
    main.innerHTML = '<h1>Notebook</h1><button id="b">x</button>'
    document.body.appendChild(main)
    const btn = main.querySelector('button') as HTMLElement
    openEvidence({ title: 't', receiptId: 'r_1' }, btn)
    btn.remove()
    closeEvidence()
    expect(document.activeElement).toBe(main.querySelector('h1'))
    main.remove()
  })

  it('falls back to the main region when there is no heading', () => {
    const main = document.createElement('main')
    document.body.appendChild(main)
    openEvidence({ title: 't', receiptId: 'r_1' })
    closeEvidence()
    expect(document.activeElement).toBe(main)
    main.remove()
  })
})
