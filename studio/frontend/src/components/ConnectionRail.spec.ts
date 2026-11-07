// ConnectionRail (#117, SPEC 05). Supplementary: the acceptance run is Rodney.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const push = vi.fn()
vi.mock('vue-router', () => ({ useRouter: () => ({ push }), useRoute: () => ({ query: {} }) }))
vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {}, EventsOff: () => {} }))

import ConnectionRail from './ConnectionRail.vue'
import { _resetConnectionsForTests, connectionList, signInFor, extensionFor } from '../composables/useConnections'
import { _resetForTests as resetWorkspace, takeOverOpen } from '../composables/useWorkspace'
import { startSignInTracking, _resetSignInForTests } from '../composables/useSignIn'
import { confirmVisible, confirmMessage, resolveConfirmChoice } from '../composables/useConfirm'

const conn = (over: Partial<ConnectionInfo>): ConnectionInfo => ({
  name: 'x', type: 'postgres', scope: 'local', readOnly: true, default: false, status: 'not-connected', remote: true, ...over,
})
const LOCAL = conn({ name: 'local', type: 'duckdb', scope: 'shared', readOnly: false, default: true, status: 'ok', remote: false })

function setup(list: ConnectionInfo[], catalog: Record<string, any> = {}) {
  const app: Record<string, any> = {
    ListConnections: vi.fn(async () => ({ connections: connectionList.value.length ? connectionList.value : list, relaunchRequired: false })),
    ListCatalog: vi.fn(async (req: any) => {
      const k = [req.connection, req.schema, req.table].join('/')
      if (catalog[k]) return catalog[k]
      throw new Error('no catalog for ' + k)
    }),
    ConnectConnection: vi.fn(async (ref: any) => {
      connectionList.value = connectionList.value.map((c) => (c.name === ref.name ? { ...c, status: 'ok' } : c))
      return null
    }),
    DisconnectConnection: vi.fn(async () => null),
    RefreshCatalog: vi.fn(async () => {}),
    DeleteConnection: vi.fn(async () => {}),
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  connectionList.value = list
  return app
}

const mountRail = () => mount(ConnectionRail, { attachTo: document.body, global: { stubs: { Icon: true, RouterLink: { template: '<a><slot/></a>' } } } })
const rows = (w: ReturnType<typeof mountRail>) => w.findAll('[role="treeitem"]')

describe('ConnectionRail', () => {
  beforeEach(() => { _resetConnectionsForTests(); resetWorkspace(); push.mockClear(); document.body.innerHTML = '' })

  it('opens the workspace\'s own local database expanded and dials nothing else', async () => {
    const app = setup([LOCAL, conn({ name: 'pg' })], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [{ kind: 'table', name: 'customers', qualified: 'local.main.customers', rows: 12400 }] } })
    const w = mountRail()
    await flushPromises()
    expect(app.ConnectConnection).not.toHaveBeenCalled()
    const names = rows(w).map((r) => r.attributes('data-name'))
    expect(names).toEqual(['local', 'customers', 'pg'])
    expect(w.text()).toContain('~12K')
    // the schema level is hidden for a connection with only main
    expect(rows(w).some((r) => r.attributes('data-kind') === 'schema')).toBe(false)
  })

  it('is a tree: one tab stop, levels, expanded state and named status icons', async () => {
    setup([LOCAL, conn({ name: 'pg', status: 'needs-credentials' })], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [] } })
    const w = mountRail()
    await flushPromises()
    expect(w.find('[role="tree"]').exists()).toBe(true)
    const items = rows(w)
    expect(items.filter((r) => r.attributes('tabindex') === '0')).toHaveLength(1)
    expect(items[0].attributes('aria-level')).toBe('1')
    expect(items[0].attributes('aria-expanded')).toBe('true')
    expect(w.findAll('[role="img"]').map((i) => i.attributes('aria-label'))).toContain('Needs credentials')
    // the detail line is the row's description
    const pg = w.find('[data-name="pg"]')
    expect(pg.attributes('aria-describedby')).toBeTruthy()
    expect(w.find('[data-testid="conn-detail"]').text()).toContain('Needs a username and password.')
  })

  it('expanding a not-connected connection connects it, then lists schemas, tables and columns lazily', async () => {
    const app = setup([LOCAL, conn({ name: 'pg' })], {
      'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [] },
      'pg//': { kind: 'schemas', schema: '', flattened: false, nodes: [{ kind: 'schema', name: 'public', rows: -1 }] },
      'pg/public/': { kind: 'tables', schema: 'public', flattened: false, nodes: [{ kind: 'table', name: 'orders', qualified: 'pg.public.orders', rows: -1 }, { kind: 'view', name: 'v_open', qualified: 'pg.public.v_open', rows: -1 }] },
      'pg/public/orders': { kind: 'columns', schema: 'public', flattened: false, nodes: [{ kind: 'column', name: 'id', dataType: 'BIGINT', pk: true, rows: -1 }, { kind: 'column', name: 'customer_id', dataType: 'BIGINT', fk: true, rows: -1 }] },
    })
    const w = mountRail()
    await flushPromises()
    await w.find('[data-name="pg"] .tn').trigger('click')
    await flushPromises()
    expect(app.ConnectConnection).toHaveBeenCalledWith({ name: 'pg', scope: '' })
    await w.find('[data-key="s:pg/public"] .tn').trigger('click')
    await flushPromises()
    await w.find('[data-key="t:pg/public/orders"] .tn').trigger('click') // a table opens the datasheet
    expect(push).toHaveBeenCalledWith({ path: '/tables', query: { table: 'pg.public.orders' } })
    // keyboard: Right expands the table into its columns
    await w.find('[data-key="t:pg/public/orders"]').trigger('keydown', { key: 'ArrowRight' })
    await flushPromises()
    const text = w.text()
    expect(text).toContain('BIGINT · PK')
    expect(text).toContain('BIGINT · FK')
    const levels = rows(w).map((r) => [r.attributes('data-name'), r.attributes('aria-level')])
    expect(levels).toContainEqual(['public', '2'])
    expect(levels).toContainEqual(['orders', '3'])
    expect(levels).toContainEqual(['id', '4'])
    expect(text).toContain('view')
  })

  it('arrows move, Left goes to the parent, Home and End jump', async () => {
    setup([LOCAL], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [{ kind: 'table', name: 'a', qualified: 'local.main.a', rows: -1 }, { kind: 'table', name: 'b', qualified: 'local.main.b', rows: -1 }] } })
    const w = mountRail()
    await flushPromises()
    await w.find('[data-name="local"]').trigger('keydown', { key: 'ArrowDown' })
    await flushPromises()
    expect(document.activeElement?.getAttribute('data-name')).toBe('a')
    await w.find('[data-name="a"]').trigger('keydown', { key: 'End' })
    await flushPromises()
    expect(document.activeElement?.getAttribute('data-name')).toBe('b')
    await w.find('[data-name="b"]').trigger('keydown', { key: 'ArrowLeft' })
    await flushPromises()
    expect(document.activeElement?.getAttribute('data-name')).toBe('local')
  })

  it('a status row opens what fixes it: sign-in, install consent, take over, editor on its test', async () => {
    setup([
      LOCAL,
      conn({ name: 'md', type: 'motherduck', status: 'needs-credentials' }),
      conn({ name: 'lake', type: 's3', status: 'extension-missing', extension: 'httpfs' }),
      conn({ name: 'ops', type: 'duckdb', remote: false, status: 'locked-by', lockedByPid: 4182 }),
      conn({ name: 'bad', status: 'error', detail: 'password was rejected' }),
    ], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [] } })
    const w = mountRail()
    await flushPromises()
    await w.find('[data-name="md"] .tn').trigger('click')
    expect(signInFor.value).toBe('md')
    await w.find('[data-name="lake"] .tn').trigger('click')
    expect(extensionFor.value).toEqual({ connection: 'lake', extension: 'httpfs' })
    await w.find('[data-name="ops"] .tn').trigger('click')
    expect(takeOverOpen.value).toBe(true)
    await w.find('[data-name="bad"] .tn').trigger('click')
    expect(push).toHaveBeenCalledWith({ path: '/connections/bad', query: { test: '1' } })
    expect(w.find('[data-name="bad"] [data-testid="conn-detail"]').text()).toContain('Can’t connect: password was rejected.')
  })

  it('a shared lake shows its warning in the rail, without opening the editor', async () => {
    setup([
      LOCAL,
      conn({ name: 'lake', type: 'ducklake', scope: 'shared', warning: 'Table metadata in this lake decides which files DuckDB reads.' }),
      conn({ name: 'mine', type: 'delta', scope: 'local' }),
    ], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [] } })
    const w = mountRail()
    await flushPromises()
    expect(w.find('[data-name="lake"] [data-testid="conn-warning"]').text()).toContain('decides which files DuckDB reads')
    expect(w.find('[data-name="mine"] [data-testid="conn-warning"]').exists()).toBe(false)
  })

  it('an AWS SSO connection that needs a sign-in says so and its Sign in runs the sign-in, not the key prompt (#131)', async () => {
    const app = setup([LOCAL, conn({ name: 'store', type: 's3', status: 'needs-credentials', catalogless: true, signIn: 'aws-sso:dev', signInNeeded: { kind: 'aws-sso', subject: 'dev', reason: 'expired' } })],
      { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [] } })
    app.StartSignIn = vi.fn(async () => ({ connection: 'store', profile: 'dev', url: 'https://device.sso.us-east-1.amazonaws.com/', code: 'ABCD-EFGH', expiresAt: '' }))
    ;(window as any).runtime = {}
    _resetSignInForTests()
    startSignInTracking()
    const w = mountRail()
    await flushPromises()
    const d = w.find('[data-name="store"] [data-testid="conn-detail"]')
    expect(d.text()).toContain('AWS session for profile dev has expired.')
    expect(d.text()).not.toContain('access keys')
    await d.find('button').trigger('click')
    await flushPromises()
    expect(app.StartSignIn).toHaveBeenCalledWith({ name: 'store', scope: '' })
    expect(signInFor.value).toBeNull()
    delete (window as any).runtime
  })

  it('Shift+F10 opens the context menu with the connection actions; Remove confirms first', async () => {
    const app = setup([LOCAL, conn({ name: 'pg', status: 'ok' })], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [] }, 'pg//': { kind: 'schemas', schema: '', flattened: false, nodes: [] } })
    const w = mountRail()
    await flushPromises()
    await w.find('[data-name="pg"]').trigger('keydown', { key: 'F10', shiftKey: true })
    await flushPromises()
    const menu = w.find('[role="menu"]')
    expect(menu.exists()).toBe(true)
    const labels = menu.findAll('[role="menuitem"]').map((m) => m.find('.t').text())
    expect(labels).toEqual(['Disconnect', 'Refresh', 'New query here', 'Edit…', 'Test', 'Duplicate', 'Remove…'])
    await menu.find('[data-testid="menu-newq"]').trigger('click')
    expect(push).toHaveBeenCalledWith({ path: '/queries', query: expect.objectContaining({ conn: 'pg' }) })
    // Remove asks first, with the SPEC copy, and only deletes on yes.
    await w.find('[data-name="pg"]').trigger('keydown', { key: 'Delete' })
    await flushPromises()
    expect(confirmVisible.value).toBe(true)
    expect(confirmMessage.value).toContain('This removes the connection from This computer. Your database isn\u2019t touched. Saved credentials in your keychain are also deleted.')
    expect(app.DeleteConnection).not.toHaveBeenCalled()
    resolveConfirmChoice('confirm')
    await flushPromises()
    expect(app.DeleteConnection).toHaveBeenCalledWith({ name: 'pg', scope: 'local' })
  })

  it('menu arrows and End focus the item they mark, across separators', async () => {
    setup([LOCAL, conn({ name: 'pg', status: 'ok' })], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [] }, 'pg//': { kind: 'schemas', schema: '', flattened: false, nodes: [] } })
    const w = mountRail()
    await flushPromises()
    await w.find('[data-name="pg"]').trigger('keydown', { key: 'F10', shiftKey: true })
    await flushPromises()
    const menu = w.find('[role="menu"]')
    const focused = () => (document.activeElement as HTMLElement | null)?.dataset.testid
    expect(focused()).toBe('menu-disconnect')
    for (const want of ['menu-refresh', 'menu-newq', 'menu-edit', 'menu-test']) {
      await menu.trigger('keydown', { key: 'ArrowDown' })
      await flushPromises()
      expect(focused()).toBe(want)
    }
    await menu.trigger('keydown', { key: 'End' })
    await flushPromises()
    expect(focused()).toBe('menu-remove')
    await menu.trigger('keydown', { key: 'ArrowDown' })
    await flushPromises()
    expect(focused()).toBe('menu-disconnect')
  })

  it('shows the first-run card when local is the only connection, and a hint when a table list is empty', async () => {
    setup([LOCAL], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [] } })
    const w = mountRail()
    await flushPromises()
    expect(w.find('[data-testid="rail-empty"]').text()).toContain('Your data somewhere else?')
    expect(w.text()).toContain('No tables yet. Import a CSV, or ask the Slaw assistant to build one.')
  })

  it('filters tables and columns of the open connections', async () => {
    setup([LOCAL], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [{ kind: 'table', name: 'orders', qualified: 'local.main.orders', rows: -1 }, { kind: 'table', name: 'customers', qualified: 'local.main.customers', rows: -1 }] } })
    const w = mountRail()
    await flushPromises()
    await w.find('[data-testid="rail-filter"]').setValue('cust')
    expect(rows(w).map((r) => r.attributes('data-name'))).toEqual(['local', 'customers'])
  })

  it('a failed connect does not expand and raises the matching prompt', async () => {
    const app = setup([LOCAL, conn({ name: 'pg' })], { 'local//': { kind: 'tables', schema: 'main', flattened: true, nodes: [] } })
    app.ConnectConnection.mockImplementation(async () => {
      connectionList.value = connectionList.value.map((c) => (c.name === 'pg' ? { ...c, status: 'needs-credentials' } : c))
      throw new Error('needs a sign-in')
    })
    app.ListConnections.mockImplementation(async () => ({ connections: connectionList.value, relaunchRequired: false }))
    const w = mountRail()
    await flushPromises()
    await w.find('[data-name="pg"] .tn').trigger('click')
    await flushPromises()
    expect(signInFor.value).toBe('pg')
    expect(w.find('[data-name="pg"]').attributes('aria-expanded')).toBe('false')
  })
})
