import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'

import EvidenceDrawer from './EvidenceDrawer.vue'
import { openEvidence, openEvidenceById, closeEvidence, _resetEvidenceForTests } from '../composables/useEvidenceDrawer'
import { rememberRun, _resetRunMemoryForTests } from '../composables/useReceipt'

const HMAC = '9c1e04aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab7a4'

function receipt(over: Partial<ReceiptRecord> = {}): ReceiptRecord {
  return {
    id: 'r_0123456789abcdef0123',
    at: '2026-11-09T10:00:00Z',
    duration_ms: 42,
    surface: 'query_editor',
    actor: 'person',
    source: { kind: 'saved_query', ref: 'queries/net_revenue.sql' },
    sql: 'SELECT sum(amount) FROM orders WHERE day >= $start',
    run_sql: 'SELECT * FROM (SELECT sum(amount) FROM orders WHERE day >= $start) AS _a2ui_limit LIMIT 501',
    params: { start: { value: '2026-07-01' } },
    params_hash: 'ab'.repeat(32),
    connections: [{ name: 'lake', type: 'ducklake', read_only: true, status: 'connected', snapshot: { fact: 'snapshot id', value: '4812', label: 'replayable' } }],
    verdict: 'unchecked',
    rows: { returned: 3, total: 3, truncated: false },
    columns: [{ name: 'total', type: 'DECIMAL(18,2)' }, { name: '', type: 'VARCHAR', withheld: true }],
    result_hmac: HMAC,
    ...over,
  }
}

