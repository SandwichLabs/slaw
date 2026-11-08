import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {}, EventsOff: () => {} }))

import ConnectionDialogs from './ConnectionDialogs.vue'
import { _resetConnectionsForTests, signInFor, extensionFor, connectionList } from '../composables/useConnections'

const PG: ConnTypeSpec = {
  id: 'postgres', label: 'PostgreSQL', description: '', fields: [], requiresExtension: ['postgres'], fingerprintFields: [], signInMethods: [], soon: false,
  credentials: [
    { name: 'user', label: 'User name', kind: 'text', required: true, secret: false, perPerson: true },
    { name: 'password', label: 'Password', kind: 'text', required: false, secret: true, perPerson: true },
  ],
  capabilities: { catalog: true, writable: true, readOnlyAttach: false, remote: true, test: true },
}
const detail = (over: Partial<ConnectionDetail> = {}): ConnectionDetail => ({
  name: 'prod_pg', scope: 'shared', def: { name: 'prod_pg', type: 'postgres' }, fingerprint: '', shadows: [], signIn: 'keychain',
  credential: { method: 'keychain', username: 'rosa', secretSet: true, fingerprintMatches: false, available: true },
  info: { name: 'prod_pg', type: 'postgres', scope: 'shared', readOnly: true, default: false, status: 'needs-credentials', remote: true, serverChanged: { old: 'db.internal', new: 'db-new.internal' } }, ...over,
})

