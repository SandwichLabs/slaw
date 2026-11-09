// NotebookView interaction tests (#66 part A): run a sql cell and see it
// auto-fit a view, edit-without-running shows the stale badge, and a write
// attempt surfaces the read-only message RunA2UIQueryIn already returns.
type MockApp = Partial<Record<keyof AppApi, any>>
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// createSlawEditor is hoisted so tests can inspect/reset its own mock.fn call
// count for lazy-mount assertions (#95: the SQL editor only mounts once its
// drawer is opened).
const { createSlawEditor } = vi.hoisted(() => ({
  createSlawEditor: vi.fn((el: HTMLElement, opts: any) => ({
    view: { focus: vi.fn() },
    setValue: (v: string) => { opts.onChange?.(v) },
    destroy: vi.fn(),
    getValue: () => opts.value ?? '',
  })),
}))
vi.mock('../composables/useSlawEditor', () => ({ createSlawEditor }))

// The mocked route must be genuinely Vue-reactive — NotebookView's
// `watch(() => route.params.id, ...)` (the fix for #66 part A review item
// 3, the notebook-to-notebook overwrite bug) only fires when Vue can track
// a dependency on route.params.id, which a plain mutated object can't
// provide. Built inside the (async-capable) mock factory so it can import
// 'vue' itself without fighting vi.mock's hoisting-above-imports rule.
vi.mock('vue-router', async () => {
  const { reactive } = await import('vue')
  const mockRoute = reactive({ params: { id: 'nb1' } })
  ;(globalThis as any).__mockRoute = mockRoute
  return {
    useRoute: () => mockRoute,
    useRouter: () => ({ push: vi.fn() }),
    onBeforeRouteLeave: () => {},
  }
})

import NotebookView from './NotebookView.vue'
import { setPendingAsk } from '../composables/usePendingAsk'
import { useEvidenceDrawer, _resetEvidenceForTests } from '../composables/useEvidenceDrawer'

const BAR_NOTEBOOK = {
  schemaVersion: 1,
  id: 'nb1',
  title: 'My Notebook',
  updatedAt: '2026-01-01T00:00:00Z',
  cells: [
    { id: 'c1', type: 'sql', collapsed: false, source: 'SELECT region, sum(revenue) FROM sales GROUP BY ALL', view: 'table', viewPinned: false, label: 'by region' },
  ],
}

// MIXED_NOTEBOOK (#95): one of each interesting shape — a labeled sql cell,
// an empty sql cell (should load with its drawer open), a markdown cell with
// an ATX heading, and an a2ui cell with two components.
const MIXED_NOTEBOOK = {
  schemaVersion: 1,
  id: 'nb1',
  title: 'Mixed',
  updatedAt: '2026-01-01T00:00:00Z',
  cells: [
    { id: 'c1', type: 'sql', collapsed: false, source: 'SELECT region, sum(revenue) FROM sales GROUP BY ALL', view: 'table', viewPinned: false, label: 'by region' },
    { id: 'c2', type: 'sql', collapsed: false, source: '', view: 'table', viewPinned: false, label: '' },
    { id: 'c3', type: 'markdown', collapsed: false, source: 'intro line\n## Intro' },
    { id: 'c4', type: 'a2ui', collapsed: false, page: { title: 'KPIs', components: [{ id: 'a', type: 'text', text: 'x' }, { id: 'b', type: 'text', text: 'y' }] } },
  ],
}