function makeApp(r: ReceiptRecord | Error) {
  const app = { GetReceipt: vi.fn(async () => { if (r instanceof Error) throw r; return r }) }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

let wrappers: VueWrapper[] = []
async function mountDrawer() {
  const w = mount(EvidenceDrawer, { attachTo: document.body, global: { stubs: { Icon: true, 'router-link': { template: '<a data-testid="evidence-settings-link"><slot /></a>' } } } })
  wrappers.push(w)
  return w
}
const text = (w: VueWrapper, id: string) => w.find(`[data-testid="${id}"]`).text()

describe('EvidenceDrawer', () => {
  beforeEach(() => { _resetEvidenceForTests(); _resetRunMemoryForTests() })
  afterEach(() => { wrappers.forEach((w) => w.unmount()); wrappers = []; document.body.innerHTML = '' })

  it('is not on the page until something opens it', async () => {
    makeApp(receipt())
    const w = await mountDrawer()
    expect(w.find('[data-testid="evidence-drawer"]').exists()).toBe(false)
  })

  it('shows the sections of a replayable receipt: summary, query, connection, result, run, footer', async () => {
    const app = makeApp(receipt())
    const w = await mountDrawer()
    openEvidence({ title: 'Net revenue', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()

    expect(app.GetReceipt).toHaveBeenCalledWith('r_0123456789abcdef0123')
    expect(text(w, 'evidence-title')).toBe('Net revenue')
    expect(w.text()).toContain('EVIDENCE')
    expect(text(w, 'evidence-summary')).toContain('One query on lake, read at snapshot 4812. Run it at that snapshot and you read the same data.')
    expect(text(w, 'evidence-replay')).toBe('Replayable')
    expect(text(w, 'evidence-verdict')).toBe('Your own query')
    expect(text(w, 'evidence-sql')).toContain('SELECT sum(amount)')
    expect(text(w, 'evidence-params')).toContain('$start')
    expect(text(w, 'evidence-params')).toContain('2026-07-01')
    expect(text(w, 'evidence-source')).toBe('From queries/net_revenue.sql')
    expect(text(w, 'evidence-conn-lake')).toContain('ducklake')
    expect(text(w, 'evidence-snapshot-value')).toBe('4812')
    expect(text(w, 'evidence-replayable')).toContain('current when the run finished')
    expect(text(w, 'evidence-rows')).toBe('3 returned')
    expect(text(w, 'evidence-columns')).toContain('total')
    expect(text(w, 'evidence-columns')).toContain('(name withheld)')
    expect(text(w, 'evidence-fingerprint')).toContain('9c1e04…b7a4')
    expect(text(w, 'evidence-result')).toContain('The receipt keeps a fingerprint of the rows, never the rows.')
    expect(text(w, 'evidence-result')).toContain('The value on the card comes from this run, not from the receipt.')
    expect(text(w, 'evidence-run')).toContain('You, in the Query Explorer')
    expect(text(w, 'evidence-from')).toBe('queries/net_revenue.sql')
    expect(text(w, 'evidence-id')).toBe('r_0123456789abcdef0123')
    expect(text(w, 'evidence-footer')).toContain('Kept on this computer, in .slaw-local/ (never in git or a bundle).')
    expect(text(w, 'evidence-footer')).toContain('slaw receipts show')
  })

  it('As written / As run is a pressed-state toggle', async () => {
    makeApp(receipt())
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    const written = w.find('[data-testid="evidence-as-written"]')
    const run = w.find('[data-testid="evidence-as-run"]')
    expect(written.attributes('aria-pressed')).toBe('true')
    expect(run.attributes('aria-pressed')).toBe('false')
    expect(text(w, 'evidence-sql')).not.toContain('_a2ui_limit')
    await run.trigger('click')
    expect(run.attributes('aria-pressed')).toBe('true')
    expect(written.attributes('aria-pressed')).toBe('false')
    expect(text(w, 'evidence-sql')).toContain('_a2ui_limit')
  })

  it('live only: says the moment cannot be read again, with no replay wording', async () => {
    makeApp(receipt({ connections: [{ name: 'pg', type: 'postgres', read_only: true, status: 'connected', snapshot: { fact: 'wal_lsn', value: '0/16B3748', label: 'live_only' } }] }))
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    expect(text(w, 'evidence-replay')).toBe('Live only')
    expect(text(w, 'evidence-live-only')).toContain("Where the source was when it answered. You can't read that moment again.")
    expect(text(w, 'evidence-summary')).not.toContain('Run it at that snapshot')
  })

  it('an iceberg_catalog fact that was not recorded never offers replay', async () => {
    makeApp(receipt({ connections: [{ name: 'cat', type: 'iceberg_catalog', read_only: true, status: 'connected', snapshot: { fact: 'iceberg', value: 'not available for catalog tables', label: 'not_recorded' } }] }))
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    expect(text(w, 'evidence-snapshot')).toContain('Not recorded: not available for catalog tables')
    expect(text(w, 'evidence-replay')).toBe('Not recorded')
    expect(w.text()).not.toMatch(/replayable|same data/i)
  })

  it('a pinned version says so', async () => {
    makeApp(receipt({ connections: [{ name: 'lake', type: 'ducklake', read_only: true, status: 'connected', snapshot: { fact: 'ducklake', value: 'version pinned in the statement', label: 'not_recorded' } }] }))
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    expect(text(w, 'evidence-snapshot')).toContain('Not recorded: version pinned in the statement')
  })

  it('parameter values not kept: shows the open run’s values with a lock note and a Settings link', async () => {
    makeApp(receipt({ params: { start: {} }, params_withheld: true }))
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123', memory: { params: { start: '2026-07-01' } } })
    await flushPromises()
    expect(text(w, 'evidence-params')).toContain('2026-07-01')
    const note = text(w, 'evidence-params-note')
    expect(note).toContain('Shown from this run.')
    expect(note).toContain('keeps only the names and a hash')
    expect(note).toContain('Settings > Workspace > Receipts')
    expect(w.find('[data-testid="evidence-settings-link"]').exists()).toBe(true)
  })

  it('parameter values not kept and no run in memory: "not kept"', async () => {
    makeApp(receipt({ params: { start: {} }, params_withheld: true }))
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    expect(text(w, 'evidence-params')).toContain('not kept')
    expect(text(w, 'evidence-params')).not.toContain('2026-07-01')
    expect(text(w, 'evidence-params-note')).not.toContain('Shown from this run.')
  })

  it('uses what rememberRun kept for the id when the opener gives no memory', async () => {
    makeApp(receipt({ params: { start: {} }, params_withheld: true }))
    rememberRun('r_0123456789abcdef0123', { params: { start: '2026-08-01' } })
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    expect(text(w, 'evidence-params')).toContain('2026-08-01')
  })

  it('a withheld statement says it was not kept, and a withheld param value says withheld', async () => {
    makeApp(receipt({ sql: undefined, sql_withheld: true, params: { token: { withheld: true } } }))
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    expect(text(w, 'evidence-sql-withheld')).toContain('Not kept')
    expect(text(w, 'evidence-params')).toContain('withheld')
  })

  it('no receipt kept: a warn note with the reason class, then the in-memory SQL and connection', async () => {
    const app = makeApp(receipt())
    const w = await mountDrawer()
    openEvidence({ title: 'Total', note: 'not_kept', memory: { sql: 'SELECT 1 AS n', connection: 'lake' } })
    await flushPromises()
    expect(app.GetReceipt).not.toHaveBeenCalled()
    const note = w.find('[data-testid="evidence-no-receipt"]')
    expect(note.classes()).toContain('warn')
    expect(note.text()).toContain('No receipt was kept for this run.')
    expect(note.text()).toContain("couldn't be written")
    expect(text(w, 'evidence-memory')).toContain('SELECT 1 AS n')
    expect(text(w, 'evidence-memory')).toContain('lake')
    expect(w.find('[data-testid="evidence-footer"]').exists()).toBe(false)
  })

  it('no receipt kept because this workspace keeps none says so', async () => {
    makeApp(receipt())
    const w = await mountDrawer()
    openEvidence({ title: 'Total', note: 'unavailable' })
    await flushPromises()
    expect(text(w, 'evidence-no-receipt')).toContain('keeps no receipts')
  })

  it('opened from an id: the title is the id, and the text says receipts hold what was asked, never the answer', async () => {
    makeApp(receipt())
    const w = await mountDrawer()
    openEvidenceById('r_0123456789abcdef0123')
    await flushPromises()
    expect(text(w, 'evidence-title')).toBe('r_0123456789abcdef0123')
    expect(text(w, 'evidence-from-id')).toBe('Receipts hold what was asked, never the answer.')
    expect(text(w, 'evidence-result')).not.toContain('The value on the card')
  })

  it('an id with no receipt shows the error, not a blank drawer', async () => {
    makeApp(new Error('failed to open the receipt: no receipt with that id: "r_x"'))
    const w = await mountDrawer()
    openEvidenceById('r_x')
    await flushPromises()
    expect(text(w, 'evidence-error')).toContain('no receipt with that id')
  })

  it('opening it from another value replaces its content', async () => {
    const app = makeApp(receipt())
    const w = await mountDrawer()
    openEvidence({ title: 'First', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    app.GetReceipt.mockImplementation(async () => receipt({ id: 'r_aaaaaaaaaaaaaaaaaaaa', sql: 'SELECT 2' }))
    openEvidence({ title: 'Second', receiptId: 'r_aaaaaaaaaaaaaaaaaaaa' })
    await flushPromises()
    expect(text(w, 'evidence-title')).toBe('Second')
    expect(text(w, 'evidence-id')).toBe('r_aaaaaaaaaaaaaaaaaaaa')
    expect(text(w, 'evidence-sql')).toBe('SELECT 2')
  })

  it('moves focus in on open; Esc closes it and returns focus to the opener', async () => {
    makeApp(receipt())
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' }, opener)
    await flushPromises()
    await new Promise((r) => setTimeout(r, 5))
    expect(document.activeElement).toBe(w.find('[data-testid="evidence-close"]').element)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(w.find('[data-testid="evidence-drawer"]').exists()).toBe(false)
    expect(document.activeElement).toBe(opener)
  })

  it('Esc does nothing while a modal dialog is open over the page', async () => {
    makeApp(receipt())
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    const modal = document.createElement('div')
    modal.setAttribute('aria-modal', 'true')
    document.body.appendChild(modal)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(w.find('[data-testid="evidence-drawer"]').exists()).toBe(true)
    closeEvidence()
  })

  it('the close button is labelled "Close evidence (Esc)" and closes it', async () => {
    makeApp(receipt())
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    const btn = w.find('[data-testid="evidence-close"]')
    expect(btn.attributes('aria-label')).toBe('Close evidence (Esc)')
    await btn.trigger('click')
    expect(w.find('[data-testid="evidence-drawer"]').exists()).toBe(false)
  })

  it('copy id puts the receipt id on the clipboard and says Copied', async () => {
    makeApp(receipt())
    const writeText = vi.fn(async () => {})
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    const btn = w.find('[data-testid="evidence-copy"]')
    expect(btn.attributes('aria-label')).toBe('Copy receipt id')
    await btn.trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith('r_0123456789abcdef0123')
    expect(text(w, 'evidence-copied')).toBe('Copied')
  })

  it('shows the rows "of M" and truncated when the result was cut', async () => {
    makeApp(receipt({ rows: { returned: 500, total: 1200, truncated: true } }))
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    expect(text(w, 'evidence-rows')).toBe('500 returned of 1,200, truncated')
  })

  it('an agent run says who: The agent (run_query)', async () => {
    makeApp(receipt({ actor: 'agent', surface: 'agent_run_query', verdict: 'read', source: { kind: 'sql' } }))
    const w = await mountDrawer()
    openEvidence({ title: 't', receiptId: 'r_0123456789abcdef0123' })
    await flushPromises()
    expect(text(w, 'evidence-run')).toContain('The agent (run_query)')
    expect(text(w, 'evidence-verdict')).toBe('Read-only check passed')
  })
})
