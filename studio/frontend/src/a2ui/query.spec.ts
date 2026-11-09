// Unit tests for the pure A2UI query/formatting helpers, in particular
// pickLabelIndices (#67 step 2 round 3, item 3 follow-up): the line chart's
// X-axis label thinning algorithm, extracted from A2UILineChart.vue so it
// can be tested directly against realistic measured widths — jsdom's
// clientWidth is always 0, so A2UIPage.spec.ts's component-level tests can
// only assert structural guarantees (first/last shown, fewer than N), not
// the actual no-overlap behavior at a given container width.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { pickLabelIndices, toPoints, clampGap, runLiveQuery, _resetQueryPoolForTests } from './query'
import { formatNumber } from '../lib/formatCell'

// Deferred: a promise plus its own resolve, for scripting exactly when a
// mocked RunA2UIQuery call "returns" so the pool's queueing/ordering can be
// asserted deterministically rather than racing real timers.
function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}

describe('pickLabelIndices', () => {
  it('shows every label when they all fit', () => {
    const labels = ['a', 'b', 'c']
    expect(pickLabelIndices(labels, 600, 640)).toEqual([0, 1, 2])
  })

  it('handles 0 and 1 points without throwing', () => {
    expect(pickLabelIndices([], 300, 640)).toEqual([])
    expect(pickLabelIndices(['only'], 300, 640)).toEqual([0])
  })

  // The exact live-QA-caught bug: 6 monthly labels ("2026-01".."2026-06")
  // in a narrow ~300px card overlapped because the old algorithm showed
  // ALL of them (6 <= its fixed cap of 8) with no regard for actual pixel
  // width. At a realistic narrow width, this must now thin them.
  it('thins 6 "2026-0N" labels at a realistic narrow (300px) container width', () => {
    const labels = ['2026-01', '2026-02', '2026-03', '2026-04', '2026-05', '2026-06']
    const picked = pickLabelIndices(labels, 300, 640)
    expect(picked.length).toBeLessThan(6)
    expect(picked[0]).toBe(0)
    expect(picked[picked.length - 1]).toBe(5)
    // No two consecutive picks may be adjacent indices when they can't
    // possibly fit (adjacent points are only ~50px apart at this width;
    // one "2026-0N" label needs roughly 7*6.6+8 ≈ 54px).
    for (let i = 1; i < picked.length; i++) {
      expect(picked[i] - picked[i - 1]).toBeGreaterThan(1)
    }
  })

  it('never picks two indices whose physical pixel positions are closer than one label-width + pad', () => {
    const labels = Array.from({ length: 40 }, (_, i) => `p${i}`)
    const containerWidthPx = 320
    const viewBoxWidth = 640
    const picked = pickLabelIndices(labels, containerWidthPx, viewBoxWidth)
    const scale = containerWidthPx / viewBoxWidth
    const xPx = (i: number) => (i / (labels.length - 1)) * viewBoxWidth * scale
    const maxLabelWidthPx = Math.max(...labels.map((l) => l.length * 7.2)) + 12
    for (let k = 1; k < picked.length; k++) {
      const gap = xPx(picked[k]) - xPx(picked[k - 1])
      expect(gap).toBeGreaterThanOrEqual(maxLabelWidthPx - 1e-6)
    }
  })

  it('always includes the first and last index', () => {
    const labels = Array.from({ length: 24 }, (_, i) => `2026-${String((i % 12) + 1).padStart(2, '0')}`)
    const picked = pickLabelIndices(labels, 320, 640)
    expect(picked[0]).toBe(0)
    expect(picked[picked.length - 1]).toBe(23)
  })

  it('falls back to treating the viewBox width as the pixel width when containerWidthPx is 0 (unmeasured)', () => {
    const labels = ['a', 'b', 'c', 'd', 'e']
    const viaFallback = pickLabelIndices(labels, 0, 640)
    const viaExplicitFullWidth = pickLabelIndices(labels, 640, 640)
    expect(viaFallback).toEqual(viaExplicitFullWidth)
  })
})

