// Component tests for inline datasheet editing in TableBrowserView (P1.5).
//
// These mount the real component against a mocked window.go.main.App and assert
// the inline edit-in-place / add-row behaviour end-to-end at the component
// boundary:
//   - double-click and single-click both open the inline editor
//   - committing a text edit calls UpdateRow(table, pkName, pkValue, {col: val})
//     with the correct PK and reloads
//   - add-row calls InsertRow and the new row appears after the data reload
//   - a rejected UpdateRow reverts the optimistic change (NOT persisted) and
//     surfaces cell-level feedback identifying the failed cell
//   - the declared SQL column type drives the editor: checkbox, date
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// Capture the "data:changed" handler so tests can drive it, mirroring
// AgentPanel.spec.ts's approach to the same wailsjs runtime module.
let dataChangedHandler: (() => void) | null = null
vi.mock('../wailsjs/runtime/runtime', () => ({
  EventsOn: (name: string, cb: () => void) => { if (name === 'data:changed') dataChangedHandler = cb },
  EventsOff: () => { dataChangedHandler = null },
}))

// A reactive stand-in for the route (#82: ?table= pre-select). Tests set
// mockRoute.query.table before mounting; it must be genuinely reactive for
// the component's `watch(() => route.query.table, ...)` to fire.
vi.mock('vue-router', async () => {
  const { reactive } = await import('vue')
  const mockRoute = reactive({ query: {} as Record<string, string> })
  ;(globalThis as any).__mockRoute = mockRoute
  return { useRoute: () => mockRoute, useRouter: () => ({ push: () => {} }) }
})

import TableBrowserView from './TableBrowserView.vue'
import { workspaceInfo, _resetForTests as resetWorkspace } from '../composables/useWorkspace'

// --- Fake backend (window.go.main.App) ------------------------------------

interface Row { [k: string]: any }

const groupsOf = (names: string[]) => [{
  connection: 'local', type: 'duckdb', status: 'ok', readOnly: false, default: true,
  tables: names.map((n) => ({ name: n, qualified: n, kind: 'table' })),
}]

