// railTree — the Connections rail's tree as pure functions (F10): the flat list
// of treeitem rows built from the lazily loaded levels (connection > schema >
// table or view > column), and the small formatters its rows use. The rail's
// composables own the state; nothing here touches Vue or a binding.
import { detailLine, type DetailLine } from './connections'

export type NodeKey = string // c:<conn> | s:<conn>/<schema> | t:<conn>/<schema>/<table>

// A level is loaded on demand: its listing, a pending marker, or the failure.
type Level = CatalogListing | { loading: true } | { error: string }
export type Levels = Record<string, Level>

export interface Row {
  key: NodeKey
  kind: 'conn' | 'schema' | 'table' | 'view' | 'column' | 'note'
  level: number
  label: string
  conn: ConnectionInfo
  schema?: string
  table?: string
  node?: CatalogNode
  expandable: boolean
  isOpen: boolean
  parent?: NodeKey
  detail?: DetailLine | null
  noteText?: string
  setsize: number
  pos: number
}

export const connKey = (n: string) => `c:${n}`
export const schemaKey = (n: string, s: string) => `s:${n}/${s}`
export const tableKey = (n: string, s: string, t: string) => `t:${n}/${s}/${t}`

function listing(levels: Levels, key: string): CatalogListing | null {
  const v = levels[key]
  return v && 'nodes' in v ? v : null
}

function note(parent: string, c: ConnectionInfo, level: number, text: string): Row {
  return { key: `n:${parent}`, kind: 'note', level, label: text, conn: c, expandable: false, isOpen: false, parent, noteText: text, setsize: 1, pos: 1 }
}

function pushTables(out: Row[], c: ConnectionInfo, l: CatalogListing, level: number, parent: string, schema: string, expanded: Set<string>, levels: Levels, f: string) {
  const nodes = l.nodes.filter((n) => {
    if (!f) return true
    if (n.name.toLowerCase().includes(f)) return true
    const cols = listing(levels, tableKey(c.name, schema, n.name))
    return !!cols && cols.nodes.some((x) => x.name.toLowerCase().includes(f))
  })
  if (l.nodes.length === 0) {
    out.push(note(parent, c, level, 'No tables yet. Import a CSV, or ask the Slaw assistant to build one.'))
    return
  }
  nodes.forEach((n, i) => {
    const tk = tableKey(c.name, schema, n.name)
    const isOpen = expanded.has(tk)
    out.push({ key: tk, kind: n.kind === 'view' ? 'view' : 'table', level, label: n.name, conn: c, schema, table: n.name, node: n, expandable: true, isOpen, parent, setsize: nodes.length, pos: i + 1 })
    if (!isOpen) return
    const cl = levels[tk]
    if (!cl) return
    if ('loading' in cl) { out.push(note(tk, c, level + 1, 'Loading…')); return }
    if ('error' in cl) { out.push(note(tk, c, level + 1, cl.error)); return }
    const cols = f && !n.name.toLowerCase().includes(f) ? cl.nodes.filter((x) => x.name.toLowerCase().includes(f)) : cl.nodes
    cols.forEach((col, k) => {
      out.push({ key: `col:${tk}/${col.name}`, kind: 'column', level: level + 1, label: col.name, conn: c, schema, table: n.name, node: col, expandable: false, isOpen: false, parent: tk, setsize: cols.length, pos: k + 1 })
    })
  })
}

// buildRows flattens the open part of the tree. `query` is the lower-cased filter.
export function buildRows(conns: ConnectionInfo[], expanded: Set<string>, levels: Levels, query: string): Row[] {
  const out: Row[] = []
  conns.forEach((c, ci) => {
    const ck = connKey(c.name)
    const isOpen = expanded.has(ck)
    const detail = detailLine(c)
    out.push({ key: ck, kind: 'conn', level: 1, label: c.name, conn: c, expandable: !c.catalogless || c.status === 'ok', isOpen, detail, setsize: conns.length, pos: ci + 1 })
    if (!isOpen) return
    const top = levels[ck]
    if (!top) return
    if ('loading' in top) { out.push(note(ck, c, 2, 'Loading…')); return }
    if ('error' in top) { out.push(note(ck, c, 2, top.error)); return }
    if (c.catalogless) {
      const msg = c.type === 'iceberg' ? 'Iceberg table: read it with iceberg_scan(’location’). It has no tables to browse.'
        : c.type === 'delta' ? 'Delta Lake table: read it with delta_scan(’location’). It has no tables to browse.'
        : 'Storage connection: read files with read_parquet(’scope/…’). It has no tables.'
      out.push(note(ck, c, 2, msg))
      return
    }
    if (top.kind === 'tables') {
      pushTables(out, c, top, 2, ck, top.schema, expanded, levels, query)
      return
    }
    const nsMore = top.more ?? 0
    top.nodes.forEach((s, si) => {
      const sk = schemaKey(c.name, s.name)
      const sOpen = expanded.has(sk)
      out.push({ key: sk, kind: 'schema', level: 2, label: s.name, conn: c, schema: s.name, node: s, expandable: true, isOpen: sOpen, parent: ck, setsize: top.nodes.length, pos: si + 1 })
      if (!sOpen) return
      const sl = levels[sk]
      if (!sl) return
      if ('loading' in sl) { out.push(note(sk, c, 3, 'Loading…')); return }
      if ('error' in sl) { out.push(note(sk, c, 3, sl.error)); return }
      pushTables(out, c, sl, 3, sk, s.name, expanded, levels, query)
    })
    // A catalog with more namespaces than the rail lists says so (engine.MaxCatalogNamespaces).
    if (nsMore > 0) out.push(note(`${ck}+more`, c, 2, `${nsMore} more namespace${nsMore === 1 ? '' : 's'} not listed. Query ${c.name}.<namespace>.<table> by name.`))
  })
  return out
}

export function domId(k: string) { return 'rail-' + k.replace(/[^a-zA-Z0-9_-]/g, '_') }
export function rowsText(n: number) { return n < 0 ? '' : `~${n >= 1e6 ? (n / 1e6).toFixed(1) + 'M' : n >= 1e3 ? Math.round(n / 1e3) + 'K' : n}` }
export function colMeta(r: Row) {
  const n = r.node!
  return { type: n.dataType ?? '', tag: n.pk ? 'PK' : n.fk ? 'FK' : '' }
}