function setup(d: ConnectionDetail) {
  const app = {
    GetConnection: vi.fn(async () => d),
    ListConnectionTypes: vi.fn(async () => [PG]),
    ConnectWithCredentials: vi.fn(async () => null),
    InstallExtension: vi.fn(async () => {}),
    ListConnections: vi.fn(async () => ({ connections: [], relaunchRequired: false })),
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}
const mountD = () => mount(ConnectionDialogs, { attachTo: document.body, global: { stubs: { Icon: true } } })

describe('connection prompts', () => {
  beforeEach(() => { _resetConnectionsForTests(); document.body.innerHTML = '' })

  it('the server-changed prompt uses the SPEC copy, prefills the readable username, and confirms with new credentials', async () => {
    const app = setup(detail())
    const w = mountD()
    signInFor.value = 'prod_pg'
    await flushPromises()
    const d = w.find('[data-testid="signin-dialog"]')
    expect(d.text()).toContain('Sign in to prod_pg again')
    expect(d.text()).toContain("prod_pg's server changed since you signed in (db.internal to db-new.internal). Sign in again to confirm it's the one you mean.")
    expect(d.text()).toContain("so it isn't sent to the new one")
    expect((w.find('[data-testid="signin-user"]').element as HTMLInputElement).value).toBe('rosa')
    expect(w.find('[data-testid="signin-password"]').attributes('type')).toBe('password')
    await w.find('[data-testid="signin-password"]').setValue('new-pw')
    expect(w.find('[data-testid="signin-submit"]').text()).toBe('Confirm and connect')
    await w.find('[data-testid="signin-dialog"] form').trigger('submit')
    await flushPromises()
    expect(app.ConnectWithCredentials).toHaveBeenCalledWith(expect.objectContaining({ name: 'prod_pg', username: 'rosa', password: 'new-pw', remember: true }))
    expect(signInFor.value).toBeNull()
  })

  it('a plain first sign-in says what is missing and "Remember" starts on for the keychain, off for ask-each-time', async () => {
    setup(detail({ credential: { method: 'keychain', secretSet: false, fingerprintMatches: true, available: true }, info: { ...detail().info, serverChanged: undefined } }))
    const w = mountD()
    signInFor.value = 'prod_pg'
    await flushPromises()
    expect(w.find('[data-testid="signin-dialog"]').text()).toContain('Sign in to prod_pg')
    expect(w.find('[data-testid="signin-dialog"]').text()).toContain("doesn't have a username and password for this connection on this computer yet")
    expect((w.find('[data-testid="signin-remember"]').element as HTMLInputElement).checked).toBe(true)
    signInFor.value = null
    await flushPromises()
    setup(detail({ signIn: 'prompt', info: { ...detail().info, serverChanged: undefined } }))
    signInFor.value = 'prod_pg'
    await flushPromises()
    expect((w.find('[data-testid="signin-remember"]').element as HTMLInputElement).checked).toBe(false)
  })

  it('an iceberg_catalog prompt asks only for the credentials its auth signs in with', async () => {
    const cond = (auth: string) => [{ field: 'endpoint_type', in: ['rest'] }, { field: 'auth', in: [auth] }]
    const ICE: ConnTypeSpec = {
      ...PG, id: 'iceberg_catalog', requiresExtension: [],
      fields: [
        { name: 'endpoint_type', label: 'Catalog kind', kind: 'enum', required: true, secret: false, perPerson: false, option: true },
        { name: 'auth', label: 'Authentication', kind: 'enum', default: 'oauth2', required: false, secret: false, perPerson: false, option: true },
      ],
      credentials: [
        { name: 'token', label: 'Bearer token', kind: 'text', required: false, secret: true, perPerson: true, visibleWhen: cond('bearer') },
        { name: 'key_id', label: 'OAuth2 client id', kind: 'text', required: false, secret: false, perPerson: true, visibleWhen: cond('oauth2') },
        { name: 'secret', label: 'OAuth2 client secret', kind: 'text', required: false, secret: true, perPerson: true, visibleWhen: cond('oauth2') },
      ],
    }
    const app = setup(detail({ def: { name: 'cat', type: 'iceberg_catalog', options: { endpoint_type: 'rest', auth: 'bearer' } }, info: { ...detail().info, serverChanged: undefined } }))
    app.ListConnectionTypes.mockResolvedValue([ICE])
    const w = mountD()
    signInFor.value = 'cat'
    await flushPromises()
    expect([w.find('[data-testid="signin-token"]').exists(), w.find('[data-testid="signin-key_id"]').exists(), w.find('[data-testid="signin-secret"]').exists()]).toEqual([true, false, false])
  })

  it('a keychain that does not answer gets the "Can\'t reach your keychain" dialog', async () => {
    setup(detail({ credential: { method: 'keychain', secretSet: false, fingerprintMatches: true, available: false, detail: 'x' }, info: { ...detail().info, serverChanged: undefined } }))
    const w = mountD()
    signInFor.value = 'prod_pg'
    await flushPromises()
    expect(w.find('[data-testid="signin-dialog"]').text()).toContain("Can't reach your keychain")
    expect(w.find('[data-testid="signin-edit"]').exists()).toBe(true)
  })

  it('the extension prompt downloads nothing until Install is pressed, and offers Not now', async () => {
    const app = setup(detail())
    connectionList.value = [{ name: 'lake', type: 's3', scope: 'shared', readOnly: true, default: false, status: 'extension-missing', remote: true, extension: 'httpfs' }]
    const w = mountD()
    extensionFor.value = { connection: 'lake', extension: 'httpfs' }
    await flushPromises()
    const d = w.find('[data-testid="extension-dialog"]')
    expect(d.text()).toContain('Install the httpfs extension?')
    expect(d.text()).toContain('extensions.duckdb.org')
    expect(d.text()).toContain('Needed by 1 connection here: lake.')
    expect(app.InstallExtension).not.toHaveBeenCalled()
    await w.find('[data-testid="extension-install"]').trigger('click')
    await flushPromises()
    expect(app.InstallExtension).toHaveBeenCalledWith({ name: 'httpfs' })
    expect(extensionFor.value).toBeNull()
  })

  it('a failed install shows the plain message and offers Try again', async () => {
    const app = setup(detail())
    app.InstallExtension.mockRejectedValueOnce(new Error('check your network connection and try again'))
    const w = mountD()
    extensionFor.value = { connection: 'lake', extension: 'httpfs' }
    await flushPromises()
    await w.find('[data-testid="extension-install"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="extension-error"]').text()).toContain('check your network connection')
    expect(w.find('[data-testid="extension-install"]').text()).toBe('Try again')
  })

  it('an AWS SSO sign-in shows the hint and no key fields', async () => {
    setup(detail({ signIn: 'aws-sso:dev', credential: { method: 'aws-sso:dev', secretSet: false, fingerprintMatches: true, available: true, detail: 'AWS session for profile dev has expired. Sign in, or run `aws sso login --profile dev`.' }, info: { name: 'prod_pg', type: 'postgres', scope: 'shared', readOnly: true, default: false, status: 'needs-credentials', remote: true } }))
    const w = mountD()
    signInFor.value = 'prod_pg'
    await flushPromises()
    const d = w.find('[data-testid="signin-dialog"]')
    expect(d.text()).toContain('AWS profile dev')
    expect(d.text()).toContain('aws sso login --profile dev')
    expect(d.find('input').exists()).toBe(false)
    expect(d.find('[data-testid="signin-submit"]').exists()).toBe(false)
  })
})
