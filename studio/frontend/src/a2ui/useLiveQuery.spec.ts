// useLiveQuery — the live "sql" binding shared by the four data-bound A2UI
// components (F5): runs once on mount, stays 'low' priority on the component's
// connection, and drops a result that arrives after the host stopped caring.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, h, provide } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { useLiveQuery } from './useLiveQuery'
import { _resetQueryPoolForTests } from './query'
import type { A2UIComponentT } from './types'

function host(component: A2UIComponentT, signal?: AbortSignal) {
  let state!: ReturnType<typeof useLiveQuery>
  const Inner = defineComponent({
    setup() { state = useLiveQuery(() => component); return () => h('div') },
  })
  const Outer = defineComponent({
    setup() { if (signal) provide('a2uiAbortSignal', signal); return () => h(Inner) },
  })
  mount(Outer)
  return () => state
}

function setApp(app: Record<string, any>) {
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
}

describe('useLiveQuery', () => {
  beforeEach(() => { _resetQueryPoolForTests() })

  it('runs nothing for a component with inline data', async () => {
    const run = vi.fn()
    setApp({ RunA2UIQuery: run })
    const s = host({ id: 'a', type: 'stat', label: 'x', value: 1 } as A2UIComponentT)
    await flushPromises()
    expect(run).not.toHaveBeenCalled()
    expect(s().loading.value).toBe(false)
    expect(s().fetched.value).toBe(false)
  })

  it('loads on mount and hands over the result', async () => {
    setApp({ RunA2UIQuery: vi.fn(async () => ({ columns: ['n'], types: ['INTEGER'], rows: [[3]] })) })
    const s = host({ id: 'a', type: 'stat', label: 'x', sql: 'SELECT 3 AS n' } as A2UIComponentT)
    expect(s().loading.value).toBe(true)
    await flushPromises()
    expect(s().loading.value).toBe(false)
    expect(s().fetched.value).toBe(true)
    expect(s().error.value).toBe('')
    expect(s().data.value).toMatchObject({ columns: ['n'], rows: [[3]] })
  })

  it('uses the component’s connection when it names one', async () => {
    const inCatalog = vi.fn(async () => ({ columns: [], types: [], rows: [] }))
    const plain = vi.fn(async () => ({ columns: [], types: [], rows: [] }))
    setApp({ RunA2UIQueryIn: inCatalog, RunA2UIQuery: plain })
    host({ id: 'a', type: 'table', sql: 'SELECT 1', connection: 'prod_pg' } as A2UIComponentT)
    await flushPromises()
    expect(inCatalog).toHaveBeenCalledWith('SELECT 1', 'prod_pg', { kind: '' })
    expect(plain).not.toHaveBeenCalled()
  })

  it('a failed query is an error state, not a thrown rejection, and leaves no data', async () => {
    setApp({ RunA2UIQuery: vi.fn(async () => { throw new Error('query failed: Parser Error: nope') }) })
    const s = host({ id: 'a', type: 'table', sql: 'SELEKT' } as A2UIComponentT)
    await flushPromises()
    expect(s().error.value).toBe('Parser Error: nope')
    expect(s().fetched.value).toBe(true)
    expect(s().data.value).toBeNull()
    expect(s().loading.value).toBe(false)
  })

  it('drops a result that resolves after the host was aborted', async () => {
    let resolve!: (v: any) => void
    setApp({ RunA2UIQuery: vi.fn(() => new Promise((r) => { resolve = r })) })
    const ctl = new AbortController()
    const s = host({ id: 'a', type: 'table', sql: 'SELECT 1' } as A2UIComponentT, ctl.signal)
    await flushPromises()
    ctl.abort()
    resolve({ columns: ['x'], types: [], rows: [[1]] })
    await flushPromises()
    expect(s().data.value).toBeNull()
    expect(s().fetched.value).toBe(false)
  })
})
