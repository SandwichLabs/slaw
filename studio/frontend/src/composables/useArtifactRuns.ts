/**
 * useArtifactRuns — the runs behind an open data artifact (#162). One run per
 * (query, the values that query uses), shared by every component bound to it
 * through the artifact runtime; a changed parameter re-runs only the queries
 * that use it.
 *
 * Every run goes through the one live-query pool (a2ui/pool.ts) in the 'low'
 * lane, so a person's own Run (the 'high' lane) is never queued behind a burst
 * of parameter changes. Per query there is at most ONE Go call in flight and
 * at most ONE run waiting: an aborted call still holds its slot until Go
 * returns (the call cannot be cancelled), so a newer value aborts the older
 * run, replaces any run still waiting, and starts only when the call in flight
 * has finished. A result that arrives for a value that has since changed, or
 * after dispose, is dropped.
 */
import { shallowReactive, shallowRef, watch, computed } from 'vue'
import { App } from '../lib/bindings'
import { errorText } from '../lib/errors'
import { runPooled } from '../a2ui/pool'
import type { LiveQueryResult } from '../a2ui/query'
import type { ParamValue } from '../a2ui/artifactParams'
import type { ArtifactQueryState } from '../a2ui/artifactRuntime'

export interface ArtifactRunsOptions {
  name: () => string
  queries: () => { name: string; uses: string[] }[]
  /** The committed, validated values (name -> typed value). */
  values: () => Record<string, ParamValue>
  /** Changes when the artifact was reloaded (new revision): every shown query re-runs. */
  rev?: () => string
  /** False once the workspace this view was opened in is gone. */
  sessionOk?: () => boolean
  /** The binding; replaceable in tests. */
  run?: (name: string, query: string, values: Record<string, unknown>) => Promise<A2UIQueryResult>
}

interface Slot {
  gen: number
  inFlight: boolean
  next: { gen: number; values: Record<string, ParamValue> } | null
  ctrl: AbortController | null
  key: string
}

export function useArtifactRuns(o: ArtifactRunsOptions) {
  const states = new Map<string, ArtifactQueryState>()
  const slots = new Map<string, Slot>()
  const demanded = new Set<string>()
  // The same set, as a reactive list for the computed below.
  const shown = shallowRef<string[]>([])
  let disposed = false
  const call = o.run ?? ((n, q, v) => App.RunArtifactQuery(n, q, v))

  function result(query: string): ArtifactQueryState {
    let st = states.get(query)
    if (!st) {
      st = shallowReactive<ArtifactQueryState>({ loading: false, error: '', data: null, fetched: false, truncated: false })
      states.set(query, st)
    }
    return st
  }

  function slot(query: string): Slot {
    let s = slots.get(query)
    if (!s) {
      s = { gen: 0, inFlight: false, next: null, ctrl: null, key: '' }
      slots.set(query, s)
    }
    return s
  }

  const uses = (query: string) => o.queries().find((q) => q.name === query)?.uses

  function pick(query: string): Record<string, ParamValue> {
    const all = o.values()
    const out: Record<string, ParamValue> = {}
    for (const n of uses(query) ?? []) if (n in all) out[n] = all[n]
    return out
  }

  const keyFor = (query: string) => JSON.stringify(pick(query))

  async function start(query: string, req: { gen: number; values: Record<string, ParamValue> }) {
    const s = slot(query)
    const st = result(query)
    s.inFlight = true
    const ctrl = (s.ctrl = new AbortController())
    let res: A2UIQueryResult | undefined
    let err = ''
    let aborted = false
    try {
      const out = await runPooled(() => call(o.name(), query, req.values), ctrl.signal, 'low')
      if (out.aborted) aborted = true
      else res = out.value
    } catch (e) {
      err = errorText(e)
    }
    s.inFlight = false
    if (disposed || (o.sessionOk && !o.sessionOk())) return
    if (s.next) {
      const next = s.next
      s.next = null
      void start(query, next)
      return
    }
    if (aborted || req.gen !== s.gen) return
    st.loading = false
    st.fetched = true
    st.error = err
    st.data = err ? null : toResult(res)
    st.truncated = !err && !!res?.truncated
  }

  function request(query: string) {
    if (disposed) return
    const s = slot(query)
    const st = result(query)
    s.gen += 1
    s.key = keyFor(query)
    st.loading = true
    s.ctrl?.abort()
    const req = { gen: s.gen, values: pick(query) }
    if (s.inFlight) s.next = req
    else void start(query, req)
  }

  function demand(query: string) {
    if (demanded.has(query)) return
    demanded.add(query)
    shown.value = [...shown.value, query]
    if (uses(query) === undefined) {
      const st = result(query)
      st.error = `This artifact has no query named "${query}".`
      st.fetched = true
      return
    }
    request(query)
  }

  watch(() => o.values(), () => {
    for (const q of demanded) if (uses(q) !== undefined && slot(q).key !== keyFor(q)) request(q)
  }, { deep: true })

  if (o.rev) {
    // A new revision may rebind the view to other queries: forget what was
    // shown, so the remounted page asks again and dropped queries stop running.
    watch(o.rev, () => {
      demanded.clear()
      shown.value = []
    }, { flush: 'sync' })
  }

  function rerunAll() {
    for (const q of demanded) if (uses(q) !== undefined) request(q)
  }

  function dispose() {
    disposed = true
    for (const s of slots.values()) { s.ctrl?.abort(); s.next = null }
  }

  /** Names of shown queries whose result hit Go's row cap. */
  const truncated = computed(() => shown.value.filter((q) => states.get(q)?.truncated))

  return { result, demand, rerunAll, dispose, truncated }
}

function toResult(res: A2UIQueryResult | undefined): LiveQueryResult {
  return { columns: res?.columns ?? [], types: res?.types ?? [], rows: res?.rows ?? [], receipt_id: res?.receipt_id, receipt_note: res?.receipt_note }
}
