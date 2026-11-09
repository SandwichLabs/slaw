import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import RecentReceipts from './RecentReceipts.vue'
import { useEvidenceDrawer, _resetEvidenceForTests } from '../composables/useEvidenceDrawer'

function line(over: Partial<ReceiptSummary> = {}): ReceiptSummary {
  return {
    id: 'r_aaaaaaaaaaaaaaaaaaaa', at: '2026-11-09T10:00:00Z', duration_ms: 3, surface: 'notebook_cell', actor: 'person',
    source: { kind: 'cell', ref: 'notebooks/n.slnb#c1' }, verdict: 'read', rows: { returned: 3, total: 3, truncated: false },
    sql: 'SELECT 1', ...over,
  }
}

function setApp(list: ReceiptSummary[] | Error, capped = false) {
  const app = { ListReceipts: vi.fn(async () => { if (list instanceof Error) throw list; return { receipts: list, capped } }) }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

describe('RecentReceipts', () => {
  beforeEach(() => _resetEvidenceForTests())

  it('lists the newest receipts with when, who, rows and ref, and asks Go for ten', async () => {
    const app = setApp([line(), line({ id: 'r_bbbbbbbbbbbbbbbbbbbb', sql: undefined, sql_withheld: true, rows: { returned: 1, total: 1, truncated: false } })])
    const w = mount(RecentReceipts, { props: { count: 2 } })
    await flushPromises()
    expect(app.ListReceipts).toHaveBeenCalledWith({ surface: '', limit: 10 })
    const first = w.find('[data-testid="recent-r_aaaaaaaaaaaaaaaaaaaa"]').text()
    expect(first).toContain('SELECT 1')
    expect(first).toContain('You, opening the notebook')
    expect(first).toContain('3 rows')
    expect(first).toContain('notebooks/n.slnb#c1')
    const second = w.find('[data-testid="recent-r_bbbbbbbbbbbbbbbbbbbb"]').text()
    expect(second).toContain('(statement not kept)')
    expect(second).toContain('1 row')
  })

  it('a line opens the Evidence drawer by its id alone', async () => {
    setApp([line()])
    const w = mount(RecentReceipts, { props: { count: 1 } })
    await flushPromises()
    const btn = w.find('[data-testid="recent-show"]')
    expect(btn.attributes('aria-label')).toBe('Show evidence for r_aaaaaaaaaaaaaaaaaaaa')
    await btn.trigger('click')
    expect(useEvidenceDrawer().target.value).toMatchObject({ title: 'r_aaaaaaaaaaaaaaaaaaaa', receiptId: 'r_aaaaaaaaaaaaaaaaaaaa', fromId: true })
  })

  it('says so when there are none, and reloads when the count changes', async () => {
    const app = setApp([])
    const w = mount(RecentReceipts, { props: { count: 0 } })
    await flushPromises()
    expect(w.find('[data-testid="recent-receipts-empty"]').exists()).toBe(true)
    app.ListReceipts.mockResolvedValue({ receipts: [line()], capped: false })
    await w.setProps({ count: 1 })
    await flushPromises()
    expect(app.ListReceipts).toHaveBeenCalledTimes(2)
    expect(w.findAll('[data-testid="recent-show"]')).toHaveLength(1)
  })

  it('a failed list is an error line, not a throw', async () => {
    setApp(new Error('receipts are not being kept for this workspace'))
    const w = mount(RecentReceipts, { props: { count: 0 } })
    await flushPromises()
    expect(w.find('[data-testid="recent-receipts-error"]').text()).toContain('not being kept')
  })

  it('an empty list from a scan that stopped early says none among the newest, not none', async () => {
    setApp([], true)
    const w = mount(RecentReceipts, { props: { count: 20000 } })
    await flushPromises()
    expect(w.find('[data-testid="recent-receipts-capped"]').text()).toBe('None among the newest 2,000 receipts.')
    expect(w.find('[data-testid="recent-receipts-empty"]').exists()).toBe(false)
  })
})
