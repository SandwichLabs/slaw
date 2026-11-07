// ConnectionEditorView (#117, SPEC 06). Supplementary: acceptance is Rodney.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const route = { path: '/connections/new', params: {} as Record<string, string>, query: {} as Record<string, string> }
const router = { push: vi.fn(), replace: vi.fn() }
vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => router,
  onBeforeRouteLeave: () => {},
  onBeforeRouteUpdate: () => {},
}))
vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {}, EventsOff: () => {} }))

import ConnectionEditorView from './ConnectionEditorView.vue'
import { _resetConnectionsForTests, extensionFor } from '../composables/useConnections'
import { _resetForTests as resetWorkspace, workspaceInfo } from '../composables/useWorkspace'
import { confirmVisible, resolveConfirmChoice } from '../composables/useConfirm'
import { stashDraft, currentDraft, clearDraft } from '../composables/useConnectionDraft'

const field = (o: Partial<ConnField>): ConnField => ({ name: 'x', label: 'X', kind: 'text', required: false, secret: false, perPerson: false, ...o })
const TYPES: ConnTypeSpec[] = [
  { id: 'duckdb', label: 'DuckDB file', description: 'file', fields: [field({ name: 'path', label: 'File', kind: 'path', required: true })], credentials: [], requiresExtension: [], capabilities: { catalog: true, writable: true, readOnlyAttach: true, remote: false, test: false }, fingerprintFields: [], signInMethods: [], soon: false },
  {
    id: 'postgres', label: 'PostgreSQL', description: 'pg',
    fields: [field({ name: 'host', label: 'Host', required: true }), field({ name: 'port', label: 'Port', kind: 'number', default: '5432' }), field({ name: 'database', label: 'Database', required: true }), field({ name: 'sslmode', label: 'SSL mode', kind: 'enum', choices: ['require', 'disable'], option: true })],
    credentials: [field({ name: 'user', label: 'User name', required: true, perPerson: true }), field({ name: 'password', label: 'Password', secret: true, perPerson: true })],
    requiresExtension: ['postgres'], capabilities: { catalog: true, writable: true, readOnlyAttach: false, remote: true, test: true }, fingerprintFields: ['type', 'host', 'port', 'database', 'option:sslmode'], signInMethods: ['keychain', 'env', 'prompt'], soon: false,
  },
  {
    id: 'motherduck', label: 'MotherDuck', description: 'md', fields: [field({ name: 'database', label: 'Database', required: true })],
    credentials: [field({ name: 'token', label: 'Token', required: true, secret: true, perPerson: true })], requiresExtension: ['motherduck'],
    capabilities: { catalog: true, writable: true, readOnlyAttach: false, remote: true, test: true, subprocess: true }, fingerprintFields: [], signInMethods: ['keychain', 'env', 'prompt'], soon: false,
  },
  {
    id: 'httpfs', label: 'S3 bucket', description: 's3',
    fields: [field({ name: 'scope', label: 'Bucket', required: true }), field({ name: 'region', label: 'Region' }), field({ name: 'endpoint', label: 'Endpoint', option: true })],
    credentials: [field({ name: 'key_id', label: 'Key id', perPerson: true }), field({ name: 'secret', label: 'Secret', secret: true, perPerson: true })],
    requiresExtension: ['httpfs'], capabilities: { catalog: true, writable: true, readOnlyAttach: false, remote: true, test: true }, fingerprintFields: ['type', 'scope'], signInMethods: ['keychain', 'env', 'prompt'], soon: false,
  },
  {
    id: 'r2', label: 'Cloudflare R2', description: 'r2',
    fields: [field({ name: 'scope', label: 'Bucket', required: true }), field({ name: 'account_id', label: 'Account id', required: true, option: true })],
    credentials: [field({ name: 'key_id', label: 'Key id', perPerson: true }), field({ name: 'secret', label: 'Secret', secret: true, perPerson: true })],
    requiresExtension: ['httpfs'], capabilities: { catalog: true, writable: true, readOnlyAttach: false, remote: true, test: true }, fingerprintFields: ['type', 'scope'], signInMethods: ['keychain', 'env', 'prompt'], soon: false,
  },
  {
    id: 's3', label: 'Amazon S3', description: 's3 type',
    fields: [field({ name: 'scope', label: 'Scope', kind: 'url', required: true }), field({ name: 'region', label: 'Region', default: 'us-east-1' }), field({ name: 'endpoint', label: 'Endpoint', option: true }),
      field({ name: 'use_ssl', label: 'Use TLS', kind: 'enum', choices: ['true', 'false'], default: 'true', option: true })],
    credentials: [field({ name: 'key_id', label: 'Key id', perPerson: true }), field({ name: 'secret', label: 'Secret', secret: true, perPerson: true })],
    requiresExtension: ['httpfs'], capabilities: { catalog: false, writable: false, readOnlyAttach: false, remote: true, test: true }, fingerprintFields: ['type', 'scope', 'option:use_ssl'], signInMethods: ['keychain', 'env', 'prompt', 'aws-sso'], soon: false,
    presets: [
      { id: 'aws', label: 'Amazon S3', options: {} },
      { id: 'minio', label: 'MinIO', options: { endpoint: 'localhost:9000', url_style: 'path', use_ssl: 'false' } },
      { id: 'b2', label: 'Backblaze B2', options: { endpoint: 's3.<region>.backblazeb2.com', url_style: 'vhost', use_ssl: 'true' }, regionHint: 'us-west-004' },
      { id: 'other', label: 'Other S3-compatible', options: {} },
    ],
  },
  {
    id: 'azure', label: 'Azure Blob', description: 'azure type',
    fields: [field({ name: 'scope', label: 'Scope', kind: 'url', required: true }),
      field({ name: 'sign_in', label: 'Sign in with', kind: 'enum', choices: ['account_key', 'connection_string'], default: 'account_key', option: true, prominent: true }),
      field({ name: 'account_name', label: 'Storage account', option: true })],
    credentials: [field({ name: 'secret', label: 'Secret', required: true, secret: true, perPerson: true })],
    requiresExtension: ['azure'], capabilities: { catalog: false, writable: false, readOnlyAttach: false, remote: true, test: true }, fingerprintFields: ['type', 'scope'], signInMethods: ['keychain', 'env', 'prompt'], soon: false,
  },
  { id: 'future', label: 'Future', description: 'Coming soon.', fields: [], credentials: [], requiresExtension: [], capabilities: { catalog: true, writable: true, readOnlyAttach: false, remote: true, test: false }, fingerprintFields: [], signInMethods: [], soon: true },
  {
    id: 'mysql', label: 'MySQL', description: 'A MySQL server.',
    fields: [field({ name: 'host', label: 'Host', required: true }), field({ name: 'port', label: 'Port', kind: 'number', default: '3306' }), field({ name: 'database', label: 'Database', required: true }),
      field({ name: 'ssl_mode', label: 'SSL mode', kind: 'enum', choices: ['disabled', 'required'], option: true })],
    credentials: [field({ name: 'user', label: 'User name', required: true, perPerson: true }), field({ name: 'password', label: 'Password', secret: true, perPerson: true })],
    requiresExtension: ['mysql'], capabilities: { catalog: true, writable: true, readOnlyAttach: false, remote: true, test: true }, fingerprintFields: ['type', 'host', 'port', 'database', 'option:ssl_mode'], signInMethods: ['keychain', 'env', 'prompt'], soon: false,
  },
  {
    id: 'ducklake', label: 'DuckLake', description: 'A lake.',
    fields: [field({ name: 'path', label: 'Catalog (file or storage URL)', kind: 'path', required: true }), field({ name: 'data_path', label: 'Data folder', required: true, option: true })],
    credentials: [], requiresExtension: ['ducklake'], capabilities: { catalog: true, writable: true, readOnlyAttach: true, remote: true, test: true }, fingerprintFields: ['type', 'path', 'option:data_path'], signInMethods: [], soon: false,
  },
]