function makeApp(overrides: MockApp = {}) {
  const app: MockApp = {
    GetNotebook: vi.fn(async () => JSON.parse(JSON.stringify(BAR_NOTEBOOK))),
    SaveNotebook: vi.fn(async (nb: any) => ({ notebook: nb, warnings: [] })),
    RunA2UIQueryIn: vi.fn(async () => ({
      columns: ['region', 'sum(revenue)'],
      rows: [['North', 100], ['South', 200], ['East', 50]],
      truncated: false,
    })),
    ...overrides,
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

let mountedWrappers: ReturnType<typeof mount>[] = []
function mountView() {
  const wrapper = mount(NotebookView, { global: { stubs: { Icon: true } }, attachTo: document.body })
  mountedWrappers.push(wrapper)
  return wrapper
}

describe('NotebookView', () => {
  beforeEach(() => {
    vi.useRealTimers()
    ;(globalThis as any).__mockRoute.params.id = 'nb1'
    createSlawEditor.mockClear()
  })

  // Every mounted NotebookView keeps a live watch() on the shared mocked
  // route object — without unmounting between tests, a PREVIOUS test's
  // component instance is still around and reacts to a LATER test's route
  // mutations too, causing spurious/duplicate GetNotebook and SaveNotebook
  // calls that made the route-switch test flaky when run alongside others.
  afterEach(() => {
    for (const w of mountedWrappers) w.unmount()
    mountedWrappers = []
  })

  it('a pending ask for this notebook opens the AI cell dialog filled in, once, and runs nothing', async () => {
    const app = makeApp({ AgentTask: vi.fn() })
    setPendingAsk('nb1', 'revenue by region')
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    expect((wrapper.find('[data-testid="ai-cell-prompt"]').element as HTMLTextAreaElement).value).toBe('revenue by region')
    expect(app.AgentTask).not.toHaveBeenCalled()
    wrapper.unmount()
    mountedWrappers = []
    const again = mountView()
    await flushPromises()
    await flushPromises()
    expect(again.find('[data-testid="ai-cell-dialog"]').exists()).toBe(false)
  })

  it('a failed load takes the pending ask and opens no dialog', async () => {
    makeApp({ GetNotebook: vi.fn(async () => { throw new Error('gone') }) })
    setPendingAsk('nb1', 'x')
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="ai-cell-dialog"]').exists()).toBe(false)
    const { takePendingAsk } = await import('../composables/usePendingAsk')
    expect(takePendingAsk('nb1')).toBeNull()
  })

  it('a pending ask for another notebook is left alone', async () => {
    makeApp()
    setPendingAsk('other', 'x')
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    expect(wrapper.find('[data-testid="ai-cell-dialog"]').exists()).toBe(false)
  })

  it('loads and runs sql cells on open, auto-fitting a chart view', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[data-testid="notebook-title"]').element).toBeTruthy()
    // Auto-fit: 3 distinct regions with a numeric second column -> bar.
    expect(wrapper.find('[data-testid="view-bar"]').classes()).toContain('row-selected')
  })

  // synthesizedPage stamps the client-only `types` field onto the
  // table component it builds from a sql cell's result, so a DECIMAL column
  // renders with its real scale/grouping instead of JS's trimmed number
  // text — proving the types plumbing end to end (runLiveQuery -> cell.result
  // -> synthesizedPage -> A2UITable), not just formatCell in isolation.
  it('a table-view sql cell renders DECIMAL results with their real scale via the synthesized types', async () => {
    const app = makeApp({
      RunA2UIQueryIn: vi.fn(async () => ({
        columns: ['id', 'amt'],
        types: ['BIGINT', 'DECIMAL(12,2)'],
        rows: [['9007199254740993', 15675.3]],
        truncated: false,
      })),
    })
    const nb = JSON.parse(JSON.stringify(BAR_NOTEBOOK))
    nb.cells[0].view = 'table'
    app.GetNotebook = vi.fn(async () => nb)
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    const cells = wrapper.findAll('tbody td')
    expect(cells[0].text()).toBe('9,007,199,254,740,993')
    expect(cells[1].text()).toBe('15,675.30')
  })

  // #68 follow-up (reviewer-caught data corruption): NotebookView must
  // stamp itself with the workspace session id in effect at LOAD time and
  // send it back on every save, so the backend can reject a save whose
  // token no longer matches the currently open workspace (e.g. the workspace
  // was switched while this view was still mounted for the previous one).
  it('captures the session id from GetSessionID at load and sends it with every SaveNotebook call', async () => {
    const app = makeApp({ GetSessionID: vi.fn(async () => 'session-abc') })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    const vm = wrapper.vm as any
    expect(vm.getLoadedSessionId()).toBe('session-abc')

    await vm.flushSave()
    expect(app.SaveNotebook).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'nb1' }),
      'session-abc',
    )
  })

  it('falls back to an empty session id when GetSessionID is unavailable, without crashing the load', async () => {
    const app = makeApp() // no GetSessionID override — makeApp's base app doesn't define one
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    const vm = wrapper.vm as any
    expect(vm.getLoadedNotebookId()).toBe('nb1') // the notebook still loaded fine
    expect(vm.getLoadedSessionId()).toBe('')

    await vm.flushSave()
    expect(app.SaveNotebook).toHaveBeenCalledWith(expect.objectContaining({ id: 'nb1' }), '')
  })

  // Notebook format v2 (#108): a sql cell's `connection` runs its query with
  // that connection as the default catalog and round-trips through save.
  it('runs a sql cell with its connection and saves the connection back', async () => {
    const nb = JSON.parse(JSON.stringify(BAR_NOTEBOOK))
    nb.cells[0].connection = 'prod_pg'
    const app = makeApp({
      GetNotebook: vi.fn(async () => nb),
      RunA2UIQueryIn: vi.fn(async () => ({
        columns: ['region', 'sum(revenue)'], types: ['VARCHAR', 'BIGINT'], rows: [['North', 100]], truncated: false,
      })),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    // The cell names itself (notebook and cell id), so its receipt says where it ran.
    expect(app.RunA2UIQueryIn).toHaveBeenCalledWith(nb.cells[0].source, 'prod_pg', { kind: 'cell', notebook: 'nb1', cell: 'c1' })

    // An edit triggers a save; the connection must ride along in the payload.
    const vm = wrapper.vm as any
    vm.cells[0].label = 'renamed'
    await vm.flushSave()
    const saved = (app.SaveNotebook as any).mock.calls.at(-1)[0]
    expect(saved.cells[0].connection).toBe('prod_pg')
    expect(saved.cells[0].label).toBe('renamed')
  })

  it('a sql cell without a connection saves no connection key', async () => {
    const app = makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.cells[0].label = 'renamed'
    await vm.flushSave()
    const saved = (app.SaveNotebook as any).mock.calls.at(-1)[0]
    expect(saved.cells[0].label).toBe('renamed')
    expect('connection' in saved.cells[0]).toBe(false)
  })

  it('marks a cell stale when the source is edited without re-running', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[data-testid="stale-badge"]').exists()).toBe(false)

    // Simulate an edit through the (mocked) editor's onChange.
    const vm = wrapper.vm as any
    vm.cells[0].source = vm.cells[0].source + ' -- edited'
    await wrapper.vm.$nextTick()

    expect(wrapper.find('[data-testid="stale-badge"]').exists()).toBe(true)
  })

  it('a write query surfaces the read-only message from RunA2UIQueryIn', async () => {
    const app = makeApp({
      RunA2UIQueryIn: vi.fn(async () => {
        throw new Error('live bindings are read-only queries over this file\'s own tables (no writes, no external files)')
      }),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    const vm = wrapper.vm as any
    vm.cells[0].source = 'DELETE FROM sales'
    await vm.runCell(vm.cells[0])
    await flushPromises()

    expect(wrapper.find('[data-testid="cell-error"]').text()).toContain('read-only')
    expect(app.RunA2UIQueryIn).toHaveBeenCalled()
  })

  it('an empty cell disables Run and says to write a query first, without calling the binding (#3)', async () => {
    const app = makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    const vm = wrapper.vm as any
    vm.cells[0].source = '   '
    await wrapper.vm.$nextTick()
    const calls = (app.RunA2UIQueryIn as any).mock.calls.length
    expect(wrapper.find('[data-testid="run-cell"]').attributes('disabled')).toBeDefined()

    await vm.runCell(vm.cells[0])
    await flushPromises()

    expect(wrapper.find('[data-testid="cell-error"]').text()).toBe('Write a query first.')
    expect((app.RunA2UIQueryIn as any).mock.calls.length).toBe(calls)
  })

  it('picking an unfit view shows a mismatch note instead of the chart', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    await wrapper.find('[data-testid="view-stat"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="mismatch-note"]').exists()).toBe(true)
  })

  it('adds, moves, and deletes cells', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    await wrapper.find('[data-testid="add-markdown-cell"]').trigger('click')
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.cells).toHaveLength(2)
    expect(vm.cells[1].type).toBe('markdown')

    vm.moveCell(vm.cells[1], -1)
    await flushPromises()
    expect(vm.cells[0].type).toBe('markdown')

    vm.deleteCell(vm.cells[0])
    await flushPromises()
    expect(vm.cells).toHaveLength(1)
  })

  // Review fix, step 4a #3: vue-router reuses this component instance
  // across a /library/notebook/A -> /library/notebook/B navigation — only route.params.id
  // changes. Before the fix, the debounced save still in flight (or one
  // that fired after cells.value had already been replaced with B's cells)
  // built its payload from the LIVE route id, so A's edit could be written
  // under B's id, silently overwriting B's content with A's.
  it('switching notebooks flushes the old one under its own id and never overwrites the new one', async () => {
    const NB_A = { schemaVersion: 1, id: 'nbA', title: 'Notebook A', updatedAt: 't', cells: [{ id: 'a1', type: 'markdown', collapsed: false, source: 'original A' }] }
    const NB_B = { schemaVersion: 1, id: 'nbB', title: 'Notebook B', updatedAt: 't', cells: [{ id: 'b1', type: 'markdown', collapsed: false, source: 'original B' }] }
    ;(globalThis as any).__mockRoute.params.id = 'nbA'
    const saveCalls: any[] = []
    makeApp({
      GetNotebook: vi.fn(async (id: string) => JSON.parse(JSON.stringify(id === 'nbA' ? NB_A : NB_B))),
      SaveNotebook: vi.fn(async (nb: any) => {
        saveCalls.push(JSON.parse(JSON.stringify(nb)))
        return { notebook: nb, warnings: [] }
      }),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    const vm = wrapper.vm as any
    expect(vm.getLoadedNotebookId()).toBe('nbA')
    // Simulate an in-progress edit to A that has not yet autosaved.
    vm.cells[0].source = 'EDITED A'

    // Navigate to B — the component instance is reused (Vitest mounts fresh,
    // but this exercises the same `watch(() => route.params.id, ...)` path
    // vue-router's real reuse would trigger).
    ;(globalThis as any).__mockRoute.params.id = 'nbB'
    await flushPromises()
    await flushPromises()
    await flushPromises()

    // A's edit must have been flushed under A's OWN id before B loaded.
    const aSave = saveCalls.find((c) => c.id === 'nbA')
    expect(aSave).toBeTruthy()
    expect(aSave.cells[0].source).toBe('EDITED A')

    // B must be loaded fresh, completely unaffected by A's content.
    expect(vm.getLoadedNotebookId()).toBe('nbB')
    expect(vm.title).toBe('Notebook B')
    expect(vm.cells[0].source).toBe('original B')

    // The bug this guards against: a save landing under B's id but carrying
    // A's edited content.
    const corrupted = saveCalls.find((c) => c.id === 'nbB' && c.cells[0]?.source === 'EDITED A')
    expect(corrupted).toBeFalsy()
  })

  // --- coordinator follow-up #3: a "notebooks:changed" append event that
  // arrives WHILE a notebook is loading must be queued and replayed once
  // loading finishes, not silently dropped. ---
  it('queues an append event received while loading and replays it once the load settles', async () => {
    const NB2_BASE = { schemaVersion: 1, id: 'nb2', title: 'NB2', updatedAt: 't', cells: [] as any[] }
    const NB2_WITH_APPEND = { ...NB2_BASE, cells: [{ id: 'new1', type: 'markdown', collapsed: false, source: 'appended while loading' }] }

    let resolveNb2Load: ((v: any) => void) | null = null
    let getNotebookCalls = 0
    makeApp({
      GetNotebook: vi.fn((id: string) => {
        getNotebookCalls++
        if (id === 'nb1') return Promise.resolve(JSON.parse(JSON.stringify(BAR_NOTEBOOK)))
        // id === 'nb2': the FIRST call is loadNotebook's own fetch — held
        // pending so the test can fire the event while loading.value is
        // still true. Every subsequent call (the merge fetch inside
        // applyNotebooksChanged's replay) returns the version WITH the
        // appended cell.
        if (getNotebookCalls === 2) {
          return new Promise((resolve) => { resolveNb2Load = resolve })
        }
        return Promise.resolve(JSON.parse(JSON.stringify(NB2_WITH_APPEND)))
      }),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    const vm = wrapper.vm as any
    expect(vm.getLoadedNotebookId()).toBe('nb1')

    // Switch to nb2 — loadNotebook('nb2') starts and blocks on GetNotebook.
    ;(globalThis as any).__mockRoute.params.id = 'nb2'
    await flushPromises()
    await flushPromises()

    expect(vm.getLoadedNotebookId()).toBe('nb2')
    expect(vm.isLoading()).toBe(true)

    // The event arrives mid-load — must be queued, not dropped.
    await vm.handleNotebooksChanged({ notebookId: 'nb2', appendedCellIds: ['new1'] })
    expect(vm.cells.map((c: any) => c.id)).not.toContain('new1') // not applied yet — still loading

    // The load itself finishes now.
    resolveNb2Load!(JSON.parse(JSON.stringify(NB2_BASE)))
    await flushPromises()
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(vm.isLoading()).toBe(false)
    expect(vm.cells.map((c: any) => c.id)).toContain('new1')
  })

  // Re-review #109 item 6: the remote revision is adopted after an agent append
  // only when nothing else changed on disk.
  it('adopts the remote rev after an append that is the only change, and sends it on the next save', async () => {
    const base = { ...JSON.parse(JSON.stringify(BAR_NOTEBOOK)), rev: 'r1' }
    const appended = { id: 'new1', type: 'markdown', collapsed: false, source: 'agent cell' }
    let calls = 0
    const app = makeApp({
      GetNotebook: vi.fn(async () => {
        calls++
        return calls === 1 ? JSON.parse(JSON.stringify(base)) : { ...JSON.parse(JSON.stringify(base)), cells: [...base.cells, appended], rev: 'r2' }
      }),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    await vm.handleNotebooksChanged({ notebookId: 'nb1', appendedCellIds: ['new1'] })
    await flushPromises()
    expect(vm.cells.map((c: any) => c.id)).toContain('new1')
    await vm.flushSave()
    await flushPromises()
    const sent = (app.SaveNotebook as any).mock.calls.at(-1)[0]
    expect(sent.rev).toBe('r2')
  })

  // #220: an agent's update_cell / delete_cell / move_cell changes existing cells,
  // which an append merge cannot show; the open copy reloads, as for a change on disk.
  it('reloads the open notebook when an agent edited its cells and nothing is unsaved', async () => {
    const base = { ...JSON.parse(JSON.stringify(BAR_NOTEBOOK)), rev: 'r1' }
    let calls = 0
    makeApp({
      GetNotebook: vi.fn(async () => {
        calls++
        const nb = JSON.parse(JSON.stringify(base))
        if (calls > 1) {
          nb.cells = [{ id: 'agent-edit', type: 'markdown', collapsed: false, source: 'edited by the assistant' }]
          nb.rev = 'r2'
        }
        return nb
      }),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    await vm.handleNotebooksChanged({ notebookId: 'nb1', changed: true })
    await flushPromises()
    await flushPromises()
    expect(vm.cells.map((c: any) => c.id)).toEqual(['agent-edit'])
  })

  it('keeps the old rev, and says so, when the remote notebook changed in some other way too', async () => {
    const base = { ...JSON.parse(JSON.stringify(BAR_NOTEBOOK)), rev: 'r1' }
    const appended = { id: 'new1', type: 'markdown', collapsed: false, source: 'agent cell' }
    let calls = 0
    const app = makeApp({
      GetNotebook: vi.fn(async () => {
        calls++
        return calls === 1
          ? JSON.parse(JSON.stringify(base))
          : { ...JSON.parse(JSON.stringify(base)), title: 'Retitled by a git pull', cells: [...base.cells, appended], rev: 'r2' }
      }),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    await vm.handleNotebooksChanged({ notebookId: 'nb1', appendedCellIds: ['new1'] })
    await flushPromises()
    expect((wrapper.vm as any).conflict).toEqual({ deleted: false })
    expect(wrapper.find('[data-testid="conflict-banner"]').exists()).toBe(true)
    // Saving is held back until the person chooses, so nothing is overwritten.
    await vm.flushSave()
    await flushPromises()
    expect(app.SaveNotebook).not.toHaveBeenCalled()
  })

  it('drops a queued append event for a notebook that is no longer loaded (superseded)', async () => {
    let resolveNb2Load: ((v: any) => void) | null = null
    const NB2 = { schemaVersion: 1, id: 'nb2', title: 'NB2', updatedAt: 't', cells: [] as any[] }
    makeApp({
      GetNotebook: vi.fn((id: string) => {
        if (id === 'nb1') return Promise.resolve(JSON.parse(JSON.stringify(BAR_NOTEBOOK)))
        return new Promise((resolve) => { resolveNb2Load = resolve })
      }),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    ;(globalThis as any).__mockRoute.params.id = 'nb2'
    await flushPromises()
    await flushPromises()
    expect(vm.getLoadedNotebookId()).toBe('nb2')

    // An event for the notebook we already LEFT (nb1) must never queue or apply.
    await vm.handleNotebooksChanged({ notebookId: 'nb1', appendedCellIds: ['ghost'] })
    resolveNb2Load!(JSON.parse(JSON.stringify(NB2)))
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(vm.cells.map((c: any) => c.id)).not.toContain('ghost')
  })

  // --- #91: export confirms with a toast ---
  it('exportNotebook shows a status toast with the written path', async () => {
    const app = makeApp({ ExportNotebook: vi.fn(async () => '/tmp/nb1.json') })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.find('[data-testid="export-notebook"]').trigger('click')
    await flushPromises()

    expect(app.ExportNotebook).toHaveBeenCalledWith('nb1', '')
    expect(wrapper.find('[data-testid="notebook-status"]').text()).toContain('/tmp/nb1.json')
  })

  // --- #95: SQL hidden by default behind a slide-out drawer ---

  it('loads with SQL hidden: closed drawer, no mounted editor, result still renders', async () => {
    makeApp({ GetNotebook: vi.fn(async () => JSON.parse(JSON.stringify(MIXED_NOTEBOOK))) })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    const drawer = wrapper.find('[data-testid="sql-drawer"]')
    expect(drawer.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('[data-testid="sql-drawer-body"]').attributes('inert')).toBeDefined()
    expect(drawer.text()).toContain('Show SQL · 1 line · SELECT region')
    expect(wrapper.find('[data-testid="view-bar"]').exists()).toBe(true)

    const vm = wrapper.vm as any
    // c1 (non-empty source) never got an editor; c2 (empty source) did.
    const c1Calls = createSlawEditor.mock.calls.filter((c: any) => c[1]?.value === vm.cells[0].source)
    expect(c1Calls).toHaveLength(0)
  })

  it('mounts the editor lazily, exactly once, and keeps it mounted across a close/reopen', async () => {
    makeApp({ GetNotebook: vi.fn(async () => JSON.parse(JSON.stringify(MIXED_NOTEBOOK))) })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    const before = createSlawEditor.mock.calls.length

    await vm.toggleSource(vm.cells[0])
    await flushPromises()
    expect(createSlawEditor.mock.calls.length).toBe(before + 1)
    const editor = vm.cells[0].editor

    await vm.toggleSource(vm.cells[0]) // close
    await vm.toggleSource(vm.cells[0]) // open again
    await flushPromises()
    expect(createSlawEditor.mock.calls.length).toBe(before + 1) // still just once
    expect(editor.destroy).not.toHaveBeenCalled()
  })

  it('loads an empty sql cell with its drawer already open and an editor mounted', async () => {
    makeApp({ GetNotebook: vi.fn(async () => JSON.parse(JSON.stringify(MIXED_NOTEBOOK))) })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    const emptyCell = vm.cells.find((c: any) => c.id === 'c2')
    expect(emptyCell.sourceOpen).toBe(true)
    expect(emptyCell.editor).toBeTruthy()
  })

  it('addCell("sql") opens the drawer and focuses the new editor', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.addCell('sql')
    await flushPromises()
    const newCell = vm.cells[vm.cells.length - 1]
    expect(newCell.sourceOpen).toBe(true)
    expect(newCell.editor.view.focus).toHaveBeenCalled()
  })

  it('opens the drawer on a run error and never auto-closes it again', async () => {
    const app = makeApp({
      GetNotebook: vi.fn(async () => JSON.parse(JSON.stringify(MIXED_NOTEBOOK))),
      RunA2UIQueryIn: vi.fn(async () => { throw new Error('boom') }),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    const c1 = vm.cells[0]
    expect(c1.sourceOpen).toBe(true)
    expect(wrapper.find('[data-testid="cell-error"]').exists()).toBe(true)

    app.RunA2UIQueryIn = vi.fn(async () => ({ columns: ['region'], rows: [['a']], truncated: false }))
    ;(window as any).go.main.App.RunA2UIQueryIn = app.RunA2UIQueryIn
    await vm.runCell(c1)
    await flushPromises()
    expect(c1.sourceOpen).toBe(true) // never auto-closes
  })

  it('an AI-inserted sql cell stays closed with no editor mounted', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    const before = createSlawEditor.mock.calls.length

    vm.insertCellFromEnvelope({ type: 'sql', sql: 'SELECT 1', label: 'x' })
    await flushPromises()
    const newCell = vm.cells[vm.cells.length - 1]
    expect(newCell.sourceOpen).toBe(false)
    expect(createSlawEditor.mock.calls.length).toBe(before)
  })

  it('replacing a cell whose drawer is open keeps it open, with a fresh editor for the new source', async () => {
    makeApp() // BAR_NOTEBOOK's c1 has a non-empty source, so it loads closed
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.toggleSource(vm.cells[0])
    await flushPromises()
    expect(vm.cells[0].sourceOpen).toBe(true)
    const oldEditor = vm.cells[0].editor
    const before = createSlawEditor.mock.calls.length

    vm.replaceCellFromEnvelope('c1', { type: 'sql', sql: 'SELECT 2', label: 'y' })
    await flushPromises()

    const replaced = vm.cells[0]
    expect(replaced.id).toBe('c1')
    expect(replaced.sourceOpen).toBe(true) // stayed open across the replace
    expect(oldEditor.destroy).toHaveBeenCalled()
    expect(replaced.editor).toBeTruthy()
    expect(replaced.editor).not.toBe(oldEditor) // recreated with the new value
    expect(createSlawEditor.mock.calls.length).toBe(before + 1)
  })

  it('toggling the drawer never schedules a save; minimizing does', async () => {
    const app = makeApp({ GetNotebook: vi.fn(async () => JSON.parse(JSON.stringify(MIXED_NOTEBOOK))) })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    app.SaveNotebook.mockClear()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      vm.toggleSource(vm.cells[0])
      vm.toggleSource(vm.cells[0])
      await vi.advanceTimersByTimeAsync(2000)
      expect(app.SaveNotebook).not.toHaveBeenCalled()

      vm.toggleCollapsed(vm.cells[0])
      await vi.advanceTimersByTimeAsync(2000)
      expect(app.SaveNotebook).toHaveBeenCalledTimes(1)
      const payload = app.SaveNotebook.mock.calls[0][0]
      const savedC1 = payload.cells.find((c: any) => c.id === 'c1')
      expect(savedC1.collapsed).toBe(true)
      expect(savedC1.sourceOpen).toBeUndefined()
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows a quiet minimized row for each cell type, with move/delete kept and view/run hidden', async () => {
    makeApp({ GetNotebook: vi.fn(async () => JSON.parse(JSON.stringify(MIXED_NOTEBOOK))) })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    for (const cell of vm.cells) vm.toggleCollapsed(cell)
    await flushPromises()

    const sqlCard = wrapper.find('[data-cell-id="c1"]')
    expect(sqlCard.find('[data-testid="cell-min-title"]').text()).toBe('by region')
    expect(sqlCard.find('[data-testid="cell-min-meta"]').text()).toBe('3 rows')
    expect(sqlCard.find('[data-testid="view-bar"]').exists()).toBe(false)
    expect(sqlCard.find('[data-testid="run-cell"]').exists()).toBe(false)
    expect(sqlCard.find('button[aria-label="Move cell up"]').exists()).toBe(true)
    expect(sqlCard.find('button[aria-label="Delete cell"]').exists()).toBe(true)
    expect(sqlCard.find('[data-testid="cell-minimize"]').attributes('aria-label')).toBe('Expand by region')

    const mdCard = wrapper.find('[data-cell-id="c3"]')
    expect(mdCard.find('[data-testid="cell-min-title"]').text()).toBe('Intro')

    const a2uiCard = wrapper.find('[data-cell-id="c4"]')
    expect(a2uiCard.find('[data-testid="cell-min-title"]').text()).toBe('KPIs')
  })

  it('renders the a2ui drawer labeled "Advanced: card definition", closed with no <pre> until clicked', async () => {
    makeApp({ GetNotebook: vi.fn(async () => JSON.parse(JSON.stringify(MIXED_NOTEBOOK))) })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    const drawer = wrapper.find('[data-testid="a2ui-drawer"]')
    expect(drawer.text()).toContain('Advanced: card definition')
    expect(wrapper.find('[data-testid="a2ui-drawer-body"] pre').exists()).toBe(false)

    await drawer.trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="a2ui-drawer-body"] pre').text()).toContain('"title": "KPIs"')
  })

  // #95 carry-over (lastPersisted guard): a visit that never edits anything
  // must never overwrite a newer save that may have landed elsewhere. Uses
  // a markdown-only notebook (no sql cell) so there's no view auto-fit to
  // count as a "change" — BAR_NOTEBOOK's sql cell auto-fits view on its
  // first run, which is itself a real local change worth persisting.
  it('a no-edit visit never calls SaveNotebook on unmount', async () => {
    const MD_ONLY_NOTEBOOK = {
      schemaVersion: 1, id: 'nb1', title: 'Notes', updatedAt: '2026-01-01T00:00:00Z',
      cells: [{ id: 'c1', type: 'markdown', collapsed: false, source: 'hello' }],
    }
    const app = makeApp({ GetNotebook: vi.fn(async () => JSON.parse(JSON.stringify(MD_ONLY_NOTEBOOK))) })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    app.SaveNotebook.mockClear()
    wrapper.unmount()
    mountedWrappers = mountedWrappers.filter((w) => w !== wrapper)
    await flushPromises()

    expect(app.SaveNotebook).not.toHaveBeenCalled()
  })

  it('an edited visit calls SaveNotebook once on unmount, with the edit', async () => {
    const app = makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    app.SaveNotebook.mockClear()
    vm.title = 'Renamed'
    await flushPromises()

    wrapper.unmount()
    mountedWrappers = mountedWrappers.filter((w) => w !== wrapper)
    await flushPromises()

    expect(app.SaveNotebook).toHaveBeenCalledTimes(1)
    expect(app.SaveNotebook.mock.calls[0][0].title).toBe('Renamed')
  })

  it('after a successful autosave with no further edits, leaving again does not save a second time', async () => {
    const app = makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    app.SaveNotebook.mockClear()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      vm.title = 'Renamed'
      await vi.advanceTimersByTimeAsync(2000)
    } finally {
      vi.useRealTimers()
    }
    await flushPromises()
    expect(app.SaveNotebook).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    mountedWrappers = mountedWrappers.filter((w) => w !== wrapper)
    await flushPromises()

    expect(app.SaveNotebook).toHaveBeenCalledTimes(1)
  })

  // SPEC 07 D: a file changed on disk while there is NO unsaved edit is reloaded
  // quietly (the sql cells re-run); with an unsaved edit the banner shows and
  // nothing is overwritten.
  it('reloads quietly when the file changes on disk and there is no unsaved edit', async () => {
    const app = makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    app.GetNotebook.mockClear()
    app.RunA2UIQueryIn.mockClear()
    await vm.handleChangedOnDisk({ kind: 'notebook', id: 'nb1', op: 'changed' })
    await flushPromises()
    await flushPromises()

    expect(app.GetNotebook).toHaveBeenCalledTimes(1)
    expect(app.RunA2UIQueryIn).toHaveBeenCalled()
    expect(vm.conflict).toBeNull()
  })

  it('ignores a change to a different notebook', async () => {
    const app = makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    app.GetNotebook.mockClear()
    await (wrapper.vm as any).handleChangedOnDisk({ kind: 'notebook', id: 'other', op: 'changed' })
    expect(app.GetNotebook).not.toHaveBeenCalled()
  })

  it('shows the conflict banner, and does not reload, when the file changes on disk under an unsaved edit', async () => {
    const app = makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.title = 'My unsaved rename'
    await flushPromises()
    app.GetNotebook.mockClear()
    await vm.handleChangedOnDisk({ kind: 'notebook', id: 'nb1', op: 'changed' })
    await flushPromises()

    expect(vm.conflict).toEqual({ deleted: false })
    expect(wrapper.find('[data-testid="conflict-banner"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('This notebook changed on disk')
    // Only the revision is read (for "Keep mine"); the notebook is not reloaded.
    expect(app.GetNotebook).toHaveBeenCalledTimes(1)
    expect(vm.title).toBe('My unsaved rename')
  })

  it('a save refused as a conflict shows the banner and stops autosaving', async () => {
    const app = makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    app.SaveNotebook.mockRejectedValueOnce(new Error('conflict: notebooks/nb1.slnb changed on disk since it was loaded'))
    vm.title = 'Edited'
    await flushPromises()
    await expect(vm.flushSave()).resolves.toBeUndefined()
    await flushPromises()
    expect(vm.conflict).toEqual({ deleted: false })

    // While the banner shows, a pending-save flush reports that nothing was written.
    app.SaveNotebook.mockClear()
    vm.title = 'Edited again'
    await flushPromises()
    expect(app.SaveNotebook).not.toHaveBeenCalled()
  })

  it('"Keep mine" overwrites through ResolveConflict and adopts the new rev; "Reload" re-reads the file', async () => {
    const app = makeApp({
      ResolveConflict: vi.fn(async () => ({ id: 'nb1', rev: 'r9', title: 'x' })),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.title = 'Mine'
    await flushPromises()
    await vm.handleChangedOnDisk({ kind: 'notebook', id: 'nb1', op: 'changed' })
    expect(vm.conflict).not.toBeNull()
    await vm.resolveConflict('overwrite')
    await flushPromises()
    expect(app.ResolveConflict).toHaveBeenCalledWith(expect.objectContaining({ kind: 'notebook', id: 'nb1', action: 'overwrite', data: expect.objectContaining({ title: 'Mine' }) }))
    expect(vm.conflict).toBeNull()

    vm.title = 'Mine again'
    await flushPromises()
    await vm.handleChangedOnDisk({ kind: 'notebook', id: 'nb1', op: 'changed' })
    app.GetNotebook.mockClear()
    await vm.resolveConflict('reload')
    await flushPromises()
    expect(app.GetNotebook).toHaveBeenCalledTimes(1)
    expect(vm.conflict).toBeNull()
  })

  it('save mine as a copy writes the copy and shows the disk version', async () => {
    const app = makeApp({
      ResolveConflict: vi.fn(async () => ({ id: 'nb1-copy', rev: 'r1', title: 'My Notebook (copy)' })),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.title = 'Mine'
    await flushPromises()
    await vm.handleChangedOnDisk({ kind: 'notebook', id: 'nb1', op: 'changed' })
    app.GetNotebook.mockClear()
    await vm.resolveConflict('saveCopy')
    await flushPromises()
    expect(app.ResolveConflict).toHaveBeenCalledWith(expect.objectContaining({ action: 'saveCopy' }))
    expect(app.GetNotebook).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('My Notebook (copy)')
  })

  it('"Keep mine" sends the revision the banner was raised against, and re-raises if the file changed again', async () => {
    let n = 0
    const app = makeApp({
      GetNotebook: vi.fn(async () => ({ ...JSON.parse(JSON.stringify(BAR_NOTEBOOK)), rev: n++ === 0 ? 'r1' : 'r2' })),
      ResolveConflict: vi.fn(async () => { throw new Error('conflict: notebooks/nb1.slnb changed on disk since it was loaded') }),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.title = 'Mine'
    await flushPromises()
    await vm.handleChangedOnDisk({ kind: 'notebook', id: 'nb1', op: 'changed' })
    await flushPromises()
    await vm.resolveConflict('overwrite')
    await flushPromises()
    expect(app.ResolveConflict).toHaveBeenCalledWith(expect.objectContaining({ action: 'overwrite', rev: 'r2' }))
    expect(vm.conflict).not.toBeNull() // still raised, nothing was overwritten
    expect(wrapper.text()).toContain('changed on disk again')
  })
})

// #117: a cell's connection survives AI Apply (review #116 follow-up) and is
// saved with the cell.
describe('NotebookView connections (#117)', () => {
  beforeEach(() => {
    vi.useRealTimers()
    ;(globalThis as any).__mockRoute.params.id = 'nb1'
    createSlawEditor.mockClear()
  })

  it('an AI proposal that names a connection keeps it when inserted', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.insertCellFromEnvelope({ type: 'sql', sql: 'SELECT 1', label: 'x', connection: 'prod_pg' })
    await flushPromises()
    expect(vm.cells[vm.cells.length - 1].connection).toBe('prod_pg')
  })

  it('an AI proposal that names a connection keeps it when it replaces a cell', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.replaceCellFromEnvelope('c1', { type: 'sql', sql: 'SELECT 2', connection: 'prod_pg' })
    await flushPromises()
    expect(vm.cells[0].connection).toBe('prod_pg')
  })

  it('a replacing proposal with no connection keeps the cell\'s own connection', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.cells[0].connection = 'prod_pg'
    vm.replaceCellFromEnvelope('c1', { type: 'sql', sql: 'SELECT 2' })
    await flushPromises()
    expect(vm.cells[0].connection).toBe('prod_pg')
  })

  it('a proposal with no connection leaves the cell on the workspace default', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.insertCellFromEnvelope({ type: 'sql', sql: 'SELECT 1' })
    await flushPromises()
    expect(vm.cells[vm.cells.length - 1].connection).toBe('')
  })

  it('the cell chip shows the default until a connection is chosen, and the choice is saved', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const app = makeApp()
    ;(app as any).ListConnections = vi.fn(async () => ({
      connections: [
        { name: 'local', type: 'duckdb', scope: 'shared', readOnly: false, default: true, status: 'ok', remote: false },
        { name: 'prod_pg', type: 'postgres', scope: 'local', readOnly: true, default: false, status: 'ok', remote: true },
      ],
      relaunchRequired: false,
    }))
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    const vm = wrapper.vm as any
    const chip = wrapper.find('[data-testid="conn-select"]')
    expect(chip.text()).toContain('Default')
    vm.cells[0].connection = 'prod_pg'
    await flushPromises()
    vi.advanceTimersByTime(5000)
    await flushPromises()
    expect(app.SaveNotebook).toHaveBeenCalledWith(
      expect.objectContaining({ cells: [expect.objectContaining({ id: 'c1', connection: 'prod_pg' })] }),
      expect.anything(),
    )
    vi.useRealTimers()
  })

  // Evidence (docs/plans/run-receipts.md R4a): a sql cell's footer opens the shared
  // drawer for its last run, and an a2ui cell's components say which cell asked.
  it('a sql cell with a receipt has an Evidence button that opens the drawer for the cell', async () => {
    _resetEvidenceForTests()
    const app = makeApp({
      RunA2UIQueryIn: vi.fn(async () => ({ columns: ['n'], types: ['BIGINT'], rows: [[1]], truncated: false, receipt_id: 'r_cell' })),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    expect(app.RunA2UIQueryIn).toHaveBeenCalledWith(BAR_NOTEBOOK.cells[0].source, '', { kind: 'cell', notebook: 'nb1', cell: 'c1' })
    const btn = wrapper.find('[data-testid="cell-evidence"]')
    expect(btn.exists()).toBe(true)
    await btn.trigger('click')
    expect(useEvidenceDrawer().target.value).toMatchObject({ title: 'by region', receiptId: 'r_cell' })
    expect(useEvidenceDrawer().target.value?.memory?.sql).toBe(BAR_NOTEBOOK.cells[0].source)
    _resetEvidenceForTests()
  })

  it('a sql cell whose run has no receipt and no note has no Evidence button', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    expect(wrapper.find('[data-testid="cell-evidence"]').exists()).toBe(false)
  })

  it('a sql cell with only a note still has the button, and the drawer gets the note', async () => {
    _resetEvidenceForTests()
    makeApp({
      RunA2UIQueryIn: vi.fn(async () => ({ columns: ['n'], types: ['BIGINT'], rows: [[1]], truncated: false, receipt_note: 'not_kept' })),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    await wrapper.find('[data-testid="cell-evidence"]').trigger('click')
    expect(useEvidenceDrawer().target.value).toMatchObject({ note: 'not_kept' })
    expect(useEvidenceDrawer().target.value?.receiptId).toBeUndefined()
    _resetEvidenceForTests()
  })

  it('an a2ui cell’s data-bound component runs with its notebook, cell and component id', async () => {
    const nb = JSON.parse(JSON.stringify(BAR_NOTEBOOK))
    nb.cells = [{ id: 'ui1', type: 'a2ui', collapsed: false, page: { title: 'KPIs', components: [{ id: 'k', type: 'stat', label: 'Revenue', sql: 'SELECT 7' }] } }]
    const app = makeApp({
      GetNotebook: vi.fn(async () => nb),
      RunA2UIQueryIn: vi.fn(async () => ({ columns: ['n'], types: ['BIGINT'], rows: [[7]], truncated: false, receipt_id: 'r_ui' })),
    })
    const wrapper = mountView()
    await flushPromises()
    await flushPromises()
    // The cell is lazily mounted when it scrolls into view; jsdom has no IntersectionObserver, so show it.
    const vm = wrapper.vm as any
    vm.cells[0].visible = true
    await flushPromises()
    await flushPromises()
    expect(app.RunA2UIQueryIn).toHaveBeenCalledWith('SELECT 7', '', { kind: 'a2ui', notebook: 'nb1', cell: 'ui1', component: 'k' })
    expect(wrapper.find('[data-testid="a2ui-evidence"]').attributes('aria-label')).toBe('Show evidence for Revenue')
  })
})