function makeApp(opts: {
  rows?: Row[]
  columns?: { name: string; type: string; not_null: boolean; pk: boolean }[]
  updateRow?: (...a: any[]) => Promise<any>
  lookupRows?: any[][]
  lookupCols?: { name: string }[]
  types?: string[]
} = {}) {
  const columns = opts.columns ?? [
    { name: 'id', type: 'INTEGER', not_null: true, pk: true },
    { name: 'name', type: 'TEXT', not_null: false, pk: false },
  ]
  // store is the mutable "database" so InsertRow shows up on reload.
  const store: Row[] = (opts.rows ?? [{ id: 1, name: 'Ada' }]).map(r => ({ ...r }))

  const calls: Record<string, any[][]> = {}
  const rec = (name: string) => (...args: any[]) => { (calls[name] ??= []).push(args) }

  const toTableData = () => ({
    columns: columns.map(c => c.name),
    // types is the DuckDB ScanRows type name per column (distinct from
    // DescribeTable's DDL type above, though usually the same text) — tests
    // that care about formatCell/cellEditText fidelity pass opts.types;
    // others fall back to the declared column type, close enough for the
    // checkbox/date editorType tests that don't touch formatCell scale.
    types: opts.types ?? columns.map(c => c.type),
    rows: store.map(r => columns.map(c => r[c.name] ?? null)),
    page: 1,
    page_size: 50,
    total_rows: store.length,
    total_pages: 1,
  })

  const app = {
    ListCatalogGroups: vi.fn(async () => groupsOf(['people'])),
    DescribeTable: vi.fn(async () => ({ name: 'people', columns })),
    ExecuteQuery: vi.fn(async () => ({
      columns: opts.lookupCols ?? [{ name: 'id' }, { name: 'label' }],
      rows: opts.lookupRows ?? [],
      duration: '0s',
      row_count: (opts.lookupRows ?? []).length,
    })),
    GetTableData: vi.fn(async () => toTableData()),
    UpdateRow: vi.fn(async (table: string, pkCol: string, pkVal: any, values: Record<string, any>) => {
      rec('UpdateRow')(table, pkCol, pkVal, values)
      if (opts.updateRow) return opts.updateRow(table, pkCol, pkVal, values)
      const row = store.find(r => String(r[pkCol]) === String(pkVal))
      if (row) Object.assign(row, values)
      return { affected: 1 }
    }),
    InsertRow: vi.fn(async (table: string, values: Record<string, any>) => {
      rec('InsertRow')(table, values)
      store.push({ ...values })
      return { affected: 1 }
    }),
    DeleteRow: vi.fn(async () => ({ affected: 1 })),
    CreateTable: vi.fn(async () => {}),
    DropTable: vi.fn(async () => {}),
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return { app, store }
}

function mountView() {
  return mount(TableBrowserView, {
    global: {
      stubs: {
        // Child components are irrelevant to inline-editing behaviour.
        Icon: true,
        ConfirmDialog: true,
      },
    },
  })
}

async function openTable(wrapper: any) {
  await flushPromises()
  // Click the table in the sidebar list.
  // The Connections rail is the table list now: opening a table is a route query.
  ;(globalThis as any).__mockRoute.query.table = 'people'
  await flushPromises()
}

// data cells = all <td> except the trailing actions cell.
function dataCells(wrapper: any) {
  const tds = wrapper.findAll('tbody td')
  // exclude the actions cell (last per row); with 2 cols + 1 action = 3 tds/row
  return tds
}

describe('TableBrowserView inline datasheet editing', () => {
  beforeEach(() => {
    vi.useRealTimers()
    ;(globalThis as any).__mockRoute.query = {}
  })

  it('double-click opens an editor and Enter commits via UpdateRow with the correct PK', async () => {
    const { app } = makeApp({ rows: [{ id: 7, name: 'Ada' }] })
    const wrapper = mountView()
    await openTable(wrapper)

    // The "name" cell is the 2nd td of the row.
    const nameCell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await nameCell.trigger('dblclick')
    const input = nameCell.find('input.cell-input')
    expect(input.exists()).toBe(true)

    await input.setValue('Grace')
    await input.trigger('keydown.enter')
    await flushPromises()

    expect(app.UpdateRow).toHaveBeenCalledTimes(1)
    expect(app.UpdateRow).toHaveBeenCalledWith('people', 'id', 7, { name: 'Grace' })
    // Reloaded value is persisted.
    expect(wrapper.findAll('tbody tr')[0].findAll('td')[1].text()).toContain('Grace')
  })

  it('single-click also opens the inline editor', async () => {
    makeApp({ rows: [{ id: 1, name: 'Ada' }] })
    const wrapper = mountView()
    await openTable(wrapper)

    const nameCell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await nameCell.trigger('click')
    expect(nameCell.find('input.cell-input').exists()).toBe(true)
  })

  it('does not open an editor on the primary-key column', async () => {
    makeApp({ rows: [{ id: 1, name: 'Ada' }] })
    const wrapper = mountView()
    await openTable(wrapper)

    const pkCell = wrapper.findAll('tbody tr')[0].findAll('td')[0]
    await pkCell.trigger('dblclick')
    expect(pkCell.find('input').exists()).toBe(false)
  })

  it('add-row inserts via InsertRow and the new row appears after reload', async () => {
    const { app } = makeApp({ rows: [{ id: 1, name: 'Ada' }] })
    const wrapper = mountView()
    await openTable(wrapper)

    // Open the add-row modal.
    const addBtn = wrapper.findAll('button').find((b: any) => b.text().includes('Add row'))
    await addBtn!.trigger('click')
    await flushPromises()

    // Fill the "name" field in the modal and insert.
    const modalInputs = wrapper.findAll('.modal-overlay .field-input')
    // inputs map to columns id, name (in tableInfo order)
    await modalInputs[1].setValue('Linus')
    const insertBtn = wrapper.findAll('.modal-overlay button').find((b: any) => b.text().includes('Insert'))
    await insertBtn!.trigger('click')
    await flushPromises()

    expect(app.InsertRow).toHaveBeenCalledTimes(1)
    expect(app.InsertRow).toHaveBeenCalledWith('people', { name: 'Linus' })
    // New row visible after reload.
    expect(wrapper.find('tbody').text()).toContain('Linus')
  })

  it('rejected UpdateRow reverts the optimistic change and shows cell-level feedback', async () => {
    const { app } = makeApp({
      rows: [{ id: 1, name: 'Ada' }],
      updateRow: async () => { throw { message: 'name must be unique' } },
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const nameCell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await nameCell.trigger('dblclick')
    const input = nameCell.find('input.cell-input')
    await input.setValue('Bad')
    await input.trigger('keydown.enter')
    await flushPromises()

    expect(app.UpdateRow).toHaveBeenCalledTimes(1)
    // Optimistic change reverted: the persisted/displayed value is still Ada.
    const cellAfter = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    expect(cellAfter.text()).toContain('Ada')
    expect(cellAfter.text()).not.toContain('Bad')
    // Cell-level feedback present on the failed cell.
    expect(cellAfter.classes()).toContain('cell-error')
    expect(cellAfter.find('.cell-error-msg').text()).toContain('name must be unique')
  })

  // --- #91 M-F16: cell error keyed by table+pk+col, cleared on switch/Escape ---

  it('a cell error clears when a different table is selected', async () => {
    const { app } = makeApp({
      rows: [{ id: 1, name: 'Ada' }],
      updateRow: async () => { throw { message: 'rejected' } },
    })
    app.ListCatalogGroups = vi.fn(async () => groupsOf(['people', 'other']))
    const wrapper = mountView()
    await openTable(wrapper)

    const nameCell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await nameCell.trigger('dblclick')
    await nameCell.find('input.cell-input').setValue('Bad')
    await nameCell.find('input.cell-input').trigger('keydown.enter')
    await flushPromises()
    expect(wrapper.find('.cell-error').exists()).toBe(true)

    // DescribeTable/GetTableData are re-mocked for the second table so
    // selectTable resolves cleanly.
    app.DescribeTable = vi.fn(async () => ({ name: 'other', columns: [{ name: 'id', type: 'INTEGER', not_null: true, pk: true }] }))
    app.GetTableData = vi.fn(async () => ({ columns: ['id'], types: ['INTEGER'], rows: [[1]], page: 1, page_size: 50, total_rows: 1, total_pages: 1 }))
    ;(globalThis as any).__mockRoute.query.table = 'other'
    await flushPromises()

    expect(wrapper.find('.cell-error').exists()).toBe(false)
  })

  it('Escape clears a standing cell error when no editor is open', async () => {
    makeApp({
      rows: [{ id: 1, name: 'Ada' }],
      updateRow: async () => { throw { message: 'rejected' } },
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const nameCell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await nameCell.trigger('dblclick')
    await nameCell.find('input.cell-input').setValue('Bad')
    await nameCell.find('input.cell-input').trigger('keydown.enter')
    await flushPromises()
    expect(wrapper.find('.cell-error').exists()).toBe(true)

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(wrapper.find('.cell-error').exists()).toBe(false)
  })

  it('a multi-line rejection message shows only its first line, full text in the title', async () => {
    makeApp({
      rows: [{ id: 1, name: 'Ada' }],
      updateRow: async () => { throw { message: 'LINE 1: UPDATE people ...\n         ^\nsyntax error' } },
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const nameCell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await nameCell.trigger('dblclick')
    await nameCell.find('input.cell-input').setValue('Bad')
    await nameCell.find('input.cell-input').trigger('keydown.enter')
    await flushPromises()

    const msg = wrapper.find('.cell-error-msg')
    expect(msg.text()).toBe('LINE 1: UPDATE people ...')
    expect(msg.attributes('title')).toContain('syntax error')
  })

  it('renders a checkbox editor for boolean columns', async () => {
    makeApp({
      columns: [
        { name: 'id', type: 'INTEGER', not_null: true, pk: true },
        { name: 'active', type: 'BOOLEAN', not_null: false, pk: false },
      ],
      rows: [{ id: 1, active: false }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const activeCell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    // Read-only checkbox shown in display mode.
    expect(activeCell.find('input[type="checkbox"]').exists()).toBe(true)
    await activeCell.trigger('dblclick')
    // Editor is a checkbox, not a text input.
    expect(activeCell.find('input.cell-input').exists()).toBe(false)
    expect(activeCell.find('input[type="checkbox"]').exists()).toBe(true)
  })

  it('toggling the checkbox editor commits the boolean via UpdateRow', async () => {
    const { app } = makeApp({
      columns: [
        { name: 'id', type: 'INTEGER', not_null: true, pk: true },
        { name: 'active', type: 'BOOLEAN', not_null: false, pk: false },
      ],
      rows: [{ id: 5, active: false }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const activeCell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await activeCell.trigger('dblclick')
    const cb = activeCell.find('input[type="checkbox"]')
    ;(cb.element as HTMLInputElement).checked = true
    await cb.trigger('change')
    await flushPromises()

    expect(app.UpdateRow).toHaveBeenCalledWith('people', 'id', 5, { active: 'true' })
  })

  it('renders a date editor for date columns', async () => {
    makeApp({
      columns: [
        { name: 'id', type: 'INTEGER', not_null: true, pk: true },
        { name: 'born', type: 'DATE', not_null: false, pk: false },
      ],
      rows: [{ id: 1, born: '1815-12-10' }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const cell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await cell.trigger('dblclick')
    expect(cell.find('input[type="date"]').exists()).toBe(true)
  })

  // --- Faithful value rendering — DECIMAL scale, TIMESTAMP as text ---

  it('seeds a DECIMAL editor with its declared scale, not JS-trimmed text', async () => {
    makeApp({
      columns: [
        { name: 'id', type: 'INTEGER', not_null: true, pk: true },
        { name: 'amt', type: 'DECIMAL(18,4)', not_null: false, pk: false },
      ],
      types: ['INTEGER', 'DECIMAL(18,4)'],
      rows: [{ id: 1, amt: 12.5 }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const cell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    // Display shows the full scale too, not JS's trimmed "12.5".
    expect(cell.text()).toContain('12.5000')
    await cell.trigger('dblclick')
    const input = cell.find('input.cell-input')
    expect((input.element as HTMLInputElement).value).toBe('12.5000')
  })

  it('a TIMESTAMP column gets a text editor, not a date input, seeded with the full value', async () => {
    makeApp({
      columns: [
        { name: 'id', type: 'INTEGER', not_null: true, pk: true },
        { name: 'ts', type: 'TIMESTAMP', not_null: false, pk: false },
      ],
      types: ['INTEGER', 'TIMESTAMP'],
      rows: [{ id: 1, ts: '2025-04-06 13:45:00' }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const cell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await cell.trigger('dblclick')
    expect(cell.find('input[type="date"]').exists()).toBe(false)
    const input = cell.find('input.cell-input')
    expect(input.exists()).toBe(true)
    expect((input.element as HTMLInputElement).value).toBe('2025-04-06 13:45:00')
  })

  it('a TIME column gets a text editor seeded with the DuckDB text', async () => {
    makeApp({
      columns: [
        { name: 'id', type: 'INTEGER', not_null: true, pk: true },
        { name: 't', type: 'TIME', not_null: false, pk: false },
      ],
      types: ['INTEGER', 'TIME'],
      rows: [{ id: 1, t: '13:45:00' }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const cell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await cell.trigger('dblclick')
    expect(cell.find('input[type="date"]').exists()).toBe(false)
    expect((cell.find('input.cell-input').element as HTMLInputElement).value).toBe('13:45:00')
  })

  it('an UpdateRow for a BIGINT primary key past 2^53 targets the exact string id', async () => {
    const { app } = makeApp({
      columns: [
        { name: 'id', type: 'BIGINT', not_null: true, pk: true },
        { name: 'name', type: 'TEXT', not_null: false, pk: false },
      ],
      types: ['BIGINT', 'VARCHAR'],
      rows: [{ id: '9007199254740993', name: 'Ada' }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const nameCell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    await nameCell.trigger('dblclick')
    await nameCell.find('input.cell-input').setValue('Grace')
    await nameCell.find('input.cell-input').trigger('keydown.enter')
    await flushPromises()

    expect(app.UpdateRow).toHaveBeenCalledWith('people', 'id', '9007199254740993', { name: 'Grace' })
  })

  it('a NULL cell renders as NULL, muted, distinct from an empty string', async () => {
    makeApp({
      columns: [
        { name: 'id', type: 'INTEGER', not_null: true, pk: true },
        { name: 'note', type: 'VARCHAR', not_null: false, pk: false },
      ],
      types: ['INTEGER', 'VARCHAR'],
      rows: [{ id: 1, note: null }, { id: 2, note: '' }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const rows = wrapper.findAll('tbody tr')
    const nullCell = rows[0].findAll('td')[1]
    const emptyCell = rows[1].findAll('td')[1]
    expect(nullCell.text()).toContain('NULL')
    expect(nullCell.classes()).toContain('cell-null')
    expect(emptyCell.text()).not.toContain('NULL')
    expect(emptyCell.classes()).not.toContain('cell-null')
  })

  // The datasheet's numeric header right-aligns, and a NULL in that
  // numeric column right-aligns with it.
  it('a numeric column header gets .cell-num, and a NULL in it right-aligns', async () => {
    makeApp({
      columns: [
        { name: 'id', type: 'INTEGER', not_null: true, pk: true },
        { name: 'name', type: 'VARCHAR', not_null: false, pk: false },
        { name: 'amt', type: 'DECIMAL(12,2)', not_null: false, pk: false },
      ],
      types: ['INTEGER', 'VARCHAR', 'DECIMAL(12,2)'],
      rows: [{ id: 1, name: 'Ada', amt: null }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const headers = wrapper.findAll('thead th')
    expect(headers[2].classes()).toContain('cell-num')
    expect(headers[1].classes()).not.toContain('cell-num')

    const amtCell = wrapper.findAll('tbody tr')[0].findAll('td')[2]
    expect(amtCell.classes()).toContain('cell-null')
    expect(amtCell.classes()).toContain('cell-num')
  })

  // A STRUCT/LIST cell's editor opens seeded with the exact JSON text
  // the cell displays, not
  // String(value) ("[object Object]"/a bare comma-joined list).
  it('a STRUCT cell opens the editor seeded with the displayed JSON text', async () => {
    makeApp({
      columns: [
        { name: 'id', type: 'INTEGER', not_null: true, pk: true },
        { name: 'meta', type: 'STRUCT("a" INTEGER, "b" VARCHAR)', not_null: false, pk: false },
      ],
      types: ['INTEGER', 'STRUCT("a" INTEGER, "b" VARCHAR)'],
      rows: [{ id: 1, meta: { a: 1, b: 'x' } }],
    })
    const wrapper = mountView()
    await openTable(wrapper)

    const cell = wrapper.findAll('tbody tr')[0].findAll('td')[1]
    expect(cell.text()).toContain('{"a":1,"b":"x"}')
    await cell.trigger('dblclick')
    const input = cell.find('input.cell-input')
    expect((input.element as HTMLInputElement).value).toBe('{"a":1,"b":"x"}')
  })
})

// Review fix #6: an agent-driven create_table/create_record/import previously
// left the table list (and the open table's rows) stale until a manual
// reload — this view never listened for "data:changed" (DashboardView did,
// TableBrowserView didn't).
describe('TableBrowserView "data:changed" live refresh', () => {
  beforeEach(() => {
    dataChangedHandler = null
    ;(globalThis as any).window = (globalThis as any).window || {}
    ;(window as any).runtime = {} // hasWailsRuntime() gate
  })

  it('re-lists tables and reloads the open table when data:changed fires', async () => {
    const { app, store } = makeApp({ rows: [{ id: 1, name: 'Ada' }] })
    const wrapper = mountView()
    await openTable(wrapper)

    expect(app.ListCatalogGroups).toHaveBeenCalledTimes(1)
    expect(app.GetTableData).toHaveBeenCalledTimes(1)
    expect(dataChangedHandler).not.toBeNull()

    // Simulate the agent inserting a row and the backend's emitted event.
    store.push({ id: 2, name: 'Grace' })
    dataChangedHandler!()
    await flushPromises()

    expect(app.ListCatalogGroups).toHaveBeenCalledTimes(2)
    expect(app.GetTableData).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('Grace')
  })

  it('clears the open table if data:changed reveals it was dropped', async () => {
    const { app } = makeApp({ rows: [{ id: 1, name: 'Ada' }] })
    app.ListCatalogGroups.mockImplementationOnce(async () => groupsOf(['people'])) // initial mount
    const wrapper = mountView()
    await openTable(wrapper)

    app.ListCatalogGroups.mockImplementation(async () => groupsOf([])) // the assistant dropped it
    dataChangedHandler!()
    await flushPromises()

    expect(wrapper.text()).not.toContain('Ada')
    expect(wrapper.text()).toContain('No tables yet')
  })
})

// #82: "Open table →" after an import routes here with ?table=<name>.
describe('TableBrowserView ?table= pre-select', () => {
  beforeEach(() => {
    ;(globalThis as any).__mockRoute.query = {}
  })

  it('selects the table named by ?table= once the table list loads', async () => {
    ;(globalThis as any).__mockRoute.query = { table: 'people' }
    const { app } = makeApp({ rows: [{ id: 1, name: 'Ada' }] })
    const wrapper = mountView()
    await flushPromises()

    expect(app.DescribeTable).toHaveBeenCalledWith('people')
    expect(wrapper.text()).toContain('Ada')
  })

  it('ignores a ?table= value that is not in the table list', async () => {
    ;(globalThis as any).__mockRoute.query = { table: 'nope' }
    const { app } = makeApp({ rows: [{ id: 1, name: 'Ada' }] })
    const wrapper = mountView()
    await flushPromises()

    expect(app.DescribeTable).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('Ada')
  })

  it('opened read-only: New and Add row are disabled and say why', async () => {
    resetWorkspace()
    workspaceInfo.value = { readOnly: true, holder: { text: 'slaw mcp serve (pid 4182)' } } as unknown as WorkspaceInfo
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    const nw = wrapper.find('[data-testid="new-table"]')
    expect((nw.element as HTMLButtonElement).disabled).toBe(true)
    expect(nw.attributes('title')).toContain('slaw mcp serve (pid 4182)')
    await openTable(wrapper)
    expect((wrapper.find('[data-testid="add-row"]').element as HTMLButtonElement).disabled).toBe(true)
    resetWorkspace()
  })

  // #117: a read-only connection, a view or a locked file says why (SPEC 07 E).
  it('has no table list of its own: the Connections rail is the list, and a table opens from the route', async () => {
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="table-row"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pick-from-rail"]').exists()).toBe(true)
    await openTable(wrapper)
    expect(wrapper.find('[data-testid="pick-from-rail"]').exists()).toBe(false)
    expect(wrapper.find('h1').text()).toBe('people')
  })

  it('a table on a read-only connection is locked, with the reason and a way to the connection', async () => {
    const { app } = makeApp()
    ;(app as any).GetTableCapabilities = vi.fn(async () => ({ connection: 'prod_pg', editable: false, reason: 'connection-read-only', text: 'prod_pg is set to read-only, so rows can\'t be added, edited or deleted here.' }))
    const wrapper = mountView()
    await openTable(wrapper)
    const reason = wrapper.find('[data-testid="readonly-reason"]')
    expect(reason.exists()).toBe(true)
    expect(reason.text()).toContain('This table is read-only.')
    expect(reason.text()).toContain('prod_pg is set to read-only')
    expect(reason.text()).toContain('turn off Read-only for the connection')
    expect(wrapper.find('[data-testid="readonly-badge"]').exists()).toBe(true)
    const add = wrapper.find('[data-testid="add-row"]')
    expect((add.element as HTMLButtonElement).disabled).toBe(true)
    expect(add.attributes('title')).toContain('prod_pg is set to read-only')
    // no delete affordance, and a click on a cell does not start an edit
    expect(wrapper.find('button[aria-label="Delete row"]').exists()).toBe(false)
    const td = wrapper.findAll('tbody td').find((t: any) => t.text().includes('Ada'))
    await td?.trigger('click')
    expect(wrapper.find('.cell-input').exists()).toBe(false)
    expect(td?.attributes('aria-readonly')).toBe('true')
  })

  it('opens a qualified table in a schema other than the connection default (lake.warehouse.x from the rail)', async () => {
    const { app } = makeApp()
    const wrapper = mountView()
    await flushPromises()
    ;(globalThis as any).__mockRoute.query.table = 'local.warehouse.dim_students'
    await flushPromises()
    expect(wrapper.find('h1').text()).toBe('local.warehouse.dim_students')
    expect(app.DescribeTable).toHaveBeenCalledWith('local.warehouse.dim_students')
  })

  it('keeps a table in another schema open when the data changes', async () => {
    dataChangedHandler = null
    ;(window as any).runtime = {} // hasWailsRuntime() gate
    makeApp()
    const wrapper = mountView()
    await flushPromises()
    ;(globalThis as any).__mockRoute.query.table = 'local.warehouse.dim_students'
    await flushPromises()
    expect(wrapper.find('h1').text()).toBe('local.warehouse.dim_students')
    expect(dataChangedHandler).not.toBeNull()
    await dataChangedHandler!()
    await flushPromises()
    expect(wrapper.find('h1').exists()).toBe(true)
    expect(wrapper.find('h1').text()).toBe('local.warehouse.dim_students')
  })

  it('a lake whose catalog is in object storage says so, not "turn off Read-only"', async () => {
    const { app } = makeApp()
    ;(app as any).GetTableCapabilities = vi.fn(async () => ({ connection: 'lake', editable: false, reason: 'connection-read-only', text: 'lake is set to read-only, so rows can\'t be added, edited or deleted here.' }))
    ;(app as any).GetConnection = vi.fn(async () => ({ def: { name: 'lake', type: 'ducklake', path: 's3://bucket/warehouse/ducklake/catalog.sqlite' } }))
    const wrapper = mountView()
    await openTable(wrapper)
    const reason = wrapper.find('[data-testid="readonly-reason"]')
    expect(reason.text()).toContain("catalog is in object storage, so it can only be read")
    expect(reason.text()).not.toContain('turn off Read-only')
    expect(reason.find('button').exists()).toBe(false)
  })

  it('a lake with a local catalog keeps the generic advice and the Edit connection button', async () => {
    const { app } = makeApp()
    ;(app as any).GetTableCapabilities = vi.fn(async () => ({ connection: 'lake', editable: false, reason: 'connection-read-only', text: 'lake is set to read-only.' }))
    ;(app as any).GetConnection = vi.fn(async () => ({ def: { name: 'lake', type: 'ducklake', path: 'data/lake.ducklake' } }))
    const wrapper = mountView()
    await openTable(wrapper)
    const reason = wrapper.find('[data-testid="readonly-reason"]')
    expect(reason.text()).toContain('turn off Read-only for the connection')
    expect(reason.find('button').exists()).toBe(true)
  })

  it('an editable table shows no banner', async () => {
    const { app } = makeApp()
    ;(app as any).GetTableCapabilities = vi.fn(async () => ({ connection: 'local', editable: true, reason: '', text: '' }))
    const wrapper = mountView()
    await openTable(wrapper)
    expect(wrapper.find('[data-testid="readonly-reason"]').exists()).toBe(false)
    expect((wrapper.find('[data-testid="add-row"]').element as HTMLButtonElement).disabled).toBe(false)
  })
})

describe('TableBrowserView when the rows cannot be read', () => {
  beforeEach(() => {
    vi.useRealTimers()
    ;(globalThis as any).__mockRoute.query = {}
    resetWorkspace()
    workspaceInfo.value = { isGit: true, root: '/w', name: 'A', sessionId: 's' } as unknown as WorkspaceInfo
  })

  it('shows the error Go sent in the grid, not an empty table, and clears it when a load works', async () => {
    const { app } = makeApp()
    const msg = "data at s3://other/ is outside every storage connection's scope: add one whose scope contains it"
    app.GetTableData.mockRejectedValueOnce(new Error(msg))
    const wrapper = mountView()
    await openTable(wrapper)
    const alert = wrapper.find('[data-testid="table-load-error"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain(msg)
    expect(wrapper.find('table.brutal-table').exists()).toBe(false)
    // the next load that works replaces it with the rows
    dataChangedHandler?.()
    await flushPromises()
    expect(wrapper.find('[data-testid="table-load-error"]').exists()).toBe(false)
    expect(wrapper.find('table.brutal-table').exists()).toBe(true)
  })
})
