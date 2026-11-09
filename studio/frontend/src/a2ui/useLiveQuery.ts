// useLiveQuery — the live "sql" binding of an A2UI component (F5), shared by
// stat, table, barChart and lineChart. A data-bound component's sql re-runs every
// time it mounts, so what it shows is whatever the data is now, not what the
// agent saw when it wrote the component. The query goes through the shared
// concurrency pool at 'low' priority (a user's Run never waits behind it) and
// honours the host's abort signal: a result that resolves after the page was
// unmounted or reloaded is dropped, never applied to state that moved on.
import { ref, shallowRef, onMounted, inject } from 'vue'
import { ARTIFACT_RUNTIME } from './artifactRuntime'
import { useBoundQuery } from './useBoundQuery'
import type { A2UIComponentT } from './types'
import { runLiveQuery, type LiveQueryResult } from './query'
import { useRenderSource } from './source'

export function useLiveQuery(component: () => A2UIComponentT) {
  // Inside a data artifact view the rows come from its named queries (#162).
  const artifact = inject(ARTIFACT_RUNTIME, undefined)
  if (artifact) return useBoundQuery(artifact, component)
  const abortSignal = inject<AbortSignal | undefined>('a2uiAbortSignal', undefined)
  const sourceOf = useRenderSource()

  const loading = ref(false)
  const error = ref('')
  // true once the query has come back (even with an error)
  const fetched = ref(false)
  // the query's result; null until it succeeds (and for a failed query)
  const data = shallowRef<LiveQueryResult | null>(null)

  onMounted(async () => {
    const c = component()
    if (!c.sql) return
    loading.value = true
    const res = await runLiveQuery(c.sql, abortSignal, 'low', c.connection || undefined, sourceOf(c.id))
    if (res.aborted) return // caller stopped caring (unmounted/reloaded) — never apply a stale result
    loading.value = false
    fetched.value = true
    if (res.error) {
      error.value = res.error
      return
    }
    data.value = res
  })

  return { loading, error, fetched, data }
}