// Review fix, step 4a round 2 #5: a background a2ui component storm (low
// priority) must never make a user-initiated cell Run (high priority) wait
// behind it — high priority gets a reserved slot the low lane can never
// fully consume.
describe('runLiveQuery priority lane', () => {
  beforeEach(() => {
    _resetQueryPoolForTests()
    ;(globalThis as any).window = (globalThis as any).window || {}
  })

  it('a high-priority call starts immediately even while low-priority calls saturate the other slots', async () => {
    const pending: Array<ReturnType<typeof deferred<any>>> = []
    ;(window as any).go = {
      main: {
        App: {
          RunA2UIQuery: vi.fn(() => {
            const d = deferred<any>()
            pending.push(d)
            return d.promise
          }),
        },
      },
    }

    // Fill the 3 low-priority slots (LOW_SLOT_CAP) with background work —
    // none of these resolve yet.
    const low1 = runLiveQuery('SELECT 1', undefined, 'low')
    const low2 = runLiveQuery('SELECT 2', undefined, 'low')
    const low3 = runLiveQuery('SELECT 3', undefined, 'low')
    await Promise.resolve() // let the pool's synchronous pump() run for each

    expect(pending).toHaveLength(3) // all 3 low slots taken, 1 reserved slot still free

    // A 4th, low-priority call now queues — it must NOT get the reserved slot.
    const low4 = runLiveQuery('SELECT 4', undefined, 'low')
    await Promise.resolve()
    expect(pending).toHaveLength(3)

    // The user clicks Run: high priority. It must claim the reserved slot
    // immediately, without waiting for any of the 3 low calls to finish.
    const high = runLiveQuery('SELECT high', undefined, 'high')
    await Promise.resolve()
    expect(pending).toHaveLength(4) // the high call is now in flight too

    // Resolve the high call FIRST, proving it never queued behind the
    // low-priority backlog.
    pending[3].resolve({ columns: ['x'], rows: [[1]] })
    const highResult = await high
    expect(highResult.error).toBeUndefined()
    expect(highResult.aborted).toBeUndefined()

    // Clean up the rest so nothing leaks into other tests. Resolving low1-3
    // frees slots under LOW_SLOT_CAP, which dispatches low4 to a NEW
    // pending entry — iterate with a live index so that gets resolved too.
    let i = 0
    while (i < pending.length) {
      pending[i].resolve({ columns: [], rows: [] }) // resolving an already-settled promise is a harmless no-op
      i++
      await Promise.resolve()
    }
    await Promise.all([low1, low2, low3, low4])
  })

  it('a queued high-priority call jumps ahead of an already-queued low-priority call for the next free slot', async () => {
    const pending: Array<ReturnType<typeof deferred<any>>> = []
    const started: string[] = []
    ;(window as any).go = {
      main: {
        App: {
          RunA2UIQuery: vi.fn((sql: string) => {
            started.push(sql)
            const d = deferred<any>()
            pending.push(d)
            return d.promise
          }),
        },
      },
    }

    // Saturate ALL 4 slots (3 low + 1 high) so anything further must queue.
    runLiveQuery('L1', undefined, 'low')
    runLiveQuery('L2', undefined, 'low')
    runLiveQuery('L3', undefined, 'low')
    runLiveQuery('H1', undefined, 'high')
    await Promise.resolve()
    expect(started).toEqual(['L1', 'L2', 'L3', 'H1'])

    // Now queue a low call, THEN a high call — high was enqueued SECOND but
    // must be dispatched FIRST once a slot frees.
    runLiveQuery('L4-queued', undefined, 'low')
    runLiveQuery('H2-queued', undefined, 'high')
    await Promise.resolve()
    expect(started).toEqual(['L1', 'L2', 'L3', 'H1']) // neither queued call has started yet

    // Free one slot (resolve H1) — the queued HIGH call must take it, not L4.
    pending[3].resolve({ columns: [], rows: [] })
    await Promise.resolve()
    await Promise.resolve()
    expect(started).toEqual(['L1', 'L2', 'L3', 'H1', 'H2-queued'])

    // Clean up.
    for (const d of pending) d.resolve({ columns: [], rows: [] })
  })
})

