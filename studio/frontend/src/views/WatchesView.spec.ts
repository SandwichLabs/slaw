// WatchesView (E7 W2): the list with each watch's last outcome, Check all and
// per-watch Check with Stop, the alert panel (reason and sample rows), error rows
// with their reason, the history panel, delete, and the New watch form.
type MockApp = Partial<Record<keyof AppApi, any>>
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const handlers = vi.hoisted(() => ({} as Record<string, (p?: unknown) => void>))
vi.mock('../wailsjs/runtime/runtime', () => ({
  EventsOn: (name: string, cb: (p?: unknown) => void) => { handlers[name] = cb; return () => {} },
}))

import WatchesView from './WatchesView.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { confirmMessage, confirmVisible, resolveConfirm } from '../composables/useConfirm'

const when = (over: Partial<WatchSummary>): WatchSummary => ({
  name: 'w', question: {}, when: {}, ...over,
})
const SUMMARIES: WatchSummary[] = [
  when({
    name: 'revenue_cap', question: { metric: 'revenue' }, when: { above: '1000' }, question_text: 'metric revenue', when_text: 'above 1,000', rev: 'r1',
    last_status: 'alert', last_checked: '2026-11-06T20:00:00Z', last_fired: '2026-11-06T20:00:00Z',
    last: { watch: 'revenue_cap', at: '2026-11-06T20:00:00Z', status: 'alert', value: 1240, reason: 'revenue is 1,240, above 1,000', duration_ms: 12 },
  }),
  when({ name: 'stuck_orders', question: { query: 'stuck' }, when: { rows: 'any' }, question_text: 'query stuck', when_text: 'any rows', rev: 'r2', last_status: 'alert', last: { watch: 'stuck_orders', at: '2026-11-06T20:00:00Z', status: 'alert', rows: 2, reason: 'stuck returned 2 rows', duration_ms: 4 } }),
  when({ name: 'volume', question: { query: 'count' }, when: { below: '3' }, question_text: 'query count', when_text: 'below 3', rev: 'r3' }),
  when({ name: 'zz_broken', error: 'watches/zz_broken.yaml: unknown field "wen"', rev: 'r4' }),
]

const result = (watch: string, status: WatchResult['status'], reason: string, over: Partial<WatchResult> = {}): WatchResult => ({
  watch, at: '2026-11-06T21:00:00Z', status, reason, duration_ms: 5, question: 'query q', condition: 'any rows', ...over,
})

