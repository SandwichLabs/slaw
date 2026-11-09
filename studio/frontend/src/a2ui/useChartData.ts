// useChartData — a KINETIC chart's data: its live "sql" result when bound,
// else its inline columns + rows (the same shape). streamChart's "refresh"
// re-runs the query every N seconds while the chart is mounted and the window
// is visible, keeping the last `window` rows.
import { computed, inject, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { ARTIFACT_RUNTIME } from './artifactRuntime'
import { useBoundQuery } from './useBoundQuery'
import type { A2UIComponentT } from './types'
import { runLiveQuery, isBound, type LiveQueryResult } from './query'
import { useRenderSource } from './source'
import type { TableData } from './series'

export function useChartData(component: () => A2UIComponentT) {
  const abortSignal = inject<AbortSignal | undefined>('a2uiAbortSignal', undefined)
  const sourceOf = useRenderSource()
  // Inside a data artifact view the rows come from its named queries (#162).
  const artifact = inject(ARTIFACT_RUNTIME, undefined)
  const bound = artifact ? useBoundQuery(artifact, component) : null
  const loading = ref(false)
  const error = ref('')
  const live = shallowRef<LiveQueryResult | null>(null)
  let timer: ReturnType<typeof setInterval> | undefined
  let unmounted = false
  let inFlight = false

  async function fetchOnce(first: boolean) {
    const c = component()
    if (!c.sql || inFlight) return
    inFlight = true
    if (first) loading.value = true
    const res = await runLiveQuery(c.sql, abortSignal, 'low', c.connection || undefined, sourceOf(c.id))
    inFlight = false
    if (res.aborted || unmounted) return
    loading.value = false
    error.value = res.error ?? ''
    if (!res.error) live.value = res
  }

  onMounted(() => {
    if (bound) return
    void fetchOnce(true)
    const refresh = Number(component().refresh)
    if (component().sql && Number.isFinite(refresh) && refresh >= 5) {
      timer = setInterval(() => {
        if (typeof document !== 'undefined' && document.hidden) return
        void fetchOnce(false)
      }, Math.min(refresh, 3600) * 1000)
    }
  })
  onBeforeUnmount(() => {
    unmounted = true
    if (timer) clearInterval(timer)
  })

  const table = computed<TableData>(() => {
    const c = component()
    const src = isBound(c) ? (bound ? bound.data.value : live.value) : { columns: c.columns ?? [], rows: c.rows ?? [] }
    if (!src) return { columns: [], rows: [] }
    const win = Number(c.window ?? (c.type === 'streamChart' ? 30 : Number.NaN))
    const rows = Number.isFinite(win) && win > 0 ? src.rows.slice(-Math.min(win, 120)) : src.rows
    return { columns: src.columns ?? [], rows }
  })

  // the run behind the chart, for its Evidence button (receipt id or note)
  const result = computed<LiveQueryResult | null>(() => (bound ? bound.data.value : live.value))
  return bound ? { loading: bound.loading, error: bound.error, table, result } : { loading, error, table, result }
}
