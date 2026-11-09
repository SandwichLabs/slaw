// Drill-down, the pure part (#162, D6): which parameter and result column a
// component's `drill` names, and how a clicked row's cell becomes a CANDIDATE
// for that parameter. The candidate is never a value: useDrill offers it to the
// artifact runtime, which commits it only if coerceParam accepts it (the same
// path as a control). Nothing here builds markup, a class or a style.

export interface DrillSpec { param: string; column: string }

/** The component's `drill`, or null when it has none or it is malformed (Go validates the shape). */
export function readDrill(c: { [key: string]: any }): DrillSpec | null {
  const d = c.drill
  if (!d || typeof d !== 'object') return null
  return typeof d.param === 'string' && typeof d.column === 'string' && d.param !== '' && d.column !== ''
    ? { param: d.param, column: d.column }
    : null
}

const MAX_NAMES = 20
const MAX_NAMES_TEXT = 300

/** Why a drill column cannot be used on this result, or '' when it can (or nothing has loaded yet). */
export function drillColumnProblem(spec: DrillSpec, columns: string[] | null | undefined): string {
  if (!columns || columns.length === 0) return ''
  if (columns.includes(spec.column)) return ''
  const list = columns.slice(0, MAX_NAMES).join(', ').slice(0, MAX_NAMES_TEXT)
  return `Drill column "${spec.column.slice(0, 80)}" is not in this query's result. Columns: ${list}.`
}

export type DrillCandidate = { ok: true; raw: unknown } | { ok: false; error: string }

const TIMESTAMP = /^(\d{4}-\d{2}-\d{2})[ T]/

/**
 * A result cell as the raw candidate for a parameter of the given type. Only
 * lossless readings are made (a number for a text or enum parameter becomes its
 * decimal text; a timestamp for a date parameter its date part); anything else
 * is offered as it is and refused by coerceParam. NULL and nested values are
 * refused here: a row with no value cannot set a parameter.
 */
export function drillCandidate(p: ArtifactParamT, cell: unknown, column: string): DrillCandidate {
  if (cell === null || cell === undefined) return { ok: false, error: `That row has no value for "${column.slice(0, 80)}".` }
  if (typeof cell === 'object') return { ok: false, error: `The value of "${column.slice(0, 80)}" can't be used here.` }
  if ((p.type === 'text' || p.type === 'enum') && (typeof cell === 'number' || typeof cell === 'bigint')) return { ok: true, raw: String(cell) }
  if (p.type === 'date' && typeof cell === 'string') {
    const m = TIMESTAMP.exec(cell)
    if (m) return { ok: true, raw: m[1] }
  }
  return { ok: true, raw: cell }
}
