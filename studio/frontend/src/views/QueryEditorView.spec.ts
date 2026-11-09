// Supplementary component tests for the Query Explorer (parity W5).
//
// NOT the acceptance gate — that is Rodney driving :34115 against the real
// backend (docs/archive/parity-remediation-plan.md §1). These cover the logic that's
// awkward to prove via the live DOM: lazy tree expansion with PK/FK badge
// derivation, run-metadata state, export wiring, and the Ask-agent event.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import QueryEditorView from './QueryEditorView.vue'
import { flushAll, _resetForTests as resetSaves } from '../composables/usePendingSaves'
import { _resetConnectionsForTests } from '../composables/useConnections'
import { useEvidenceDrawer, _resetEvidenceForTests } from '../composables/useEvidenceDrawer'
import { _resetRunMemoryForTests, recallRun } from '../composables/useReceipt'

// The SQL editor is CodeMirror, which jsdom cannot drive: mock it the way
// NotebookView.spec.ts does, keep the latest instance, and "type" through its
// onChange. The completion source needs no Go in these specs.
const { createSlawEditor, editors } = vi.hoisted(() => {
  const editors: Array<{ opts: any; doc: string }> = []
  return {
    editors,
    createSlawEditor: vi.fn((_el: HTMLElement, opts: any) => {
      const ed = { opts, doc: opts.value ?? '' }
      editors.push(ed)
      return {
        view: { focus: vi.fn() },
        setValue: (v: string) => { ed.doc = v; opts.onChange?.(v) },
        destroy: vi.fn(),
        getValue: () => ed.doc,
      }
    }),
  }
})
vi.mock('../composables/useSlawEditor', () => ({ createSlawEditor }))
vi.mock('../lib/sqlComplete', () => ({ sqlCompletion: () => [] }))

async function typeSql(sql: string) {
  const ed = editors[editors.length - 1]
  ed.doc = sql
  ed.opts.onChange(sql)
  await flushPromises()
}

// The editor reads ?conn= / ?sql= from the route ("New query here" in the rail).
const routeQuery: Record<string, string> = {}
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: routeQuery, path: '/queries' }),
  useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}))