// Stand-in for workspace.Fingerprint: every field but the name counts. The real
// comparison is Go's (TestConnectionFingerprint_CountsRegionAndEveryOption).
const fakeFingerprint = (d: ConnectionDef) => JSON.stringify([d.type, d.host ?? '', d.port ?? 0, d.database ?? '', d.scope ?? '', d.path ?? '', d.region ?? '', d.options ?? {}])

function detail(over: Partial<ConnectionDetail> = {}): ConnectionDetail {
  const def = over.def ?? { name: 'prod_pg', type: 'postgres', host: 'db.internal', port: 5432, database: 'app' }
  return {
    name: 'prod_pg', scope: 'shared', def, fingerprint: fakeFingerprint(def), shadows: [], signIn: 'keychain',
    credential: { method: 'keychain', username: 'rosa', secretSet: true, fingerprintMatches: true, available: true },
    info: { name: 'prod_pg', type: 'postgres', scope: 'shared', readOnly: true, default: false, status: 'ok', remote: true }, ...over,
  }
}

function makeApp(over: Record<string, any> = {}) {
  const app: Record<string, any> = {
    ListConnectionTypes: vi.fn(async () => TYPES),
    ListConnections: vi.fn(async () => ({ connections: [], relaunchRequired: false })),
    ConnectionFingerprint: vi.fn(async (d: ConnectionDef) => fakeFingerprint(d)),
    GetConnection: vi.fn(async () => detail()),
    GetConnectionInScope: vi.fn(async () => detail()),
    SaveConnection: vi.fn(async (r: any) => detail({ name: r.def.name, scope: r.scope })),
    MoveConnection: vi.fn(async () => detail()),
    DeleteConnection: vi.fn(async () => {}),
    SetConnectionCredentials: vi.fn(async () => null),
    ClearConnectionCredentials: vi.fn(async () => null),
    GetConnectionCredentialStatus: vi.fn(async () => ({ method: 'keychain', secretSet: false, fingerprintMatches: true, available: true })),
    ProbeKeychain: vi.fn(async () => ({ available: true, backend: 'test' })),
    TestConnection: vi.fn(async () => ({ ok: true, status: 'ok', ms: 400, count: 3, summary: '3 tables' })),
    ...over,
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

const mountView = () => mount(ConnectionEditorView, { attachTo: document.body, global: { stubs: { Icon: true, RouterLink: { template: '<a><slot/></a>' } } } })

async function chooseType(w: ReturnType<typeof mountView>, id: string) {
  await w.find(`[data-testid="type-${id}"]`).trigger('click')
  await flushPromises()
}

describe('ConnectionEditorView', () => {
  beforeEach(() => {
    _resetConnectionsForTests(); resetWorkspace(); document.body.innerHTML = ''
    route.path = '/connections/new'; route.params = {}; route.query = {}
    router.push.mockClear(); router.replace.mockClear()
    workspaceInfo.value = { isGit: true, root: '/w', name: 'A', sessionId: 's' } as unknown as WorkspaceInfo
  })

  it('shows a type grid from the registry; a card the server marks soon is aria-disabled and not a button, and MySQL is a normal choice', async () => {
    makeApp()
    const w = mountView()
    await flushPromises()
    expect(w.find('[data-testid="type-postgres"]').element.tagName).toBe('BUTTON')
    expect(w.find('[data-testid="type-mysql"]').element.tagName).toBe('BUTTON')
    expect(w.find('[data-testid="type-mysql"]').attributes('aria-disabled')).toBeUndefined()
    const soon = w.find('[data-testid="type-future"]')
    expect(soon.element.tagName).not.toBe('BUTTON')
    expect(soon.attributes('aria-disabled')).toBe('true')
    expect(soon.text()).toContain('Coming soon')
    await soon.trigger('click')
    expect(w.find('[data-testid="f-name"]').exists()).toBe(false)
  })

  it('generates the form from type metadata: host/port/database, advanced options, readable username, write-only password', async () => {
    makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    for (const id of ['f-name', 'f-host', 'f-port', 'f-database', 'f-c-user']) expect(w.find(`[data-testid="${id}"]`).exists()).toBe(true)
    expect(w.find('[data-testid="f-sslmode"]').exists()).toBe(true) // under Advanced options
    expect(w.find('#f-c-password-in').attributes('type')).toBe('password')
    expect((w.find('[data-testid="f-port"]').element as HTMLInputElement).value).toBe('5432')
    expect(w.text()).toContain('How you’ll refer to it in SQL'.replace('’', "'"))
    // the three sign-in choices, keychain first and selected
    const radios = w.findAll('[role="radiogroup"][aria-label="How you sign in"] [role="radio"]')
    expect(radios.map((r) => r.attributes('aria-checked'))).toEqual(['true', 'false', 'false'])
  })

  it('an option with a default (s3 Use TLS) stays closed under Advanced, reads "Default (true)" and is not written; false round-trips and Default removes it', async () => {
    const app = makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 's3')
    expect((w.find('details').element as HTMLDetailsElement).open).toBe(false)
    const sel = w.find('[data-testid="f-use_ssl"]')
    expect((sel.element as HTMLSelectElement).selectedOptions[0].text).toBe('Default (true)')
    const submit = async () => {
      await w.find('[data-testid="f-name"]').setValue('store')
      await w.find('[data-testid="f-scope"]').setValue('s3://b/')
      await w.find('form').trigger('submit')
      await flushPromises()
      return app.SaveConnection.mock.calls.at(-1)![0].def
    }
    expect((await submit()).options).toBeUndefined()
    await sel.setValue('false')
    expect((await submit()).options).toEqual({ use_ssl: 'false' })
    await sel.setValue('')
    expect((await submit()).options).toBeUndefined()
  })

  it('Azure opens Advanced on a new connection (its sign-in is prominent) and writes no default', async () => {
    makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'azure')
    expect((w.find('details').element as HTMLDetailsElement).open).toBe(true)
    expect(w.find('[data-testid="f-account_name"]').exists()).toBe(true)
  })

  it('DuckLake: the required data folder is a main field with an asterisk, the hint says main, no read-only-user callout, no Team scope, and a trust warning', async () => {
    makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'ducklake')
    const label = w.find('label[for="f-data_path"]')
    expect(label.exists()).toBe(true)
    expect(label.text()).toContain('*')
    expect(w.find('details').exists()).toBe(false) // nothing left for Advanced
    expect(w.text()).toContain('.main.orders')
    expect(w.text()).not.toContain('read-only database user')
    expect(w.find('[data-testid="scope-shared"]').exists()).toBe(true)
    expect(w.find('[data-testid="scope-local"]').exists()).toBe(true)
    expect(w.find('[data-testid="trust-note"]').text()).toContain('Table metadata in this')
    // a relative location is inside the workspace folder: the note says so
    await w.find('#f-path').setValue('data/lake.ducklake')
    expect(w.find('[data-testid="trust-note"]').text()).toContain('inside the workspace folder')
    await w.find('#f-path').setValue('https://nope')
    await w.find('#f-data_path').setValue('s3://bucket/lake/')
    await w.find('#f-path').setValue('s3://bucket/x.ducklake')
    expect(w.find('[data-testid="trust-note"]').text()).not.toContain('inside the workspace folder')
  })

  it('defaults the scope to This computer; Team shows the git explainer', async () => {
    makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    expect(w.find('[data-testid="scope-local"]').attributes('aria-checked')).toBe('true')
    expect(w.find('[data-testid="scope-team-note"]').exists()).toBe(false)
    await w.find('[data-testid="scope-shared"]').trigger('click')
    expect(w.find('[data-testid="scope-team-note"]').text()).toContain('This file is committed to git.')
    expect(w.find('[data-testid="scope-team-note"]').text()).toContain('Your username is saved on this computer only.')
  })

  it('read-only is on by default with the client-side warning; MotherDuck has no read-only switch', async () => {
    makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    expect(w.find('[data-testid="f-readonly"]').attributes('aria-checked')).toBe('true')
    expect(w.text()).toContain('a safety belt inside Slaw, not a lock on the database')
    expect(w.text()).toContain('For production data, also use a read-only database user.')
    const w2 = mountView()
    await flushPromises()
    await chooseType(w2, 'motherduck')
    expect(w2.find('[data-testid="f-readonly"]').exists()).toBe(false)
    expect(w2.find('[data-testid="md-note"]').text()).toContain('read-scaling token')
  })

  it('Test sends what was typed, shows a redacted failure with details, and Save becomes "Save anyway"', async () => {
    const app = makeApp({
      TestConnection: vi.fn(async () => ({ ok: false, status: 'error', ms: 900, count: 0, error: { code: 'bad-signin', message: 'The server turned down the username or password.', details: "Unable to connect to Postgres at user='rosa' password='***' host='db'" } })),
    })
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    await w.find('[data-testid="f-name"]').setValue('prod_pg')
    await w.find('[data-testid="f-host"]').setValue('db.internal')
    await w.find('[data-testid="f-database"]').setValue('app')
    await w.find('[data-testid="f-c-user"]').setValue('rosa')
    await w.find('#f-c-password-in').setValue('typed-secret')
    await w.find('[data-testid="test-btn"]').trigger('click')
    await flushPromises()
    expect(app.TestConnection).toHaveBeenCalledWith(expect.objectContaining({
      scope: 'local', signIn: 'keychain',
      def: expect.objectContaining({ host: 'db.internal', database: 'app', port: 5432 }),
      credentials: expect.objectContaining({ username: 'rosa', password: 'typed-secret' }),
    }))
    const err = w.find('[data-testid="test-error"]')
    expect(err.attributes('role')).toBe('alert')
    expect(err.text()).toContain('The server turned down the username or password.')
    expect(w.find('[data-testid="test-details"]').text()).toContain("password='***'")
    expect(w.find('[data-testid="save-btn"]').text()).toBe('Save anyway')
  })

  it('a successful Test says how long it took and what it found', async () => {
    makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    await w.find('[data-testid="f-name"]').setValue('p')
    await w.find('[data-testid="test-btn"]').trigger('click')
    await flushPromises()
    const ok = w.find('[data-testid="test-ok"]')
    expect(ok.attributes('role')).toBe('status')
    expect(ok.text()).toContain('Connected in 0.40 seconds')
    expect(ok.text()).toContain('Found 3 tables.')
    expect(w.find('[data-testid="save-btn"]').text()).toBe('Save')
  })

  it('an extension-missing failure offers the install consent', async () => {
    makeApp({ TestConnection: vi.fn(async () => ({ ok: false, status: 'extension-missing', ms: 1, count: 0, installExtension: 'postgres', error: { code: 'extension-missing', message: 'needs the postgres extension', details: '' } })) })
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    await w.find('[data-testid="test-btn"]').trigger('click')
    await flushPromises()
    await w.find('[data-testid="test-install"]').trigger('click')
    expect(extensionFor.value).toEqual({ connection: '', extension: 'postgres' })
  })

  it('Save writes the definition to This computer, then the credential, and never sends the secret in the definition', async () => {
    const app = makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    await w.find('[data-testid="f-name"]').setValue('prod_pg')
    await w.find('[data-testid="f-host"]').setValue('db.internal')
    await w.find('[data-testid="f-database"]').setValue('app')
    await w.find('[data-testid="f-c-user"]').setValue('rosa')
    await w.find('#f-c-password-in').setValue('typed-secret')
    await w.find('form').trigger('submit')
    await flushPromises()
    const save = app.SaveConnection.mock.calls[0][0]
    expect(save).toMatchObject({ scope: 'local', isNew: true, signIn: 'keychain', def: { name: 'prod_pg', type: 'postgres', host: 'db.internal', database: 'app' } })
    expect(JSON.stringify(save)).not.toContain('typed-secret')
    expect(app.SetConnectionCredentials).toHaveBeenCalledWith(expect.objectContaining({ name: 'prod_pg', method: 'keychain', username: 'rosa', password: 'typed-secret' }))
    expect(router.replace).toHaveBeenCalledWith('/connections/prod_pg')
  })

  it('refuses to save a bad name or a missing required field, and says so', async () => {
    const app = makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    await w.find('[data-testid="f-name"]').setValue('Prod PG')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.find('[data-testid="save-error"]').text()).toContain('lowercase letters, numbers and underscores')
    await w.find('[data-testid="f-name"]').setValue('prod_pg')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.find('[data-testid="save-error"]').text()).toContain('Host is needed.')
    expect(app.SaveConnection).not.toHaveBeenCalled()
  })

  it('blocks saving a password while the keychain is unreachable and offers environment variables', async () => {
    const app = makeApp({ ProbeKeychain: vi.fn(async () => ({ available: false, backend: 'x', detail: 'no secret service' })) })
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    await w.find('[data-testid="f-name"]').setValue('p')
    await w.find('[data-testid="f-host"]').setValue('h')
    await w.find('[data-testid="f-database"]').setValue('d')
    await w.find('#f-c-password-in').setValue('typed-secret')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(app.SaveConnection).not.toHaveBeenCalled()
    expect(w.find('[data-testid="keychain-dialog"]').text()).toContain("Can't reach your keychain")
  })

  describe('editing a saved connection', () => {
    beforeEach(() => { route.path = '/connections/prod_pg'; route.params = { name: 'prod_pg' } })

    it('keeps the name immutable, shows the username, and the saved password only as dots with Replace and Clear', async () => {
      makeApp()
      const w = mountView()
      await flushPromises()
      expect(w.find('[data-testid="f-name"]').attributes('readonly')).toBeDefined()
      expect((w.find('[data-testid="f-c-user"]').element as HTMLInputElement).value).toBe('rosa')
      expect(w.find('#f-c-password-in').exists()).toBe(true) // the Replace button carries this id, not an input
      expect(w.find('#f-c-password-in').element.tagName).toBe('BUTTON')
      expect(w.find('[aria-label="Saved, hidden"]').exists()).toBe(true)
      expect(w.text()).toContain('In your keychain')
      expect(w.find('[data-testid="cred-replace"]').exists()).toBe(true)
      expect(w.html()).not.toMatch(/type="password"/)
    })

    it('Replace shows an empty password input, never the old value', async () => {
      makeApp()
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="cred-replace"]').trigger('click')
      const input = w.find('input[type="password"]')
      expect(input.exists()).toBe(true)
      expect((input.element as HTMLInputElement).value).toBe('')
    })

    it('Clear asks first, then clears just the password', async () => {
      const app = makeApp()
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="cred-clear"]').trigger('click')
      await flushPromises()
      expect(confirmVisible.value).toBe(true)
      expect(app.ClearConnectionCredentials).not.toHaveBeenCalled()
      resolveConfirmChoice('confirm')
      await flushPromises()
      expect(app.ClearConnectionCredentials).toHaveBeenCalledWith({ name: 'prod_pg', field: 'password' })
    })

    it('a saved password bound to another server is not shown as saved', async () => {
      makeApp({ GetConnectionInScope: vi.fn(async () => detail({ credential: { method: 'keychain', username: 'rosa', secretSet: true, fingerprintMatches: false, available: true } })) })
      const w = mountView()
      await flushPromises()
      expect(w.find('[aria-label="Saved, hidden"]').exists()).toBe(false)
      expect(w.find('input[type="password"]').exists()).toBe(true)
    })

    it('changing the scope moves the connection (the name is not edited)', async () => {
      const app = makeApp()
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="scope-local"]').trigger('click')
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(app.MoveConnection).toHaveBeenCalledWith({ name: 'prod_pg', to: 'local' })
      expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ isNew: false, scope: 'local' }))
    })

    it('says so when this is a private copy that overrides a Team connection', async () => {
      makeApp({ GetConnectionInScope: vi.fn(async () => detail({ scope: 'local', shadows: ['shared'] })) })
      const w = mountView()
      await flushPromises()
      expect(w.find('[data-testid="shadow-banner"]').text()).toContain('This is your private copy of prod_pg.')
      expect(w.find('[data-testid="shadow-banner"]').text()).toContain('Look at the Team one')
    })

    it('saving an edit that changes no credential never touches the saved password', async () => {
      const app = makeApp()
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="f-readonly"]').trigger('click')
      await w.find('form').trigger('submit')
      await flushPromises()
      resolveConfirmChoice('confirm') // "Allow changes to prod_pg?"
      await flushPromises()
      expect(app.SaveConnection).toHaveBeenCalled()
      expect(app.SetConnectionCredentials).not.toHaveBeenCalled()
    })

    it('changing the host puts the secret in replace state with the reason, and a save without it leaves the keychain alone', async () => {
      const app = makeApp()
      const w = mountView()
      await flushPromises()
      expect(w.find('[aria-label="Saved, hidden"]').exists()).toBe(true)
      await w.find('[data-testid="f-host"]').setValue('db-new.internal')
      await flushPromises()
      expect(w.find('[aria-label="Saved, hidden"]').exists()).toBe(false)
      expect(w.find('input[type="password"]').exists()).toBe(true)
      expect(w.text()).toContain("The server changed, so your saved password won't be used. Enter it again.")
      await w.find('form').trigger('submit')
      await flushPromises()
      resolveConfirmChoice('confirm')
      await flushPromises()
      expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ def: expect.objectContaining({ host: 'db-new.internal' }) }))
      expect(app.SetConnectionCredentials).not.toHaveBeenCalled() // the prefilled username alone is not a change
      expect(app.ClearConnectionCredentials).not.toHaveBeenCalled()
    })

    it('typing the password after a host change saves it for the new server', async () => {
      const app = makeApp()
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="f-host"]').setValue('db-new.internal')
      await w.find('input[type="password"]').setValue('new-pw')
      await w.find('form').trigger('submit')
      await flushPromises()
      resolveConfirmChoice('confirm')
      await flushPromises()
      expect(app.SetConnectionCredentials).toHaveBeenCalledWith(expect.objectContaining({ name: 'prod_pg', username: 'rosa', password: 'new-pw' }))
    })

    const ssoDetail = (over: Partial<ConnectionDetail> = {}) => {
      const def = { name: 'lake', type: 's3', scope: 's3://lake/', region: 'us-east-1' }
      return detail({ name: 'lake', def, signIn: 'aws-sso:dev', credential: { method: 'aws-sso:dev', secretSet: false, fingerprintMatches: true, available: true }, info: { ...detail().info, name: 'lake', type: 's3' }, ...over })
    }

    it('an AWS SSO sign-in is the fourth card: a profile and a session line, no key fields, saved with its real method string', async () => {
      const app = makeApp({ GetConnectionInScope: vi.fn(async () => ssoDetail()) })
      route.path = '/connections/lake'; route.params = { name: 'lake' }
      const w = mountView()
      await flushPromises()
      expect((w.find('[data-testid="f-sso-profile"]').element as HTMLInputElement).value).toBe('dev')
      expect(w.find('[data-testid="method-aws-sso"]').attributes('aria-checked')).toBe('true')
      expect(w.find('[data-testid="sso-signin-now"]').exists()).toBe(true)
      expect(w.find('[data-testid="f-c-key_id"]').exists()).toBe(false)
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(app.SaveConnection.mock.calls[0][0].signIn).toBe('aws-sso:dev')
      expect(app.SetConnectionCredentials).not.toHaveBeenCalled()
    })

    it('moving an AWS SSO connection from all my workspaces to this computer keeps its sign-in', async () => {
      const app = makeApp({ GetConnectionInScope: vi.fn(async () => ssoDetail({ scope: 'user' })) })
      route.path = '/connections/lake'; route.params = { name: 'lake' }
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="scope-local"]').trigger('click')
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(app.MoveConnection).toHaveBeenCalledWith({ name: 'lake', to: 'local' })
      expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ scope: 'local', signIn: 'aws-sso:dev' }))
    })

    it('an endpoint edit of an AWS SSO connection asks before re-binding it', async () => {
      const app = makeApp({ GetConnectionInScope: vi.fn(async () => ssoDetail()) })
      route.path = '/connections/lake'; route.params = { name: 'lake' }
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="f-region"]').setValue('eu-west-1')
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(confirmVisible.value).toBe(true)
      expect(app.SaveConnection).not.toHaveBeenCalled()
      resolveConfirmChoice('confirm')
      await flushPromises()
      expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ confirmRebind: true, signIn: 'aws-sso:dev' }))
    })

    it('an environment sign-in asks before it is re-bound to a changed server', async () => {
      const app = makeApp({ GetConnectionInScope: vi.fn(async () => detail({ signIn: 'env:PGPROD', credential: { method: 'env:PGPROD', secretSet: true, fingerprintMatches: true, available: true, env: [{ name: 'PGPROD_USER', set: true }, { name: 'PGPROD_PASSWORD', set: true }] } })) })
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="f-host"]').setValue('elsewhere.example')
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(confirmVisible.value).toBe(true)
      expect(app.SaveConnection).not.toHaveBeenCalled()
      resolveConfirmChoice('cancel')
      await flushPromises()
      expect(app.SaveConnection).not.toHaveBeenCalled()
    })

    it('switching from the keychain to ask-each-time offers to delete the saved password', async () => {
      const app = makeApp()
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="method-prompt"]').trigger('click')
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(confirmVisible.value).toBe(true) // Delete the saved sign-in?
      resolveConfirmChoice('confirm')
      await flushPromises()
      expect(app.ClearConnectionCredentials).toHaveBeenCalledWith({ name: 'prod_pg', field: '' })
    })

    it('an environment sign-in whose server changed in a pull asks before Save re-binds it, and says so on the page', async () => {
      const app = makeApp({
        GetConnectionInScope: vi.fn(async () => detail({
          signIn: 'env:PGPROD',
          credential: { method: 'env:PGPROD', secretSet: true, fingerprintMatches: false, available: true },
          info: { ...detail().info, status: 'needs-credentials', serverChanged: { old: 'db.internal:5432/app', new: 'db.internal:5432/app2' } },
        })),
      })
      const w = mountView()
      await flushPromises()
      expect(w.find('[data-testid="env-server-changed"]').text()).toContain('The server changed since you chose these variables.')
      await w.find('form').trigger('submit') // nothing edited on this page: the pulled definition is already "new"
      await flushPromises()
      expect(confirmVisible.value).toBe(true)
      expect(app.SaveConnection).not.toHaveBeenCalled()
      resolveConfirmChoice('confirm')
      await flushPromises()
      expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ confirmRebind: true, signIn: 'env:PGPROD' }))
    })

    it.each([
      ['region', 'f-region', 's3-region-2'],
      ['a non-fingerprintFields option', 'f-endpoint', 'https://other.example'],
    ])('an environment sign-in asks before Save when %s changes (Go decides what counts)', async (_what, testid, value) => {
      const def = { name: 'lake', type: 'httpfs', scope: 'bkt', region: 'us-east-1', options: { endpoint: 'https://s3.example' } }
      const app = makeApp({
        GetConnectionInScope: vi.fn(async () => detail({ name: 'lake', def, signIn: 'env:LAKE', info: { ...detail().info, name: 'lake', type: 'httpfs' } })),
      })
      const w = mountView()
      await flushPromises()
      expect(w.find('[data-testid="env-server-changed"]').exists()).toBe(false)
      await w.find(`[data-testid="${testid}"]`).setValue(value)
      await flushPromises()
      expect(app.ConnectionFingerprint).toHaveBeenCalled()
      expect(w.find('[data-testid="env-server-changed"]').exists()).toBe(true)
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(confirmVisible.value).toBe(true)
      expect(app.SaveConnection).not.toHaveBeenCalled()
      resolveConfirmChoice('confirm')
      await flushPromises()
      expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ confirmRebind: true }))
    })

    it('an untouched R2 connection (required option account_id) shows no warning, and Save sends the option', async () => {
      const def = { name: 'cf', type: 'r2', scope: 'r2://bkt/', options: { account_id: 'abc123' } }
      const app = makeApp({ GetConnectionInScope: vi.fn(async () => detail({ name: 'cf', def, signIn: 'env:CF', info: { ...detail().info, name: 'cf', type: 'r2' } })) })
      const w = mountView()
      await flushPromises()
      expect(w.find('[data-testid="env-server-changed"]').exists()).toBe(false)
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(confirmVisible.value).toBe(false)
      expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ confirmRebind: false, def: expect.objectContaining({ options: { account_id: 'abc123' } }) }))
    })

    it('an untouched DuckLake connection (required option data_path) matches its fingerprint, and Save sends the option', async () => {
      const def = { name: 'dl', type: 'ducklake', path: 'meta.ducklake', options: { data_path: 'data/' } }
      const app = makeApp({ GetConnectionInScope: vi.fn(async () => detail({ name: 'dl', def, signIn: '', info: { ...detail().info, name: 'dl', type: 'ducklake' } })) })
      const w = mountView()
      await flushPromises()
      expect(await app.ConnectionFingerprint.mock.results.at(-1)!.value).toBe(fakeFingerprint(def))
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ def: expect.objectContaining({ options: { data_path: 'data/' } }) }))
    })

    it('an unchanged definition does not ask', async () => {
      const def = { name: 'lake', type: 'httpfs', scope: 'bkt', region: 'us-east-1' }
      const app = makeApp({ GetConnectionInScope: vi.fn(async () => detail({ name: 'lake', def, signIn: 'env:LAKE', info: { ...detail().info, name: 'lake', type: 'httpfs' } })) })
      const w = mountView()
      await flushPromises()
      expect(w.find('[data-testid="env-server-changed"]').exists()).toBe(false)
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(confirmVisible.value).toBe(false)
      expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ confirmRebind: false }))
    })

    it('if the backend refuses a re-bind the screen did not foresee, it asks and sends the confirmation', async () => {
      const app = makeApp()
      app.SaveConnection.mockRejectedValueOnce(new Error('confirm-rebind: the server changed'))
      const w = mountView()
      await flushPromises()
      await w.find('[data-testid="method-env"]').trigger('click')
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(confirmVisible.value).toBe(true)
      resolveConfirmChoice('confirm')
      await flushPromises()
      expect(app.SaveConnection).toHaveBeenCalledTimes(2)
      expect(app.SaveConnection.mock.calls[1][0]).toMatchObject({ confirmRebind: true })
    })

    it('?test=1 runs the test on open (the rail\'s Fix and Test)', async () => {
      route.query = { test: '1' }
      const app = makeApp()
      mountView()
      await flushPromises()
      expect(app.TestConnection).toHaveBeenCalled()
    })
  })

  it('a duplicate never inherits the original\'s sign-in method: it starts at the keychain with nothing saved', async () => {
    route.query = { from: 'prod_pg' }
    const app = makeApp({ GetConnection: vi.fn(async () => detail({ signIn: 'env:PGPROD' })) })
    const w = mountView()
    await flushPromises()
    expect(w.find('[data-testid="method-keychain"]').attributes('aria-checked')).toBe('true')
    expect(w.find('[data-testid="method-env"]').attributes('aria-checked')).toBe('false')
    expect((w.find('[data-testid="f-name"]').element as HTMLInputElement).value).toBe('prod_pg_copy')
    await w.find('[data-testid="test-btn"]').trigger('click')
    await flushPromises()
    expect(app.TestConnection).toHaveBeenCalledWith(expect.objectContaining({ signIn: 'keychain' }))
  })

  it('turning off Read-only on a remote connection asks first', async () => {
    const app = makeApp()
    const w = mountView()
    await flushPromises()
    await chooseType(w, 'postgres')
    await w.find('[data-testid="f-name"]').setValue('p')
    await w.find('[data-testid="f-host"]').setValue('h')
    await w.find('[data-testid="f-database"]').setValue('d')
    await w.find('[data-testid="f-readonly"]').trigger('click')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(confirmVisible.value).toBe(true)
    expect(app.SaveConnection).not.toHaveBeenCalled()
    resolveConfirmChoice('confirm')
    await flushPromises()
    expect(app.SaveConnection).toHaveBeenCalledWith(expect.objectContaining({ def: expect.objectContaining({ read_only: false }) }))
  })

  describe('MotherDuck database picker (#163)', () => {
    const DBS = [{ name: 'my_db', share: false }, { name: 'sample_data', share: true }]

    it('loads the databases with the typed token, picks one into the Database field, and keeps free text', async () => {
      const app = makeApp({ ListMotherDuckDatabases: vi.fn(async () => ({ ok: true, status: 'ok', databases: DBS })) })
      const w = mountView()
      await flushPromises()
      await chooseType(w, 'motherduck')
      expect(w.find('[data-testid="md-load"]').attributes('disabled')).toBeDefined()
      await w.find('#f-c-token-in').setValue('tok_typed')
      expect(w.find('[data-testid="md-load"]').attributes('disabled')).toBeUndefined()
      await w.find('[data-testid="md-load"]').trigger('click')
      await flushPromises()
      expect(app.ListMotherDuckDatabases).toHaveBeenCalledWith(expect.objectContaining({ signIn: 'keychain', credentials: expect.objectContaining({ token: 'tok_typed' }) }))
      expect(w.find('[data-testid="md-db-sample_data"]').text()).toContain('shared')
      await w.find('[data-testid="md-db-my_db"]').trigger('click')
      expect((w.find('[data-testid="f-database"]').element as HTMLInputElement).value).toBe('my_db')
      expect(w.find('[data-testid="md-db-my_db"]').attributes('aria-pressed')).toBe('true')
      await w.find('[data-testid="f-database"]').setValue('typed_by_hand')
      expect((w.find('[data-testid="f-database"]').element as HTMLInputElement).value).toBe('typed_by_hand')
    })

    it('shows a failure in place with the install offer, and the field still works', async () => {
      makeApp({ ListMotherDuckDatabases: vi.fn(async () => ({ ok: false, status: 'extension-missing', databases: [], message: 'needs the motherduck extension', installExtension: 'motherduck' })) })
      const w = mountView()
      await flushPromises()
      await chooseType(w, 'motherduck')
      await w.find('#f-c-token-in').setValue('tok_typed')
      await w.find('[data-testid="md-load"]').trigger('click')
      await flushPromises()
      expect(w.find('[data-testid="md-error"]').text()).toContain('needs the motherduck extension')
      expect(w.find('[data-testid="md-install"]').exists()).toBe(true)
      await w.find('[data-testid="f-database"]').setValue('my_db')
      expect((w.find('[data-testid="f-database"]').element as HTMLInputElement).value).toBe('my_db')
    })

    it('sends no typed token with an environment sign-in', async () => {
      const app = makeApp({ ListMotherDuckDatabases: vi.fn(async () => ({ ok: true, status: 'ok', databases: [] })) })
      const w = mountView()
      await flushPromises()
      await chooseType(w, 'motherduck')
      await w.find('#f-c-token-in').setValue('tok_hidden')
      await w.find('[data-testid="method-env"]').trigger('click')
      await w.find('[data-testid="md-load"]').trigger('click')
      await flushPromises()
      expect(app.ListMotherDuckDatabases).toHaveBeenCalledWith(expect.objectContaining({ credentials: expect.objectContaining({ token: '' }) }))
    })

    it('discards a stale response when the token changed while it was loading', async () => {
      let release: (v: unknown) => void = () => {}
      makeApp({ ListMotherDuckDatabases: vi.fn(() => new Promise((r) => { release = r })) })
      const w = mountView()
      await flushPromises()
      await chooseType(w, 'motherduck')
      await w.find('#f-c-token-in').setValue('tok_a')
      await w.find('[data-testid="md-load"]').trigger('click')
      await w.find('#f-c-token-in').setValue('tok_b')
      release({ ok: true, status: 'ok', databases: [{ name: 'old_acct_db', share: false }] })
      await flushPromises()
      expect(w.find('[data-testid="md-db-old_acct_db"]').exists()).toBe(false)
      expect(w.find('[data-testid="md-dbs"]').exists()).toBe(false)
    })

    it('can load with an environment sign-in and no typed token', async () => {
      const app = makeApp({ ListMotherDuckDatabases: vi.fn(async () => ({ ok: true, status: 'ok', databases: [] })) })
      const w = mountView()
      await flushPromises()
      await chooseType(w, 'motherduck')
      await w.find('[data-testid="method-env"]').trigger('click')
      await w.find('[data-testid="md-load"]').trigger('click')
      await flushPromises()
      expect(app.ListMotherDuckDatabases).toHaveBeenCalledWith(expect.objectContaining({ signIn: 'env:MOTHERDUCK' }))
      expect(w.find('[data-testid="md-empty"]').exists()).toBe(true)
    })
  })

  describe('storage presets and detection (#130)', () => {
    it('the Store select fills the endpoint options, opens Advanced, and Other clears nothing', async () => {
      makeApp()
      const w = mountView()
      await flushPromises()
      await chooseType(w, 's3')
      expect(w.find('[data-testid="f-preset"]').exists()).toBe(true)
      const sel = w.find('[data-testid="f-preset"]')
      await sel.setValue('minio')
      expect((w.find('[data-testid="f-endpoint"]').element as HTMLInputElement).value).toBe('localhost:9000')
      expect((w.find('[data-testid="f-use_ssl"]').element as HTMLSelectElement).value).toBe('false')
      expect((w.find('details').element as HTMLDetailsElement).open).toBe(true)
      await sel.setValue('other')
      expect((w.find('[data-testid="f-endpoint"]').element as HTMLInputElement).value).toBe('localhost:9000')
      await sel.setValue('aws')
      expect((w.find('[data-testid="f-endpoint"]').element as HTMLInputElement).value).toBe('')
    })

    it('B2: the endpoint follows Region until it is edited; Amazon S3 takes back a region only the hint set', async () => {
      makeApp()
      const w = mountView()
      await flushPromises()
      await chooseType(w, 's3')
      const sel = w.find('[data-testid="f-preset"]')
      const ep = () => (w.find('[data-testid="f-endpoint"]').element as HTMLInputElement).value
      const region = () => (w.find('[data-testid="f-region"]').element as HTMLInputElement).value
      await sel.setValue('b2')
      expect(region()).toBe('us-west-004')
      expect(ep()).toBe('s3.us-west-004.backblazeb2.com')
      await w.find('[data-testid="f-region"]').setValue('eu-central-003')
      expect(ep()).toBe('s3.eu-central-003.backblazeb2.com')
      await sel.setValue('aws')
      expect(ep()).toBe('')
      expect(region()).toBe('eu-central-003') // the person's own region is kept

      await sel.setValue('b2')
      await w.find('[data-testid="f-endpoint"]').setValue('s3.custom.example')
      await w.find('[data-testid="f-region"]').setValue('us-west-002')
      expect(ep()).toBe('s3.custom.example') // an edited endpoint is left alone

      // A region only the hint set goes back to the default on Amazon S3.
      await w.find('[data-testid="f-region"]').setValue('us-east-1')
      await sel.setValue('b2')
      expect(region()).toBe('us-west-004')
      await sel.setValue('aws')
      expect(region()).toBe('us-east-1')
    })

    it('has no Store select for a type without presets', async () => {
      makeApp()
      const w = mountView()
      await flushPromises()
      await chooseType(w, 'postgres')
      expect(w.find('[data-testid="f-preset"]').exists()).toBe(false)
    })

    it('says so when the storage check fails', async () => {
      vi.useFakeTimers()
      try {
        makeApp({ FindStorageFor: vi.fn(async () => { throw new Error('boom') }) })
        const w = mountView()
        await flushPromises()
        await chooseType(w, 'ducklake')
        expect(w.find('.storage-slot[role="status"]').exists()).toBe(true) // mounted before any answer
        await w.find('[data-testid="f-path"]').setValue('s3://lake/c.sqlite')
        await vi.advanceTimersByTimeAsync(300)
        await flushPromises()
        expect(w.find('[data-testid="storage-failed"]').exists()).toBe(true)
      } finally {
        vi.useRealTimers()
      }
    })

    it('a lake shows what covers its URL, after the pause, and a problem without a button', async () => {
      vi.useFakeTimers()
      try {
        const find = vi.fn(async (req: FindStorageRequest) => ({
          needs: req.urls.map((u): StorageNeedView => (u.includes('..')
            ? { url: u, problem: 'not a plain storage URL' }
            : u.includes('floci') ? { url: u, match: { name: 'floci', type: 's3', scope: 's3://floci/', in: 'local', status: 'ok' } }
              : { url: u, suggest: { type: 's3', scope: 's3://lake/', name: 'lake_s3' }, group: 1 })),
        }))
        makeApp({ FindStorageFor: find })
        const w = mountView()
        await flushPromises()
        await chooseType(w, 'ducklake')
        await w.find('[data-testid="f-path"]').setValue('s3://lake/catalog.sqlite')
        expect(find).not.toHaveBeenCalled()
        await vi.advanceTimersByTimeAsync(300)
        await flushPromises()
        expect(find).toHaveBeenCalledWith({ urls: ['s3://lake/catalog.sqlite'] })
        expect(w.find('[data-testid="storage-path"]').text()).toContain('Needs a storage connection for s3://lake/')
        const setup = w.find('[data-testid="storage-setup"]')
        expect(setup.attributes('disabled')).toBeUndefined() // a real, focusable button
        expect(setup.attributes('title')).toBeUndefined()

        await w.find('[data-testid="f-path"]').setValue('s3://floci/c.sqlite')
        await vi.advanceTimersByTimeAsync(300)
        await flushPromises()
        expect(w.find('[data-testid="storage-path"]').text()).toContain('Read with floci')

        await w.find('[data-testid="f-path"]').setValue('s3://lake/../x')
        await vi.advanceTimersByTimeAsync(300)
        await flushPromises()
        expect(w.find('[data-testid="storage-problem"]').text()).toContain('not a plain storage URL')
        expect(w.find('[data-testid="storage-setup"]').exists()).toBe(false)
      } finally {
        vi.useRealTimers()
      }
    })
  })
})