function makeApp(over: MockApp = {}) {
  const app: MockApp = {
    ListWatches: vi.fn(async () => ({ watches: structuredClone(SUMMARIES), alerts: 2 })),
    CheckWatches: vi.fn(async () => ({ results: [
      result('revenue_cap', 'alert', 'revenue is 1,300, above 1,000'),
      result('stuck_orders', 'alert', 'stuck returned 2 rows', { sample: { columns: ['id', 'customer'], rows: [['1', 'acme'], ['2', 'globex']] } }),
      result('volume', 'error', 'could not run: Binder Error: Table "nope" does not exist'),
    ] })),
    WatchHistory: vi.fn(async () => [
      { watch: 'volume', at: '2026-11-06T21:00:00Z', status: 'error', reason: 'could not run: Binder Error', duration_ms: 1500 },
      { watch: 'volume', at: '2026-11-05T21:00:00Z', status: 'ok', value: 7, reason: 'order_count is 7', duration_ms: 3 },
    ]),
    DeleteWatch: vi.fn(async () => {}),
    GetWatch: vi.fn(async (name: string) => ({ ...SUMMARIES.find((x) => x.name === name)!, text: '' })),
    CancelRun: vi.fn(),
    ListQueries: vi.fn(async () => [{ id: 'stuck', title: 'Stuck', engine: 'duckdb' }]),
    ListDefinitions: vi.fn(async () => ({ entities: [], metrics: [{ kind: 'metric', name: 'revenue', title: 'Revenue' }], joins: [], terms: [], open_proposals: 0 })),
    GetQuery: vi.fn(async () => ({ id: 'stuck', parameters: [] })),
    PreviewWatch: vi.fn(async (d: WatchDraft) => `name: ${d.name || 'new_watch'}\n${d.question.metric ? `metric: ${d.question.metric}` : `query: ${d.question.query ?? ''}`}\nwhen: ${JSON.stringify(d.when)}`),
    SaveWatch: vi.fn(async () => 'r9'),
    ...over,
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

const mountView = async () => {
  const w = mount(WatchesView, { attachTo: document.body, global: { stubs: { Icon: true } } })
  await flushPromises()
  return w
}

describe('WatchesView', () => {
  beforeEach(() => { document.body.innerHTML = ''; (window as any).runtime = {} })
  afterEach(() => { vi.useRealTimers(); delete (window as any).runtime })

  it('lists each watch with its question, condition and last outcome, and an unreadable file with its error', async () => {
    makeApp()
    const w = await mountView()
    expect(w.find('[data-testid="watch-revenue_cap"]').text()).toContain('metric revenue')
    expect(w.find('[data-testid="watch-revenue_cap"]').text()).toContain('fires when above 1,000')
    expect(w.find('[data-testid="watch-status-revenue_cap"]').text()).toContain('alert')
    expect(w.find('[data-testid="watch-reason-revenue_cap"]').text()).toBe('revenue is 1,240, above 1,000')
    expect(w.find('[data-testid="watch-status-volume"]').text()).toContain('not checked')
    const broken = w.find('[data-testid="watch-zz_broken"]')
    expect(broken.text()).toContain('This file cannot be read')
    expect(w.find('[data-testid="watch-reason-zz_broken"]').text()).toContain('unknown field')
    expect((w.find('[data-testid="check-zz_broken"]').element as HTMLButtonElement).disabled).toBe(true)
    w.unmount()
  })

  it('shows the fired watches from the history in the alert panel, asking to check again for the rows', async () => {
    makeApp()
    const w = await mountView()
    expect(w.find('[data-testid="alert-revenue_cap"]').exists()).toBe(true)
    expect(w.find('[data-testid="alert-reason-revenue_cap"]').text()).toBe('revenue is 1,240, above 1,000')
    expect(w.find('[data-testid="alert-nosample-stuck_orders"]').text()).toContain('Check again to see the rows')
    expect(w.find('[data-testid="alert-volume"]').exists()).toBe(false)
    w.unmount()
  })

  it('Check all runs every watch; an alerting rows check shows its sample, an error row its reason', async () => {
    const app = makeApp()
    const w = await mountView()
    await w.find('[data-testid="check-all"]').trigger('click')
    await flushPromises()
    expect(app.CheckWatches).toHaveBeenCalledWith([])
    expect(w.find('[data-testid="alert-reason-revenue_cap"]').text()).toBe('revenue is 1,300, above 1,000')
    const sample = w.find('[data-testid="alert-sample-stuck_orders"]')
    expect(sample.findAll('th').map((th) => th.text())).toEqual(['id', 'customer'])
    expect(sample.findAll('tbody tr')).toHaveLength(2)
    expect(sample.text()).toContain('globex')
    expect(w.find('[data-testid="watch-reason-volume"]').text()).toContain('Binder Error')
    expect(w.find('[data-testid="watch-status-volume"]').text()).toContain('error')
    expect(w.find('[data-testid="watch-run-status"]').text()).toBe('Checked 3 watches: 2 fired, 1 error.')
    expect(w.find('[data-testid="watch-status-volume"] [role="status"]').exists()).toBe(false)
    w.unmount()
  })

  it('a per-watch Check checks that watch alone', async () => {
    const app = makeApp()
    const w = await mountView()
    await w.find('[data-testid="check-volume"]').trigger('click')
    await flushPromises()
    expect(app.CheckWatches).toHaveBeenCalledWith(['volume'])
    w.unmount()
  })

  it('Stop cancels the run by key and a partial run says it stopped', async () => {
    let finish: (v: WatchCheckRun) => void = () => {}
    const app = makeApp({ CheckWatches: vi.fn(() => new Promise<WatchCheckRun>((r) => { finish = r })) })
    const w = await mountView()
    await w.find('[data-testid="check-all"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="check-all"]').exists()).toBe(false)
    expect(w.find('[data-testid="watch-status-volume"]').text()).toContain('queued')
    expect((w.find('[data-testid="check-volume"]').element as HTMLButtonElement).disabled).toBe(true)
    // Progress updates rows as each watch finishes; only names and statuses travel.
    handlers['watches:progress']({ name: 'revenue_cap', status: 'running' })
    await flushPromises()
    expect(w.find('[data-testid="watch-status-revenue_cap"]').text()).toContain('checking')
    handlers['watches:progress']({ name: 'revenue_cap', status: 'ok' })
    handlers['watches:progress']({ name: 'volume', status: 'running' })
    await flushPromises()
    expect(w.find('[data-testid="watch-status-revenue_cap"]').text()).toContain('ok')
    expect(w.find('[data-testid="watch-status-revenue_cap"]').text()).not.toContain('revenue is 1,240')
    expect(w.find('[data-testid="watch-status-volume"]').text()).toContain('checking')
    expect(w.find('[data-testid="watch-status-stuck_orders"]').text()).toContain('queued')
    // The seconds counter is not in the live region; the live region only changes at the start and the end.
    expect(w.find('[data-testid="watch-run-status"]').text()).toBe('Checking all watches…')
    expect(w.find('[data-testid="watch-elapsed"]').exists()).toBe(true)
    expect(w.find('[data-testid="watch-run-status"]').element.contains(w.find('[data-testid="watch-elapsed"]').element)).toBe(false)
    await w.find('[data-testid="stop-checks"]').trigger('click')
    expect(app.CancelRun).toHaveBeenCalledWith('watches')
    finish({ results: [result('revenue_cap', 'ok', 'revenue is 5'), result('volume', 'error', 'the check was canceled', { interrupted: true })], cancelled: true })
    await flushPromises()
    expect(w.find('[data-testid="watch-run-status"]').text()).toBe('Stopped. Checked 1 watch: all ok.')
    expect(w.find('[data-testid="check-all"]').exists()).toBe(true)
    expect(w.find('[data-testid="watch-status-revenue_cap"]').text()).toContain('ok')
    const stopped = w.find('[data-testid="watch-status-volume"]')
    expect(stopped.text()).toContain('stopped')
    expect(stopped.text()).toContain("wasn't recorded")
    expect(stopped.find('.badge').classes()).toContain('badge--info')
    expect(w.find('[data-testid="alert-volume"]').exists()).toBe(false)
    w.unmount()
  })

  it('opens and closes the history of a watch', async () => {
    const app = makeApp()
    const w = await mountView()
    await w.find('[data-testid="history-volume"]').trigger('click')
    await flushPromises()
    expect(app.WatchHistory).toHaveBeenCalledWith('volume', 50)
    const rows = w.findAll('[data-testid^="history-row-"]')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('could not run: Binder Error')
    expect(rows[0].text()).toContain('1.5 s')
    expect(rows[1].text()).toContain('7')
    expect(document.activeElement?.id).toBe('wh-h')
    expect(rows[1].text()).toContain('7')
    await w.find('[data-testid="history-close"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="watch-history"]').exists()).toBe(false)
    expect(document.activeElement?.getAttribute('data-testid')).toBe('history-volume')
    w.unmount()
  })

  it('deletes a watch against its revision after confirming', async () => {
    const app = makeApp()
    const w = await mountView()
    await w.find('[data-testid="delete-volume"]').trigger('click')
    await flushPromises()
    expect(app.DeleteWatch).not.toHaveBeenCalled()
    resolveConfirm(true)
    await flushPromises()
    expect(app.DeleteWatch).toHaveBeenCalledWith('volume', 'r3')
    w.unmount()
  })

  it('saving a watch forgets what this window remembered of its old file', async () => {
    const app = makeApp()
    const w = await mountView()
    await w.find('[data-testid="check-all"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="alert-reason-revenue_cap"]').text()).toBe('revenue is 1,300, above 1,000')
    await w.find('[data-testid="edit-revenue_cap"]').trigger('click')
    await flushPromises()
    await w.find('[data-testid="watch-form"]').trigger('submit')
    await flushPromises()
    expect(app.SaveWatch).toHaveBeenCalledWith(expect.objectContaining({ name: 'revenue_cap', rev: 'r1', question: { metric: 'revenue' } }))
    expect(w.find('[data-testid="alert-reason-revenue_cap"]').text()).toBe('revenue is 1,240, above 1,000')
    w.unmount()
  })

  it('a watch whose saved query is gone shows it as missing in the form and will not save until another is picked', async () => {
    const app = makeApp({ GetWatch: vi.fn(async () => ({ ...SUMMARIES[1], question: { query: 'deleted_query' }, text: '' })) })
    const w = await mountView()
    await w.find('[data-testid="edit-stuck_orders"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="wf-target"]').text()).toContain('deleted_query (missing)')
    expect(w.find('[data-testid="wf-target-missing"]').text()).toContain('is not in this workspace')
    expect((w.find('[data-testid="wf-save"]').element as HTMLButtonElement).disabled).toBe(true)
    await w.find('[data-testid="wf-target"]').setValue('stuck')
    await flushPromises()
    expect(w.find('[data-testid="wf-target-missing"]').exists()).toBe(false)
    expect((w.find('[data-testid="wf-save"]').element as HTMLButtonElement).disabled).toBe(false)
    expect(app.SaveWatch).not.toHaveBeenCalled()
    w.unmount()
  })

  it('Escape on the Keep yours confirm leaves the Edit form open', async () => {
    const w = mount({ components: { WatchesView, ConfirmDialog }, template: '<div><WatchesView /><ConfirmDialog :visible="visible" :message="message" @confirm="ok(true)" @cancel="ok(false)" /></div>', setup: () => ({ visible: confirmVisible, message: confirmMessage, ok: resolveConfirm }) }, { attachTo: document.body, global: { stubs: { Icon: true } } })
    makeApp({ SaveWatch: vi.fn().mockRejectedValue(new Error('conflict: watches/revenue_cap.yaml changed on disk')) })
    await flushPromises()
    await w.find('[data-testid="edit-revenue_cap"]').trigger('click')
    await flushPromises()
    await w.find('[data-testid="watch-form"]').trigger('submit')
    await flushPromises()
    await w.find('[data-testid="conflict-overwrite"]').trigger('click')
    await flushPromises()
    expect(confirmVisible.value).toBe(true)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', cancelable: true }))
    await flushPromises()
    expect(confirmVisible.value).toBe(false)
    expect(w.find('[data-testid="watch-form"]').exists()).toBe(true)
    expect(w.find('[data-testid="conflict-banner"]').exists()).toBe(true)
    w.unmount()
  })

  it('writes each parameter hint as one sentence, and a required one must be filled before Save', async () => {
    makeApp({ GetQuery: vi.fn(async (id: string) => ({ id, parameters: [
      { name: 'region', type: 'text', required: true },
      { name: 'days', type: 'integer', required: false, default: '1' },
    ] })) })
    const w = await mountView()
    await w.find('[data-testid="new-watch"]').trigger('click')
    await flushPromises()
    await w.find('[data-testid="wf-name"]').setValue('p')
    await w.find('[data-testid="wf-target"]').setValue('stuck')
    await flushPromises()
    const hint = (n: string) => w.find(`#wf-p-${n}-h`).text()
    expect(hint('region')).toBe('A text with no default: give it a value.')
    expect(hint('days')).toBe('An integer. Leave it empty to use the default, 1.')
    expect(w.find('[data-testid="watch-form"]').text()).not.toContain('..')
    expect((w.find('[data-testid="wf-save"]').element as HTMLButtonElement).disabled).toBe(true)
    await w.find('[data-testid="wf-param-region"]').setValue('east')
    expect((w.find('[data-testid="wf-save"]').element as HTMLButtonElement).disabled).toBe(false)
    w.unmount()
  })

  it('a stopped check\'s alert card reads the history\'s reason, not the cancel text', async () => {
    const app = makeApp()
    const w = await mountView()
    app.CheckWatches = vi.fn(async () => ({ cancelled: true, results: [result('revenue_cap', 'error', 'the check was canceled', { at: '2026-11-07T00:00:00Z', interrupted: true })] }))
    await w.find('[data-testid="check-revenue_cap"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="alert-reason-revenue_cap"]').text()).toBe('revenue is 1,240, above 1,000')
    expect(w.find('[data-testid="alert-revenue_cap"]').text()).toContain('2026-11-06')
    expect(w.find('[data-testid="watch-status-revenue_cap"]').text()).toContain('stopped')
    w.unmount()
  })

  it('a check recorded by another process after this window\'s keeps row, card and badge in agreement and drops the stale rows', async () => {
    const app = makeApp()
    const w = await mountView()
    await w.find('[data-testid="check-stuck_orders"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="alert-sample-stuck_orders"]').exists()).toBe(true) // the window's result is newest
    // The command line then checks it again (a newer entry in the history) and it is fine.
    const newer = { watch: 'stuck_orders', at: '2099-01-01T00:00:00Z', status: 'ok', reason: 'stuck returned 0 rows', rows: 0, duration_ms: 1 }
    app.ListWatches = vi.fn(async () => ({ watches: structuredClone(SUMMARIES).map((x) => (x.name === 'stuck_orders' ? { ...x, last: newer, last_status: 'ok', last_checked: newer.at } : x)), alerts: 1 }))
    handlers['watches:checked']()
    await new Promise((r) => setTimeout(r, 400))
    await flushPromises()
    expect(w.find('[data-testid="watch-status-stuck_orders"]').text()).toContain('ok')
    expect(w.find('[data-testid="watch-reason-stuck_orders"]').text()).toBe('stuck returned 0 rows')
    expect(w.find('[data-testid="alert-stuck_orders"]').exists()).toBe(false)
    expect(w.find('[data-testid="alert-sample-stuck_orders"]').exists()).toBe(false)
    w.unmount()
  })

  it('says so when there are no watches, and offers the New watch form', async () => {
    makeApp({ ListWatches: vi.fn(async () => ({ watches: [], alerts: 0 })) })
    const w = await mountView()
    expect(w.find('[data-testid="watches-empty"]').text()).toContain('No watches yet')
    expect((w.find('[data-testid="check-all"]').element as HTMLButtonElement).disabled).toBe(true)
    w.unmount()
  })

  it('New watch: pick a metric and a condition, preview the file and save', async () => {
    const app = makeApp()
    const w = await mountView()
    await w.find('[data-testid="new-watch"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="watch-form"]').exists()).toBe(true)
    expect((w.find('[data-testid="wf-save"]').element as HTMLButtonElement).disabled).toBe(true)
    await w.find('[data-testid="wf-name"]').setValue('cap')
    await w.find('[data-testid="wf-kind-metric"]').setValue(true)
    await w.find('[data-testid="wf-target"]').setValue('revenue')
    await w.find('[data-testid="wf-cond"]').setValue('above')
    await w.find('[data-testid="wf-a"]').setValue('1000')
    await flushPromises()
    await vi.waitFor(() => expect(w.find('[data-testid="wf-preview"]').text()).toBe('name: cap\nmetric: revenue\nwhen: {"above":"1000"}'))
    await w.find('[data-testid="watch-form"]').trigger('submit')
    await flushPromises()
    expect(app.SaveWatch).toHaveBeenCalledWith({ name: 'cap', description: undefined, question: { metric: 'revenue' }, when: { above: '1000' }, rev: '', overwrite: false })
    expect(w.find('[data-testid="watch-form"]').exists()).toBe(false)
    w.unmount()
  })

  it('the form is keyboard-operable: labelled fields, a labelled radio group, Escape closes', async () => {
    makeApp()
    const w = await mountView()
    await w.find('[data-testid="new-watch"]').trigger('click')
    await flushPromises()
    const form = w.find('[data-testid="watch-form"]')
    expect(form.attributes('role')).toBe('dialog')
    expect(form.attributes('aria-modal')).toBe('true')
    for (const id of ['wf-name', 'wf-desc', 'wf-target', 'wf-cond']) {
      expect(form.find(`label[for="${id}"]`).exists(), id).toBe(true)
    }
    expect(form.find('[role="radiogroup"]').attributes('aria-label')).toBe('What to ask')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(w.find('[data-testid="watch-form"]').exists()).toBe(false)
    w.unmount()
  })
})
