// The Evidence button on data-bound A2UI values: stat, table and the charts show
// it when the run behind them has a receipt (or a note saying why not), and
// nothing for inline data. It opens the shared drawer; a component's run says
// which notebook cell and component asked.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import A2UIPage from './A2UIPage.vue'
import { _resetQueryPoolForTests } from './query'
import { useEvidenceDrawer, _resetEvidenceForTests } from '../composables/useEvidenceDrawer'
import { _resetRunMemoryForTests } from '../composables/useReceipt'
import type { A2UIPageT } from './types'

function setApp(app: Record<string, any>) {
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
}
const ok = (extra: Record<string, unknown> = {}) => ({ columns: ['n', 'm'], types: ['BIGINT', 'BIGINT'], rows: [[1, 2], [3, 4]], truncated: false, ...extra })
const page = (c: Record<string, unknown>): A2UIPageT => ({ title: 'P', components: [{ id: 'c1', ...c } as any] })

async function render(p: A2UIPageT, source?: { notebook?: string; cell?: string }) {
  const w = mount(A2UIPage, { props: { page: p, source }, global: { stubs: { Icon: true } }, attachTo: document.body })
  await flushPromises()
  return w
}

describe('A2UI evidence button', () => {
  beforeEach(() => { _resetQueryPoolForTests(); _resetEvidenceForTests(); _resetRunMemoryForTests() })

  it('a bound stat with a receipt shows "Show evidence for <label>" and opens the drawer', async () => {
    setApp({ RunA2UIQuery: vi.fn(async () => ok({ receipt_id: 'r_1' })) })
    const w = await render(page({ type: 'stat', label: 'Revenue', sql: 'SELECT 1' }))
    const btn = w.find('[data-testid="a2ui-evidence"]')
    expect(btn.exists()).toBe(true)
    expect(btn.attributes('aria-label')).toBe('Show evidence for Revenue')
    await btn.trigger('click')
    expect(useEvidenceDrawer().target.value).toMatchObject({ title: 'Revenue', receiptId: 'r_1' })
    expect(useEvidenceDrawer().target.value?.memory).toEqual({ sql: 'SELECT 1', connection: undefined })
    w.unmount()
  })

  it('a value with no receipt and no note gets no button', async () => {
    setApp({ RunA2UIQuery: vi.fn(async () => ok()) })
    const w = await render(page({ type: 'stat', label: 'Revenue', sql: 'SELECT 1' }))
    expect(w.find('[data-testid="a2ui-evidence"]').exists()).toBe(false)
    w.unmount()
  })

  it('inline data has no run, so no button', async () => {
    setApp({})
    const w = await render(page({ type: 'stat', label: 'Revenue', value: 5 }))
    expect(w.find('[data-testid="a2ui-evidence"]').exists()).toBe(false)
    w.unmount()
  })

  it('a note without a receipt still shows the button, and the drawer gets the note', async () => {
    setApp({ RunA2UIQuery: vi.fn(async () => ok({ receipt_note: 'not_kept' })) })
    const w = await render(page({ type: 'table', label: 'Orders', sql: 'SELECT 1' }))
    await w.find('[data-testid="a2ui-evidence"]').trigger('click')
    expect(useEvidenceDrawer().target.value).toMatchObject({ title: 'Orders', note: 'not_kept' })
    expect(useEvidenceDrawer().target.value?.receiptId).toBeUndefined()
    w.unmount()
  })

  it('the table, the bar and line charts and a KINETIC chart each carry the button', async () => {
    setApp({ RunA2UIQuery: vi.fn(async () => ok({ receipt_id: 'r_2' })) })
    for (const type of ['table', 'barChart', 'lineChart', 'columnChart', 'areaChart', 'ringChart', 'radarChart', 'heatmap', 'streamChart', 'sankeyChart']) {
      _resetQueryPoolForTests()
      const w = await render(page({ type, label: `L-${type}`, sql: 'SELECT 1' }))
      const btn = w.find('[data-testid="a2ui-evidence"]')
      expect(btn.exists(), type).toBe(true)
      expect(btn.attributes('aria-label'), type).toBe(`Show evidence for L-${type}`)
      w.unmount()
    }
  })

  it('a component without a label is titled by its id', async () => {
    setApp({ RunA2UIQuery: vi.fn(async () => ok({ receipt_id: 'r_3' })) })
    const w = await render(page({ type: 'barChart', sql: 'SELECT 1' }))
    expect(w.find('[data-testid="a2ui-evidence"]').attributes('aria-label')).toBe('Show evidence for c1')
    w.unmount()
  })

  it('a page with a source tells Go which component asked; a page without one runs a plain render', async () => {
    const withIn = vi.fn(async () => ok({ receipt_id: 'r_4' }))
    const plain = vi.fn(async () => ok())
    setApp({ RunA2UIQueryIn: withIn, RunA2UIQuery: plain })
    let w = await render(page({ type: 'stat', label: 'A', sql: 'SELECT 1' }), { notebook: 'nb1', cell: 'cell9' })
    expect(withIn).toHaveBeenCalledWith('SELECT 1', '', { kind: 'a2ui', notebook: 'nb1', cell: 'cell9', component: 'c1' })
    expect(plain).not.toHaveBeenCalled()
    w.unmount()

    _resetQueryPoolForTests()
    w = await render(page({ type: 'stat', label: 'A', sql: 'SELECT 2' }))
    expect(plain).toHaveBeenCalledWith('SELECT 2')
    w.unmount()
  })

  it('a chart’s own query goes with the source too', async () => {
    const withIn = vi.fn(async () => ok({ receipt_id: 'r_5' }))
    setApp({ RunA2UIQueryIn: withIn })
    const w = await render(page({ type: 'columnChart', label: 'C', sql: 'SELECT 1' }), { notebook: 'nb1', cell: 'cell9' })
    expect(withIn).toHaveBeenCalledWith('SELECT 1', '', { kind: 'a2ui', notebook: 'nb1', cell: 'cell9', component: 'c1' })
    w.unmount()
  })
})
