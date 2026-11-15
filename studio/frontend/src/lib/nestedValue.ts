// nestedValue — display text for a LIST / ARRAY / STRUCT / MAP / UNION value,
// in the style of DuckDB's own CLI (and of internal/engine/nested.go's
// NestedText, which the console prints with):
//
//   [1, 2, NULL]            LIST and ARRAY
//   {'a': 1, 'b': x}        STRUCT, fields in their declared order
//   {k=1, j=2}              MAP
//   [{'foo': bar}]          nesting of any depth; strings are not quoted
//
// It formats element by element instead of JSON.stringify so an exact value
// stays exact: a DECIMAL(10,2) 12.5 reads 12.50 (the wire number lost its
// zero; the declared scale says it), a BIGINT beyond 2^53 is the exact string
// the wire carried, and none of the text is quoted or escaped as JSON.
//
// The column's type name (DatabaseTypeName, e.g. `STRUCT("a" DECIMAL(10,2)[])`)
// gives the field order and the leaf types; without one (model-authored inline
// A2UI data) the value's own shape decides and an object reads as a STRUCT.

type NestedKind = 'scalar' | 'list' | 'struct' | 'map' | 'union'

interface TypeNode {
  kind: NestedKind
  base: string // scalar: the upper-case type name
  elem?: TypeNode // list element, map value
  key?: TypeNode // map key
  fields: { name: string; type: TypeNode }[] // struct, union
}

const scalar = (base: string): TypeNode => ({ kind: 'scalar', base, fields: [] })

// splitTop splits on commas outside parentheses, brackets and double quotes.
function splitTop(s: string): string[] {
  const parts: string[] = []
  let depth = 0
  let start = 0
  let inQuote = false
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (c === '"') inQuote = !inQuote // a doubled "" toggles twice
    else if (inQuote) continue
    else if (c === '(' || c === '[') depth++
    else if (c === ')' || c === ']') depth--
    else if (c === ',' && depth === 0) {
      parts.push(s.slice(start, i).trim())
      start = i + 1
    }
  }
  const rest = s.slice(start).trim()
  if (rest) parts.push(rest)
  return parts
}

// splitFieldName splits `"a b" INTEGER` or `a INTEGER` into the unquoted name
// and the type text.
function splitFieldName(part: string): [string, string] {
  if (part.startsWith('"')) {
    let name = ''
    for (let i = 1; i < part.length; i++) {
      if (part[i] === '"') {
        if (part[i + 1] === '"') {
          name += '"'
          i++
          continue
        }
        return [name, part.slice(i + 1).trim()]
      }
      name += part[i]
    }
    return [name, '']
  }
  const sp = part.indexOf(' ')
  return sp > 0 ? [part.slice(0, sp), part.slice(sp + 1).trim()] : [part, '']
}

function call(s: string, u: string, name: string): string | null {
  return u.startsWith(name + '(') && s.endsWith(')') ? s.slice(name.length + 1, -1) : null
}

const cache = new Map<string, TypeNode>()

// parseType never throws: what it does not understand is a scalar.
function parseType(text: string): TypeNode {
  const s = text.trim()
  const hit = cache.get(s)
  if (hit) return hit
  const node = parseUncached(s)
  if (cache.size < 512) cache.set(s, node)
  return node
}

function parseUncached(s: string): TypeNode {
  if (s.endsWith(']')) {
    const i = s.lastIndexOf('[')
    if (i > 0) return { kind: 'list', base: '', elem: parseType(s.slice(0, i)), fields: [] }
  }
  const u = s.toUpperCase()
  for (const [name, kind] of [['STRUCT', 'struct'], ['UNION', 'union']] as const) {
    const inner = call(s, u, name)
    if (inner !== null) {
      const fields = splitTop(inner).map((p) => {
        const [n, t] = splitFieldName(p)
        return { name: n, type: parseType(t) }
      })
      return { kind, base: '', fields }
    }
  }
  const inner = call(s, u, 'MAP')
  if (inner !== null) {
    const parts = splitTop(inner)
    if (parts.length === 2) return { kind: 'map', base: '', key: parseType(parts[0]), elem: parseType(parts[1]), fields: [] }
  }
  return scalar(u)
}