function makeApp(opts: { execError?: string; execResult?: any; saveId?: string } = {}) {
  const app = {
    ListQueries: vi.fn(async () => [{ id: 'q1', title: 'Top customers' }]),
    GetQuery: vi.fn(async () => ({ id: 'q1', title: 'Top customers', sql: 'SELECT 1' })),
    SaveQueryOn: vi.fn(async (_req: any) => ({ id: opts.saveId ?? 'q9', rev: 'r1' })),
    ListConnections: vi.fn(async () => ({
      connections: [
        { name: 'local', type: 'duckdb', scope: 'shared', readOnly: false, default: true, status: 'ok', remote: false },
        { name: 'prod_pg', type: 'postgres', scope: 'local', readOnly: true, default: false, status: 'not-connected', remote: true },
      ],
      relaunchRequired: false,
    })),
    ConnectConnection: vi.fn(async () => null),
    DeleteQuery: vi.fn(async () => {}),
    RunQuery: vi.fn(async (_req: any) => {
      if (opts.execError) throw new Error(opts.execError)
      return opts.execResult ?? {
        columns: [{ name: 'x' }],
        rows: [[1]],
        duration: '4.2ms',
        row_count: 1,
        total_rows: 1,
      }
    }),
    ExportQueryResults: vi.fn(async (_sql: string, format: string) => `/tmp/out.${format}`),
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

async function mountView() {
  const wrapper = mount(QueryEditorView, { global: { stubs: { Icon: true } } })
  await flushPromises()
  return wrapper
}

describe('QueryEditorView (W5 Query Explorer)', () => {
  beforeEach(() => { vi.clearAllMocks(); editors.length = 0; _resetConnectionsForTests() })

  it('has no schema tree of its own: the global Connections rail replaces it', async () => {
    makeApp()
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="schema-tree"]').exists()).toBe(false)
  })

  it('runs on the chosen connection, per editor (RunQuery carries it)', async () => {
    const app = makeApp()
    const wrapper = await mountView()
    await typeSql('SELECT 1')
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    expect(app.RunQuery).toHaveBeenLastCalledWith(expect.objectContaining({ sql: 'SELECT 1', connection: '' }))

    ;(wrapper.vm as any).conn = 'prod_pg'
    await flushPromises()
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    expect(app.RunQuery).toHaveBeenLastCalledWith(expect.objectContaining({ connection: 'prod_pg' }))
    expect(wrapper.find('[data-testid="run-engine"]').text()).toContain('ran on prod_pg')
  })

  it('Stop cancels the run of this tab and shows a neutral cancelled line, not an error', async () => {
    const app = makeApp() as any
    let reject!: (e: Error) => void
    app.RunQuery.mockImplementationOnce(() => new Promise((_, rej) => { reject = rej }))
    app.CancelRun = vi.fn(async () => { reject(new Error('cancelled')) })
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="stop-query"]').exists()).toBe(false)
    await typeSql('SELECT count(*) FROM range(1e12::BIGINT)')
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    const stop = wrapper.find('[data-testid="stop-query"]')
    expect(stop.exists()).toBe(true)
    expect(stop.attributes('aria-label')).toBeTruthy()
    const tabId = app.RunQuery.mock.calls[0][0].tabId
    await stop.trigger('click')
    await flushPromises()
    expect(app.CancelRun).toHaveBeenCalledWith(`query:${tabId}`)
    expect(wrapper.find('[data-testid="query-cancelled"]').exists()).toBe(true)
    expect(wrapper.find('.sql-error').exists()).toBe(false)
    expect(wrapper.find('[data-testid="run-badge"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="stop-query"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="run-query"]').attributes('disabled')).toBeUndefined()
  })

  it('cancels an in-flight run when the view is left', async () => {
    const app = makeApp() as any
    app.RunQuery.mockImplementationOnce(() => new Promise(() => {}))
    app.CancelRun = vi.fn(async () => {})
    const wrapper = await mountView()
    await typeSql('SELECT 1')
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    const tabId = app.RunQuery.mock.calls[0][0].tabId
    wrapper.unmount()
    expect(app.CancelRun).toHaveBeenCalledWith(`query:${tabId}`)
  })

  it('says a statement ran across the connections its qualified names pulled in', async () => {
    makeApp()
    const wrapper = await mountView()
    await typeSql('SELECT * FROM local.main.a JOIN prod_pg.public.b USING (id)')
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="run-engine"]').text()).toContain('ran across local + prod_pg')
  })

  it('saves the Run on choice with the query and restores it on open', async () => {
    const app = makeApp({ saveId: 'q9' })
    app.GetQuery.mockResolvedValue({ id: 'q1', title: 'Top customers', sql: 'SELECT 1', connection: 'prod_pg' } as any)
    const wrapper = await mountView()
    await wrapper.find('[data-testid="saved-query"]').trigger('click')
    await flushPromises()
    expect((wrapper.vm as any).conn).toBe('prod_pg')
    ;(wrapper.vm as any).saveTitle = 'Top customers'
    await (wrapper.vm as any).saveQuery()
    await flushPromises()
    expect(app.SaveQueryOn).toHaveBeenCalledWith(expect.objectContaining({ id: 'q1', connection: 'prod_pg' }))
  })

  it('a successful run surfaces the metadata bar: Complete badge, DuckDB label, last-run time', async () => {
    makeApp()
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="run-meta"]').exists()).toBe(false)

    await typeSql('SELECT 1 AS x')
    await wrapper.find('.btn-primary').trigger('click')
    await flushPromises()

    const meta = wrapper.find('[data-testid="run-meta"]')
    expect(meta.exists()).toBe(true)
    expect(wrapper.find('[data-testid="run-badge"]').text()).toContain('Complete')
    expect(wrapper.find('[data-testid="run-engine"]').text()).toContain('DuckDB')
    expect(wrapper.find('[data-testid="run-last"]').text()).toContain('Last run ·')
    expect(meta.text()).toContain('1')
    expect(meta.text()).toContain('4.2ms')
  })

  // --- Faithful value rendering in the query grid ---

  it('NULL renders as NULL in a muted cell, distinct from an empty string, and a DECIMAL cell is right-aligned and grouped', async () => {
    makeApp({
      execResult: {
        columns: [
          { name: 'note', type: 'VARCHAR' },
          { name: 'empty', type: 'VARCHAR' },
          { name: 'amt', type: 'DECIMAL(12,2)' },
          { name: 'amt_null', type: 'DECIMAL(12,2)' },
        ],
        rows: [[null, '', 15675.3, null]],
        duration: '1 ms',
        row_count: 1,
        total_rows: 1,
      },
    })
    const wrapper = await mountView()
    await typeSql('SELECT note, empty, amt, amt_null FROM probe')
    await wrapper.find('.btn-primary').trigger('click')
    await flushPromises()

    const cells = wrapper.findAll('tbody td')
    expect(cells[0].text()).toBe('NULL')
    expect(cells[0].classes()).toContain('cell-null')
    expect(cells[0].classes()).not.toContain('cell-num')
    expect(cells[1].text()).toBe('')
    expect(cells[1].classes()).not.toContain('cell-null')
    expect(cells[2].text()).toBe('15,675.30')
    expect(cells[2].classes()).toContain('cell-num')
    // A NULL in a numeric column is right-aligned like its non-null
    // neighbours in the same column.
    expect(cells[3].text()).toBe('NULL')
    expect(cells[3].classes()).toContain('cell-null')
    expect(cells[3].classes()).toContain('cell-num')

    // The numeric columns' <th> also gets .cell-num (theme/bridge.css's
    // `.table th.cell-num` rule right-aligns
    // it, beating kit.css's plain `.table th`; that CSS cascade isn't
    // exercised by jsdom, so this just proves the class is applied).
    const headers = wrapper.findAll('thead th')
    expect(headers[2].classes()).toContain('cell-num')
    expect(headers[0].classes()).not.toContain('cell-num')
  })

  it('a failed run shows the Error badge with the message', async () => {
    makeApp({ execError: 'Binder Error: nope' })
    const wrapper = await mountView()
    await typeSql('SELECT nope')
    await wrapper.find('.btn-primary').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="run-badge"]').text()).toContain('Error')
    expect(wrapper.text()).toContain('Binder Error: nope')
  })

  it('CSV / Parquet buttons export the current SQL via ExportQueryResults', async () => {
    const app = makeApp()
    const wrapper = await mountView()

    // Disabled with no SQL.
    expect((wrapper.find('[data-testid="export-csv"]').element as HTMLButtonElement).disabled).toBe(true)

    await typeSql('SELECT * FROM customers')
    await wrapper.find('[data-testid="export-csv"]').trigger('click')
    await flushPromises()
    expect(app.ExportQueryResults).toHaveBeenCalledWith('SELECT * FROM customers', 'csv', '')

    await wrapper.find('[data-testid="export-parquet"]').trigger('click')
    await flushPromises()
    expect(app.ExportQueryResults).toHaveBeenCalledWith('SELECT * FROM customers', 'parquet', '')

    // The written path is surfaced.
    expect(wrapper.find('[data-testid="query-status"]').text()).toContain('/tmp/out.parquet')
  })

  it('Ask assistant dispatches the slaw:open-agent window event', async () => {
    makeApp()
    const wrapper = await mountView()
    const seen = vi.fn()
    window.addEventListener('slaw:open-agent', seen)
    await wrapper.find('[data-testid="ask-agent"]').trigger('click')
    expect(seen).toHaveBeenCalledTimes(1)
    window.removeEventListener('slaw:open-agent', seen)
  })

  it('the Unsaved badge tracks divergence from the loaded saved query', async () => {
    makeApp()
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="query-dirty"]').exists()).toBe(false)

    await wrapper.find('[data-testid="saved-query"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="query-dirty"]').exists()).toBe(false)

    await typeSql('SELECT 2')
    expect(wrapper.find('[data-testid="query-dirty"]').exists()).toBe(true)
  })

  // --- #91 quick wins ---

  it('SaveQuery returns an id; saving twice reuses it and the header/rail follow', async () => {
    const app = makeApp({ saveId: 'q9' })
    const wrapper = await mountView()
    const saveBtn = () => wrapper.findAll('button').find((b) => b.text().includes('Save query'))!
    const modalSaveBtn = () => wrapper.findAll('.modal-overlay button').find((b) => b.text() === 'Save')!

    await saveBtn().trigger('click') // opens the modal
    await wrapper.find('input[aria-label="Query name"]').setValue('My query')
    await modalSaveBtn().trigger('click')
    await flushPromises()

    expect(app.SaveQueryOn).toHaveBeenNthCalledWith(1, expect.objectContaining({ id: '', title: 'My query', sql: '', rev: '' }))

    await typeSql('SELECT 3')
    await saveBtn().trigger('click')
    await modalSaveBtn().trigger('click')
    await flushPromises()

    expect(app.SaveQueryOn).toHaveBeenNthCalledWith(2, expect.objectContaining({ id: 'q9', title: 'My query', sql: 'SELECT 3', rev: 'r1' }))
    expect(wrapper.find('[data-testid="query-status"]').text()).toContain('My query')
  })

  it('shows the row-cap bar when a query is truncated server-side', async () => {
    makeApp({
      execResult: {
        columns: [{ name: 'i' }],
        rows: Array.from({ length: 5000 }, (_, i) => [i]),
        duration: '10ms',
        row_count: 5000,
        total_rows: 100000,
      },
    })
    const wrapper = await mountView()
    await typeSql('SELECT * FROM range(100000)')
    await wrapper.find('.btn-primary').trigger('click')
    await flushPromises()

    const cap = wrapper.find('[data-testid="query-row-cap"]')
    expect(cap.exists()).toBe(true)
    expect(cap.text()).toContain('5,000')
    expect(cap.text()).toContain('100,000')
  })

  describe('pending saves (switch / close / quit flush an edited query)', () => {
    beforeEach(() => resetSaves())

    it('flushAll saves an edited saved query with the revision it loaded', async () => {
      const app = makeApp()
      app.GetQuery.mockResolvedValue({ id: 'q1', title: 'Top customers', sql: 'SELECT 1', rev: 'r0' } as any)
      const wrapper = await mountView()
      const vm = wrapper.vm as any
      await vm.selectQuery?.('q1')
      vm.sqlText = 'SELECT 2'
      await flushPromises()
      const res = await flushAll()
      expect(res.failed).toEqual([])
      expect(app.SaveQueryOn).toHaveBeenCalledWith(expect.objectContaining({ id: 'q1', title: 'Top customers', sql: 'SELECT 2', rev: 'r0' }))
    })

    it('a never-saved scratch buffer cannot be flushed, so the caller asks', async () => {
      makeApp()
      const wrapper = await mountView()
      ;(wrapper.vm as any).sqlText = 'SELECT 42'
      await flushPromises()
      const res = await flushAll()
      expect(res.failed).toHaveLength(1)
      expect(res.failed[0].label).toBe('The query editor')
    })

    it('an untouched editor flushes cleanly', async () => {
      const app = makeApp()
      await mountView()
      expect((await flushAll()).failed).toEqual([])
      expect(app.SaveQueryOn).not.toHaveBeenCalled()
    })
  })

  it('"Keep mine" sends the revision the banner was raised against', async () => {
    const app = makeApp() as any
    let n = 0
    app.GetQuery.mockImplementation(async () => ({ id: 'q1', title: 'Top customers', sql: 'SELECT 1', rev: n++ === 0 ? 'r0' : 'r5' }))
    app.ResolveConflict = vi.fn(async () => ({ id: 'q1', rev: 'r6', title: 'Top customers' }))
    const wrapper = await mountView()
    const vm = wrapper.vm as any
    await vm.selectQuery('q1')
    vm.sqlText = 'SELECT 2'
    app.SaveQueryOn.mockRejectedValueOnce(new Error('conflict: queries/q1.sql changed on disk since it was loaded'))
    vm.saveTitle = 'Top customers'
    await vm.saveQuery()
    await flushPromises()
    expect(vm.conflict).not.toBeNull()
    await vm.resolveConflict('overwrite')
    await flushPromises()
    expect(app.ResolveConflict).toHaveBeenCalledWith(expect.objectContaining({ kind: 'query', action: 'overwrite', rev: 'r5' }))
  })

  it('Ctrl/Cmd+Enter in the editor runs the query (the Mod-Enter binding)', async () => {
    const app = makeApp()
    const wrapper = await mountView()
    await typeSql('SELECT 7')
    const ed = editors[editors.length - 1]
    const binding = ed.opts.extraKeymap.find((k: any) => k.key === 'Mod-Enter')
    expect(binding.run({ state: { field: () => null } })).toBe(true)
    await flushPromises()
    expect(app.RunQuery).toHaveBeenCalledWith(expect.objectContaining({ sql: 'SELECT 7' }))
    expect(wrapper.find('[data-testid="sql-input"]').exists()).toBe(true)
  })

  it('loading a saved query puts its SQL in the editor', async () => {
    makeApp()
    const wrapper = await mountView()
    await wrapper.find('[data-testid="saved-query"]').trigger('click')
    await flushPromises()
    expect(editors[editors.length - 1].doc).toBe('SELECT 1')
  })

  it('a DDL result with rows: null renders as Complete without throwing', async () => {
    makeApp({ execResult: { columns: [], rows: null, duration: '1ms', row_count: 0, total_rows: 0 } })
    const wrapper = await mountView()
    await typeSql('CREATE TABLE t(a INTEGER)')
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="run-badge"]').text()).toContain('Complete')
  })

  it('shows an input per declared parameter and runs the saved query with the typed values (#235)', async () => {
    const app = makeApp()
    app.GetQuery.mockResolvedValue({
      id: 'q1', title: 'Over', sql: 'SELECT $min_n',
      parameters: [{ name: 'min_n', type: 'integer', required: false, default: '5' }],
    } as any)
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="query-params"]').exists()).toBe(false)
    await wrapper.find('[data-testid="saved-query"]').trigger('click')
    await flushPromises()
    const input = wrapper.find('[data-testid="query-param-min_n"] input')
    expect((input.element as HTMLInputElement).value).toBe('5')

    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    expect(app.RunQuery).toHaveBeenLastCalledWith(expect.objectContaining({ queryId: 'q1', params: { min_n: 5 } }))

    await input.setValue('12')
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    expect(app.RunQuery).toHaveBeenLastCalledWith(expect.objectContaining({ params: { min_n: 12 } }))

    await input.setValue('lots')
    app.RunQuery.mockClear()
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    expect(app.RunQuery).not.toHaveBeenCalled()
    expect(wrapper.find('.sql-error').text()).toContain('min_n')
  })

  // Evidence (docs/plans/run-receipts.md R4a)
  it('the result bar has an Evidence button when the run has a receipt; it opens the drawer with the run’s parameter values in memory', async () => {
    _resetEvidenceForTests(); _resetRunMemoryForTests()
    const app = makeApp({ execResult: { columns: [{ name: 'x' }], rows: [[1]], duration: '4.2ms', row_count: 1, total_rows: 1, receipt_id: 'r_q1' } })
    app.GetQuery.mockResolvedValue({
      id: 'q1', title: 'Over', sql: 'SELECT $min_n',
      parameters: [{ name: 'min_n', type: 'integer', required: false, default: '5' }],
    } as any)
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="query-evidence"]').exists()).toBe(false)
    await wrapper.find('[data-testid="saved-query"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()

    const btn = wrapper.find('[data-testid="query-evidence"]')
    expect(btn.exists()).toBe(true)
    expect(recallRun('r_q1')?.params).toEqual({ min_n: '5' })
    await btn.trigger('click')
    expect(useEvidenceDrawer().target.value).toMatchObject({ title: 'Top customers', receiptId: 'r_q1' })
    expect(useEvidenceDrawer().target.value?.memory).toMatchObject({ sql: 'SELECT $min_n', params: { min_n: '5' } })
    _resetEvidenceForTests()
  })

  it('a run with no receipt and no note has no Evidence button; a note alone brings it back', async () => {
    _resetEvidenceForTests()
    makeApp()
    let wrapper = await mountView()
    await typeSql('SELECT 1')
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="query-evidence"]').exists()).toBe(false)

    makeApp({ execResult: { columns: [{ name: 'x' }], rows: [[1]], duration: '4.2ms', row_count: 1, total_rows: 1, receipt_note: 'not_kept' } })
    editors.length = 0
    wrapper = await mountView()
    await typeSql('SELECT 1')
    await wrapper.find('[data-testid="run-query"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="query-evidence"]').trigger('click')
    expect(useEvidenceDrawer().target.value).toMatchObject({ note: 'not_kept', memory: { sql: 'SELECT 1' } })
    _resetEvidenceForTests()
  })
})
