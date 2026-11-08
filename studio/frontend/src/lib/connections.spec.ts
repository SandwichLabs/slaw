import { describe, it, expect } from 'vitest'
import { fieldVisible, connectionChoices, detailLine, statusKey, statusLabel, oneLine, selectFrom, scopeLabel, typeCode, NAME_RE, STATUS_META } from './connections'

const base = { name: 'prod_pg', type: 'postgres', scope: 'local', readOnly: true, default: false, remote: true } as const

describe('connection display helpers (#117)', () => {
  it('maps the session statuses to six distinct shapes', () => {
    const keys = ['ok', 'not-connected', 'error', 'needs-credentials', 'extension-missing', 'locked-by'].map(statusKey)
    expect(new Set(keys).size).toBe(6)
    expect(new Set(keys.map((k) => STATUS_META[k].icon)).size).toBe(6)
  })

  it('says Team, This computer and All my workspaces, never the file-level words', () => {
    expect(scopeLabel('shared')).toBe('Team')
    expect(scopeLabel('local')).toBe('This computer')
    expect(scopeLabel('user')).toBe('All my workspaces')
  })

  it('shows a detail line only when the person can act (SPEC 05 copy)', () => {
    expect(detailLine({ ...base, status: 'ok' })).toBeNull()
    expect(detailLine({ ...base, status: 'not-connected' })).toBeNull()
    expect(detailLine({ ...base, status: 'error', detail: "failed to attach: password was rejected. extra" })!)
      .toMatchObject({ action: 'fix', actionLabel: 'Fix', text: "Can’t connect: failed to attach: password was rejected." })
    expect(detailLine({ ...base, type: 'motherduck', status: 'needs-credentials' })!)
      .toMatchObject({ text: 'Needs a MotherDuck token.', actionLabel: 'Add it' })
    expect(detailLine({ ...base, status: 'needs-credentials', serverChanged: { old: 'db.internal', new: 'db-new.internal' } })!)
      .toMatchObject({ text: 'The server changed since you signed in (db.internal to db-new.internal).', actionLabel: 'Sign in again', action: 'signin' })
    // An AWS SSO connection is not asked for access keys: it signs in (#131).
    expect(detailLine({ ...base, type: 's3', status: 'needs-credentials', signIn: 'aws-sso:dev', signInNeeded: { kind: 'aws-sso', subject: 'dev', reason: 'expired' } })!)
      .toMatchObject({ text: 'AWS session for profile dev has expired.', action: 'signin', actionLabel: 'Sign in' })
    expect(detailLine({ ...base, type: 's3', status: 'needs-credentials', signInNeeded: { kind: 'aws-sso', subject: 'dev', reason: 'missing' } })!)
      .toMatchObject({ text: 'Not signed in to AWS profile dev.', actionLabel: 'Sign in' })
    expect(detailLine({ ...base, type: 's3', status: 'needs-credentials', signIn: 'aws-sso:dev' })!.text).not.toContain('access keys')
    expect(detailLine({ ...base, type: 's3', status: 'needs-credentials' })!.text).toBe('Needs access keys.')
    expect(detailLine({ ...base, type: 's3', status: 'extension-missing', extension: 'httpfs' })!)
      .toMatchObject({ text: 'Needs the httpfs extension.', action: 'install' })
    expect(detailLine({ ...base, status: 'locked-by', lockedByPid: 4182 })!)
      .toMatchObject({ text: 'In use by another program (pid 4182).', action: 'takeover', actionLabel: 'Take over' })
  })

  it('keeps an error to one short line', () => {
    expect(oneLine('a'.repeat(300)).length).toBeLessThanOrEqual(90)
    expect(oneLine('')).toBe('something went wrong')
    expect(oneLine('first. second')).toBe('first')
  })

  it('names a status for screen readers', () => {
    expect(statusLabel({ ...base, status: 'needs-credentials' })).toBe('Needs credentials')
  })

  it('builds the starter query with every part quoted', () => {
    expect(selectFrom('prod_pg.public.orders')).toBe('SELECT * FROM "prod_pg"."public"."orders" LIMIT 100')
    expect(selectFrom('a.b."c d"')).toContain('"')
  })

  it('knows the type tiles and the name rule', () => {
    expect(typeCode('postgres')).toBe('PG')
    for (const [t, c] of [['mysql', 'MY'], ['azure', 'AZ'], ['ducklake', 'DL'], ['iceberg', 'IC'], ['delta', 'DT']]) expect(typeCode(t)).toBe(c)
    expect(NAME_RE.test('prod_pg')).toBe(true)
    expect(NAME_RE.test('Prod-PG')).toBe(false)
  })
})