describe('formatNumber / toPoints / clampGap (smoke coverage)', () => {
  it('formats with thousands separators and trims trailing zeros', () => {
    expect(formatNumber(147464.2)).toBe('147,464.2')
    expect(formatNumber(142225.12)).toBe('142,225.12')
    expect(formatNumber(1000)).toBe('1,000')
  })

  it('coerces rows into label/value points', () => {
    expect(toPoints(['region', 'revenue'], [['North', 100], ['South', 50]])).toEqual([
      { label: 'North', value: 100 },
      { label: 'South', value: 50 },
    ])
  })

  it('clamps gap to [0, 64]', () => {
    expect(clampGap(999)).toBe(64)
    expect(clampGap(-50)).toBe(0)
    expect(clampGap('not a number')).toBe(0)
  })
})

// Notebook format v2 (#108): a `connection` routes the statement through
// RunA2UIQueryIn; without one the original binding is used unchanged.
describe('runLiveQuery connection', () => {
  beforeEach(() => {
    _resetQueryPoolForTests()
    ;(globalThis as any).window = (globalThis as any).window || {}
  })

  it('uses RunA2UIQueryIn when a connection is given, RunA2UIQuery otherwise', async () => {
    const result = { columns: ['a'], types: ['INTEGER'], rows: [[1]], truncated: false }
    const App = {
      RunA2UIQuery: vi.fn(async () => result),
      RunA2UIQueryIn: vi.fn(async () => result),
    }
    ;(window as any).go = { main: { App } }

    const plain = await runLiveQuery('SELECT 1')
    expect(plain.rows).toEqual([[1]])
    expect(App.RunA2UIQuery).toHaveBeenCalledWith('SELECT 1')
    expect(App.RunA2UIQueryIn).not.toHaveBeenCalled()

    const bound = await runLiveQuery('SELECT * FROM orders', undefined, 'high', 'prod_pg')
    expect(bound.rows).toEqual([[1]])
    expect(App.RunA2UIQueryIn).toHaveBeenCalledWith('SELECT * FROM orders', 'prod_pg', { kind: '' })
    expect(App.RunA2UIQuery).toHaveBeenCalledTimes(1)

    // An empty connection is "unset".
    await runLiveQuery('SELECT 2', undefined, 'low', '')
    expect(App.RunA2UIQuery).toHaveBeenCalledTimes(2)
  })

  it('passes the source (which cell or component asked) to RunA2UIQueryIn, with or without a connection', async () => {
    const App = { RunA2UIQuery: vi.fn(), RunA2UIQueryIn: vi.fn(async () => ({ columns: [], types: [], rows: [], receipt_id: 'r_1' })) }
    ;(window as any).go = { main: { App } }
    const source = { kind: 'cell', notebook: 'nb1', cell: 'c1' } as const
    const res = await runLiveQuery('SELECT 1', undefined, 'high', undefined, source)
    expect(App.RunA2UIQueryIn).toHaveBeenCalledWith('SELECT 1', '', source)
    expect(App.RunA2UIQuery).not.toHaveBeenCalled()
    expect(res.receipt_id).toBe('r_1')
  })

  it('surfaces a connection error inline instead of throwing', async () => {
    ;(window as any).go = { main: { App: { RunA2UIQueryIn: vi.fn(async () => { throw new Error('connection "x" is not attached') }) } } }
    const res = await runLiveQuery('SELECT 1', undefined, 'low', 'x')
    expect(res.error).toContain('not attached')
  })
})
