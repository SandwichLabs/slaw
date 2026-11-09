// useDrill — drill-down for table, barChart, columnChart and pivot (#162, D6).
// A click (or Enter/Space) on a row sets the drill parameter to that row's
// `column` value through the artifact runtime's own `set`, so the value is
// coerced and validated exactly as a control's is: a value the parameter
// refuses leaves the control's inline error, commits nothing and runs nothing.
// Outside a data artifact (notebooks, chat) there is no runtime and drill is off.
import { computed, inject, ref, watch } from 'vue'
import { ARTIFACT_RUNTIME } from './artifactRuntime'
import { drillCandidate, drillColumnProblem, readDrill } from './drill'
import { isBound } from './query'
import type { A2UIComponentT } from './types'

interface DrillSource { columns: string[]; rows: any[][] }

const clip = (s: string) => (s.length > 80 ? s.slice(0, 80) + '…' : s)

export function useDrill(component: () => A2UIComponentT, source: () => DrillSource | null) {
  const rt = inject(ARTIFACT_RUNTIME, undefined)
  const spec = computed(() => (rt && isBound(component()) ? readDrill(component()) : null))
  const problem = computed(() => (spec.value ? drillColumnProblem(spec.value, source()?.columns) : ''))
  /** Rows can be drilled: a spec, a loaded result, and its column is in it. */
  const enabled = computed(() => !!spec.value && !problem.value && !!source()?.columns.includes(spec.value.column))

  // A row that cannot be read as a value (NULL, nested): cleared when the parameter changes.
  const local = ref('')
  // The raw value the parameter last refused; the note shows only while that draft is still there.
  const refused = ref<string | null>(null)

  const columnIndex = () => (spec.value ? (source()?.columns.indexOf(spec.value.column) ?? -1) : -1)
  const paramDecl = () => (spec.value ? rt?.params.param(spec.value.param) : undefined)
  const paramLabel = () => paramDecl()?.label || spec.value?.param || ''

  watch(
    () => (spec.value && rt ? [rt.params.display(spec.value.param), rt.params.isDefault(spec.value.param)] : null),
    () => { local.value = '' },
    { deep: true },
  )

  function drill(row: number) {
    if (!enabled.value) return
    local.value = ''
    refused.value = null
    const s = spec.value!
    const idx = columnIndex()
    const p = paramDecl()
    if (!rt || !p || idx < 0) { local.value = "This row can't set a parameter."; return }
    const cand = drillCandidate(p, source()?.rows[row]?.[idx], s.column)
    if (!cand.ok) { local.value = cand.error; return }
    const res = rt.params.set(s.param, cand.raw)
    if (res && !res.ok) refused.value = String(cand.raw)
  }

  /** The note under the component: a drill-only refusal, or the parameter's own, naming the value. */
  const error = computed(() => {
    if (local.value) return local.value
    const s = spec.value
    if (!s || !rt || refused.value === null) return ''
    const e = rt.params.error(s.param)
    if (!e || String(rt.params.display(s.param)) !== refused.value) return ''
    return `“${clip(refused.value)}” was refused for ${paramLabel()}: ${e}`
  })

  // Per source row: is its value the parameter's current value? Nothing is marked
  // while the parameter shows an error, or when every row matches (no information).
  const current = computed<boolean[]>(() => {
    const s = spec.value
    const p = paramDecl()
    const idx = columnIndex()
    const rows = source()?.rows ?? []
    if (!s || !rt || !p || idx < 0) return []
    const shown = String(rt.params.display(s.param))
    const marks = rows.map((r) => {
      const c = drillCandidate(p, r[idx], s.column)
      return c.ok && String(c.raw) === shown
    })
    return rt.params.error(s.param) || marks.every(Boolean) ? marks.map(() => false) : marks
  })
  const isCurrent = (row: number) => current.value[row] ?? false

  /** What a screen reader hears for the row's action, naming the value it sets. */
  function action(row: number): string {
    const idx = columnIndex()
    const cell = idx < 0 ? undefined : source()?.rows[row]?.[idx]
    const text = cell === null || cell === undefined ? '(blank)' : typeof cell === 'object' ? '' : clip(String(cell))
    return `Filter ${paramLabel()} by ${text}`
  }

  /** Enter or Space on a focusable non-button row or bar. */
  function onKey(e: KeyboardEvent, row: number) {
    if (e.key !== 'Enter' && e.key !== ' ') return
    e.preventDefault()
    drill(row)
  }

  return { enabled, problem, error, drill, isCurrent, action, onKey, column: computed(() => spec.value?.column ?? '') }
}