describe('catalogInStorage', () => {
  it('is true only for a ducklake whose path is a storage URL', async () => {
    const { catalogInStorage } = await import('./connections')
    expect(catalogInStorage({ type: 'ducklake', path: 's3://b/c.sqlite' })).toBe(true)
    expect(catalogInStorage({ type: 'ducklake', path: 'data/lake.ducklake' })).toBe(false)
    expect(catalogInStorage({ type: 'iceberg', path: 's3://b/t/' })).toBe(false)
    expect(catalogInStorage(null)).toBe(false)
  })
})

describe('field visibility and connection pickers (iceberg_catalog form)', () => {
  const f = (o: Partial<ConnField>): ConnField => ({ name: 'x', label: 'X', kind: 'text', required: false, secret: false, perPerson: false, ...o })
  const fields = [f({ name: 'endpoint_type' }), f({ name: 'auth', default: 'oauth2' })]
  const bearer = f({ name: 'token', visibleWhen: [{ field: 'endpoint_type', in: ['rest'] }, { field: 'auth', in: ['bearer'] }] })
  const oauth = f({ name: 'secret', visibleWhen: [{ field: 'endpoint_type', in: ['rest'] }, { field: 'auth', in: ['oauth2'] }] })

  it('shows a field with no conditions always, and one with conditions only while all hold', () => {
    expect(fieldVisible(f({}), fields, {})).toBe(true)
    expect(fieldVisible(bearer, fields, { endpoint_type: 'rest', auth: 'bearer' })).toBe(true)
    expect(fieldVisible(bearer, fields, { endpoint_type: 's3_tables', auth: 'bearer' })).toBe(false)
    expect(fieldVisible(bearer, fields, { endpoint_type: 'rest', auth: 'none' })).toBe(false)
  })
  it("counts a blank value as the field's default", () => {
    expect(fieldVisible(oauth, fields, { endpoint_type: 'rest' })).toBe(true)
    expect(fieldVisible(oauth, fields, { endpoint_type: 'rest', auth: '  ' })).toBe(true)
    expect(fieldVisible(bearer, fields, { endpoint_type: 'rest' })).toBe(false)
    expect(fieldVisible(oauth, fields, {})).toBe(false) // no kind chosen yet
  })
  it('lists the names of connections of one type, sorted', () => {
    const list = [{ name: 'b_lake', type: 's3' }, { name: 'pg', type: 'postgres' }, { name: 'a_lake', type: 's3' }]
    expect(connectionChoices(list, 's3')).toEqual(['a_lake', 'b_lake'])
    expect(connectionChoices(list, undefined)).toEqual(['a_lake', 'b_lake', 'pg'])
    expect(connectionChoices([], 's3')).toEqual([])
  })
})

describe('a connection that waits on another one', () => {
  const cat = { name: 's3t', type: 'iceberg_catalog', scope: 'local', readOnly: true, default: false, remote: true, status: 'needs-credentials', blockedBy: 'aws_lake' } as const
  it('names the connection to sign in to, in the rail note and its action', () => {
    expect(detailLine({ ...cat })).toEqual({ text: 'Needs aws_lake: it needs credentials.', action: 'signin', actionLabel: 'Sign in' })
  })
  it('an error is still an error, and blockedBy alone changes nothing else', () => {
    expect(detailLine({ ...cat, status: 'error', detail: 'boom' })!.action).toBe('fix')
    expect(detailLine({ ...cat, blockedBy: undefined })!.text).toBe('Needs credentials.')
  })
})
