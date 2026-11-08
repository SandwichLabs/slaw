// railTree — the Connections rail's rows (F10): the flat treeitem list built from
// the lazily loaded levels.
import { describe, it, expect } from 'vitest'
import { buildRows, connKey, schemaKey, tableKey, domId, rowsText, type Levels } from './railTree'

const conn = (name: string, over: Partial<ConnectionInfo> = {}): ConnectionInfo => ({
  name, type: 'duckdb', scope: 'local', readOnly: false, default: false, status: 'ok', remote: false, ...over,
} as ConnectionInfo)

const tables = (names: string[], schema = 'main'): CatalogListing => ({
  kind: 'tables', schema, nodes: names.map((n) => ({ name: n, kind: 'table', qualified: `local.${schema}.${n}` })),
} as unknown as CatalogListing)

describe('buildRows', () => {
  it('lists only the connections while nothing is expanded', () => {
    const rows = buildRows([conn('a'), conn('b')], new Set(), {}, '')
    expect(rows.map((r) => [r.key, r.kind, r.level, r.pos, r.setsize])).toEqual([
      ['c:a', 'conn', 1, 1, 2],
      ['c:b', 'conn', 1, 2, 2],
    ])
  })

  it('shows a Loading… / error note under an open connection whose level is not loaded', () => {
    const open = new Set([connKey('a')])
    const loading = buildRows([conn('a')], open, { [connKey('a')]: { loading: true } }, '')
    expect(loading.map((r) => r.kind)).toEqual(['conn', 'note'])
    expect(loading[1].noteText).toBe('Loading…')
    const failed = buildRows([conn('a')], open, { [connKey('a')]: { error: 'boom' } }, '')
    expect(failed[1].noteText).toBe('boom')
  })

  it('flattens connection > tables > columns, with the right levels and parents', () => {
    const levels: Levels = {
      [connKey('a')]: tables(['orders']),
      [tableKey('a', 'main', 'orders')]: { kind: 'columns', nodes: [{ name: 'id', pk: true }, { name: 'total' }] } as unknown as CatalogListing,
    }
    const open = new Set([connKey('a'), tableKey('a', 'main', 'orders')])
    const rows = buildRows([conn('a')], open, levels, '')
    expect(rows.map((r) => [r.kind, r.level, r.label, r.parent])).toEqual([
      ['conn', 1, 'a', undefined],
      ['table', 2, 'orders', 'c:a'],
      ['column', 3, 'id', 't:a/main/orders'],
      ['column', 3, 'total', 't:a/main/orders'],
    ])
  })

  it('nests tables under a schema when the connection lists schemas', () => {
    const levels: Levels = {
      [connKey('pg')]: { kind: 'schemas', nodes: [{ name: 'public' }, { name: 'audit' }] } as unknown as CatalogListing,
      [schemaKey('pg', 'public')]: tables(['users'], 'public'),
    }
    const rows = buildRows([conn('pg', { type: 'postgres', remote: true })], new Set([connKey('pg'), schemaKey('pg', 'public')]), levels, '')
    expect(rows.map((r) => [r.kind, r.level, r.label])).toEqual([
      ['conn', 1, 'pg'], ['schema', 2, 'public'], ['table', 3, 'users'], ['schema', 2, 'audit'],
    ])
  })

  it('filters tables by name, and keeps a table whose loaded column matches', () => {
    const levels: Levels = {
      [connKey('a')]: tables(['orders', 'customers', 'items']),
      [tableKey('a', 'main', 'items')]: { kind: 'columns', nodes: [{ name: 'sku' }] } as unknown as CatalogListing,
    }
    const open = new Set([connKey('a')])
    expect(buildRows([conn('a')], open, levels, 'cust').map((r) => r.label)).toEqual(['a', 'customers'])
    expect(buildRows([conn('a')], open, levels, 'sku').map((r) => r.label)).toEqual(['a', 'items'])
  })

  it('says so when a connection has no tables, and explains a storage connection', () => {
    const empty = buildRows([conn('a')], new Set([connKey('a')]), { [connKey('a')]: tables([]) }, '')
    expect(empty[1].noteText).toMatch(/No tables yet/)
    const s3 = conn('lake', { type: 's3', catalogless: true } as Partial<ConnectionInfo>)
    const rows = buildRows([s3], new Set([connKey('lake')]), { [connKey('lake')]: tables([]) }, '')
    expect(rows[1].noteText).toMatch(/Storage connection/)
  })
})

describe('formatters', () => {
  it('rowsText abbreviates counts and hides an unknown one', () => {
    expect([-1, 0, 999, 1500, 2_400_000].map(rowsText)).toEqual(['', '~0', '~999', '~2K', '~2.4M'])
  })
  it('domId makes a key safe to use as an element id', () => {
    expect(domId('t:a/main/my table')).toBe('rail-t_a_main_my_table')
  })
})

describe('a catalog with more namespaces than the rail lists', () => {
  it('ends the schema level with a "more" note naming the count', () => {
    const top = {
      kind: 'schemas', schema: '', flattened: false, more: 3,
      nodes: [{ kind: 'schema', name: 'ns1', rows: -1 }, { kind: 'schema', name: 'ns2', rows: -1 }],
    } as unknown as CatalogListing
    const c = conn('cat', { type: 'iceberg_catalog', remote: true })
    const rows = buildRows([c], new Set([connKey('cat')]), { [connKey('cat')]: top }, '')
    expect(rows.map((r) => r.kind)).toEqual(['conn', 'schema', 'schema', 'note'])
    expect(rows[3].noteText).toContain('3 more namespaces not listed')
    expect(rows[3].key).not.toBe('n:c:cat')
    const none = buildRows([c], new Set([connKey('cat')]), { [connKey('cat')]: { ...top, more: 0 } }, '')
    expect(none.map((r) => r.kind)).toEqual(['conn', 'schema', 'schema'])
  })
})