// "Set up storage" from a lake form (#130 step 3).
describe('ConnectionEditorView: set up storage inline', () => {
  const suggest = { type: 's3', scope: 's3://lake/', bucket_scope: 's3://lake/', name: 'lake_s3' }
  const lakeDraft = (over: Partial<Parameters<typeof stashDraft>[0]> = {}) => stashDraft({
    def: { name: 'my_lake', type: 'ducklake', path: 's3://lake/catalog.sqlite', options: { data_path: 's3://lake/data/' } },
    connScope: 'local', advancedOpen: true, editName: '', editScope: '', focus: 'f-path', fingerprint: '', suggest: { ...suggest, scope: 's3://lake/wh/' }, ...over,
  })
  const storageQuery = () => ({ type: 's3', prefix: 's3://lake/wh/', name: 'lake_s3', in: 'local', return: 'lake-draft' })
  const val = (w: ReturnType<typeof mountView>, id: string) => (w.find(`[data-testid="${id}"]`).element as HTMLInputElement).value

  beforeEach(() => {
    _resetConnectionsForTests(); resetWorkspace(); clearDraft(); document.body.innerHTML = ''
    route.path = '/connections/new'; route.params = {}; route.query = {}
    router.push.mockClear(); router.replace.mockClear()
    workspaceInfo.value = { isGit: true, root: '/w', name: 'A', sessionId: 's' } as unknown as WorkspaceInfo
  })

  it('Set up storage stashes the lake form and routes to a prefilled storage form, without asking about unsaved edits', async () => {
    vi.useFakeTimers()
    try {
      makeApp({ FindStorageFor: vi.fn(async (req: FindStorageRequest) => ({ needs: req.urls.map((u): StorageNeedView => ({ url: u, suggest: { ...suggest, scope: 's3://lake/wh/' }, group: 1 })) })) })
      const w = mountView()
      await flushPromises()
      await chooseType(w, 'ducklake')
      await w.find('[data-testid="f-name"]').setValue('my_lake')
      await w.find('[data-testid="f-path"]').setValue('s3://lake/wh/catalog.sqlite')
      await w.find('[data-testid="f-data_path"]').setValue('s3://lake/wh/data/')
      await vi.advanceTimersByTimeAsync(300)
      await flushPromises()
      // path and data path share one suggestion: one line, one button
      expect(w.findAll('[data-testid="storage-setup"]')).toHaveLength(1)
      expect(w.find('[data-testid="storage-data_path"]').exists()).toBe(false)
      await w.find('[data-testid="storage-setup"]').trigger('click')
      expect(router.replace).toHaveBeenCalledWith({ path: '/connections/new', query: storageQuery() })
      expect(confirmVisible.value).toBe(false)
      expect(currentDraft.value?.def).toMatchObject({ name: 'my_lake', path: 's3://lake/wh/catalog.sqlite', options: { data_path: 's3://lake/wh/data/' } })
      expect(JSON.stringify(currentDraft.value)).not.toMatch(/secret|password/i)
    } finally {
      vi.useRealTimers()
    }
  })

  it('opens the storage form prefilled and visible, with the keychain sign-in and nothing saved', async () => {
    const app = makeApp()
    lakeDraft()
    route.query = storageQuery()
    const w = mountView()
    await flushPromises()
    expect(val(w, 'f-scope')).toBe('s3://lake/wh/')
    expect(val(w, 'f-name')).toBe('lake_s3')
    expect(w.find('[data-testid="storage-handoff"]').text()).toContain('my_lake')
    expect(w.find('[data-testid="method-keychain"]').attributes('aria-checked')).toBe('true')
    expect(app.SaveConnection).not.toHaveBeenCalled()
    expect(w.find('[data-testid="whole-bucket"]').exists()).toBe(true)
    await w.find('[data-testid="whole-bucket"]').trigger('click')
    expect(val(w, 'f-scope')).toBe('s3://lake/')
    expect(w.find('[data-testid="whole-bucket"]').exists()).toBe(false)
  })

  it.each([
    ['an unknown type', { type: 'postgres', prefix: 's3://lake/' }],
    ['a scheme of another type', { type: 's3', prefix: 'gs://lake/' }],
    ['a scope that is not own', { type: 's3', prefix: 's3://lake/', in: 'shared' }],
    ['a bad name', { type: 's3', prefix: 's3://lake/', name: 'Bad Name' }],
    ['a dot segment', { type: 's3', prefix: 's3://lake/../x' }],
    ['a prefix with no type', { prefix: 's3://lake/' }],
  ])('drops a crafted prefill whole: %s', async (_n, q) => {
    makeApp()
    route.query = q as Record<string, string>
    const w = mountView()
    await flushPromises()
    expect(w.find('[data-testid="f-name"]').exists()).toBe(false) // still the type picker
    expect(w.find('[data-testid="prefill-note"]').exists()).toBe(false)
  })

  it('a prefill from a plain link is shown with a note to check it, and ?scope= means nothing on the new route', async () => {
    makeApp()
    route.query = { type: 's3', prefix: 's3://lake/', scope: 'user' }
    const w = mountView()
    await flushPromises()
    expect(val(w, 'f-scope')).toBe('s3://lake/')
    expect(w.find('[data-testid="prefill-note"]').exists()).toBe(true)
    expect(w.find('[data-testid="storage-handoff"]').exists()).toBe(false) // no draft, no hand-off
  })

  it('Save returns to the lake form, which comes back with every field', async () => {
    const app = makeApp()
    lakeDraft()
    route.query = storageQuery()
    let w = mountView()
    await flushPromises()
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(app.SaveConnection).toHaveBeenCalledOnce()
    expect(app.SaveConnection.mock.calls[0][0].def).toMatchObject({ name: 'lake_s3', type: 's3', scope: 's3://lake/wh/' })
    expect(router.replace).toHaveBeenCalledWith({ path: '/connections/new', query: { resume: '1' } })

    document.body.innerHTML = ''
    route.query = { resume: '1' }
    w = mountView()
    await flushPromises()
    expect(val(w, 'f-name')).toBe('my_lake')
    expect(val(w, 'f-path')).toBe('s3://lake/catalog.sqlite')
    expect(val(w, 'f-data_path')).toBe('s3://lake/data/')
    expect(currentDraft.value).toBeNull() // taken
  })

  it('Cancel returns to the lake; typed values are asked about first', async () => {
    makeApp()
    lakeDraft()
    route.query = storageQuery()
    const w = mountView()
    await flushPromises()
    await w.find('[data-testid="handoff-back"]').trigger('click')
    await flushPromises()
    expect(router.replace).toHaveBeenCalledWith({ path: '/connections/new', query: { resume: '1' } })

    router.replace.mockClear()
    await w.find('[data-testid="f-c-secret"], #f-c-secret-in').setValue('typed-needle')
    await w.find('[data-testid="handoff-back"]').trigger('click')
    await flushPromises()
    expect(confirmVisible.value).toBe(true)
    expect(router.replace).not.toHaveBeenCalled()
    resolveConfirmChoice('cancel')
    await flushPromises()
    expect(router.replace).not.toHaveBeenCalled()
  })

  it('an edited lake resumes at its own route, not as a new connection', async () => {
    makeApp()
    lakeDraft({ editName: 'prod_pg', editScope: 'local' })
    route.query = storageQuery()
    const w = mountView()
    await flushPromises()
    await w.find('[data-testid="handoff-back"]').trigger('click')
    await flushPromises()
    expect(router.replace).toHaveBeenCalledWith({ path: '/connections/prod_pg', query: { resume: '1', scope: 'local' } })
  })

  it('a draft from another workspace is not restored, and the return is dropped', async () => {
    makeApp()
    lakeDraft()
    workspaceInfo.value = { isGit: true, root: '/other', name: 'B', sessionId: 's2' } as unknown as WorkspaceInfo
    route.query = { resume: '1' }
    const w = mountView()
    await flushPromises()
    expect(w.find('[data-testid="f-name"]').exists()).toBe(false)
    route.query = storageQuery()
    document.body.innerHTML = ''
    const w2 = mountView()
    await flushPromises()
    expect(w2.find('[data-testid="storage-handoff"]').exists()).toBe(false)
  })
})