// decimalScaleOf is s of a DECIMAL(w,s) leaf type, or 0.
function decimalScaleOf(base: string): number {
  const m = /^(?:DECIMAL|NUMERIC)\((\d+)\s*,\s*(\d+)\)$/.exec(base)
  return m ? Number(m[2]) : 0
}

// numberText prints a number the way DuckDB prints a DOUBLE (and the way
// internal/engine/nested.go's leafFloatText does): shortest digits, an exponent
// (1e-07, 1.5e+16) below 1e-4 and from 1e16, ".0" on a whole DOUBLE/FLOAT/REAL,
// and a DECIMAL(w,s) at its declared scale (12.50).
function numberText(v: number, t: TypeNode | undefined): string {
  const base = t?.kind === 'scalar' ? t.base : ''
  const scale = decimalScaleOf(base)
  if (scale > 0) return v.toFixed(scale)
  if (!Number.isFinite(v)) return String(v)
  const abs = Math.abs(v)
  if (v !== 0 && (abs < 1e-4 || abs >= 1e16)) {
    return v.toExponential().replace(/e([+-])(\d)$/, (_m, sign, d) => `e${sign}0${d}`)
  }
  const s = String(v)
  return !s.includes('.') && (base === 'DOUBLE' || base === 'FLOAT' || base === 'REAL') ? s + '.0' : s
}

function leafText(v: unknown, t: TypeNode | undefined): string {
  return typeof v === 'number' ? numberText(v, t) : String(v)
}

// sortedKeys orders object keys as the Go side does (plain string order). The
// default Object.keys order puts integer-like keys first, which would not match.
function sortedKeys(v: Record<string, unknown>): string[] {
  return Object.keys(v).sort()
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function textOf(v: unknown, t: TypeNode | undefined): string {
  if (v === null || v === undefined) return 'NULL'
  if (Array.isArray(v)) {
    const et = t?.kind === 'list' ? t.elem : undefined
    return '[' + v.map((e) => textOf(e, et)).join(', ') + ']'
  }
  if (isObject(v)) {
    if (t?.kind === 'map') {
      return '{' + sortedKeys(v).map((k) => `${k}=${textOf(v[k], t.elem)}`).join(', ') + '}'
    }
    return structText(v, t?.kind === 'struct' ? t : undefined)
  }
  if (t?.kind === 'union') return leafText(v, t.fields.length === 1 ? t.fields[0].type : undefined)
  return leafText(v, t)
}

// structText prints a STRUCT's fields in the order the type declares them (the
// wire object is keyed alphabetically), then any key the type does not name.
function structText(v: Record<string, unknown>, t: TypeNode | undefined): string {
  const keys = sortedKeys(v)
  const declared = t ? t.fields.map((f) => f.name).filter((n) => n in v) : []
  const order = declared.length === keys.length ? declared : keys
  const parts = order.map((k) => {
    const ft = t?.fields.find((f) => f.name === k)?.type
    return `'${k}': ${textOf(v[k], ft)}`
  })
  return '{' + parts.join(', ') + '}'
}

// nestedText is the display text of one nested value. dbType may be empty.
export function nestedText(v: unknown, dbType?: string): string {
  return textOf(v, dbType ? parseType(dbType) : undefined)
}

// jsonText is a decoded JSON document as compact JSON with every object's keys
// in text order, the way the console prints it (JSON.stringify would list
// integer-like keys first).
export function jsonText(v: unknown): string {
  if (Array.isArray(v)) return '[' + v.map(jsonText).join(',') + ']'
  if (v !== null && typeof v === 'object') {
    const o = v as Record<string, unknown>
    return '{' + sortedKeys(o).map((k) => JSON.stringify(k) + ':' + jsonText(o[k])).join(',') + '}'
  }
  return JSON.stringify(v)
}
