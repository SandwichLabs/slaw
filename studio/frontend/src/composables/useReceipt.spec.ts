import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { useReceipt, rememberRun, recallRun, paramText, _resetRunMemoryForTests } from './useReceipt'
import { useEvidenceDrawer, openEvidence, openEvidenceById, closeEvidence, _resetEvidenceForTests } from './useEvidenceDrawer'

function setApp(app: Record<string, any>) {
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
}

describe('useReceipt', () => {
  beforeEach(() => _resetRunMemoryForTests())

  it('loads a receipt by id', async () => {
    setApp({ GetReceipt: vi.fn(async (id: string) => ({ id })) })
    const r = useReceipt()
    const p = r.load('r_1')
    expect(r.loading.value).toBe(true)
    await p
    expect(r.loading.value).toBe(false)
    expect(r.receipt.value).toMatchObject({ id: 'r_1' })
    expect(r.error.value).toBe('')
  })

  it('a failed load is an error, not a throw, and leaves no receipt', async () => {
    setApp({ GetReceipt: vi.fn(async () => { throw new Error('no receipt with that id') }) })
    const r = useReceipt()
    await r.load('r_x')
    expect(r.receipt.value).toBeNull()
    expect(r.error.value).toContain('no receipt with that id')
  })

  it('no id means nothing to load and no call', async () => {
    const GetReceipt = vi.fn()
    setApp({ GetReceipt })
    const r = useReceipt()
    await r.load(undefined)
    expect(GetReceipt).not.toHaveBeenCalled()
    expect(r.receipt.value).toBeNull()
    expect(r.loading.value).toBe(false)
  })

  it('a later load wins over an earlier answer that arrives late', async () => {
    const waiting: Record<string, (v: any) => void> = {}
    setApp({ GetReceipt: vi.fn((id: string) => new Promise((res) => { waiting[id] = res })) })
    const r = useReceipt()
    const first = r.load('r_a')
    const second = r.load('r_b')
    waiting.r_b({ id: 'r_b' })
    await second
    waiting.r_a({ id: 'r_a' })
    await first
    expect(r.receipt.value).toMatchObject({ id: 'r_b' })
  })

  it('remembers the in-memory side of a run by id, and keeps only the newest 200', () => {
    rememberRun('r_1', { sql: 'SELECT 1', params: { a: '1' } })
    expect(recallRun('r_1')).toEqual({ sql: 'SELECT 1', params: { a: '1' } })
    expect(recallRun('r_nope')).toBeUndefined()
    rememberRun(undefined, { sql: 'ignored' })
    for (let i = 0; i < 205; i++) rememberRun(`r_n${i}`, { sql: String(i) })
    expect(recallRun('r_1')).toBeUndefined()
    expect(recallRun('r_n204')).toBeDefined()
    expect(recallRun('r_n5')).toBeDefined()
    expect(recallRun('r_n4')).toBeUndefined()
  })

  it('paramText makes parameter values text and drops an empty set', () => {
    expect(paramText({ a: 1, b: true, c: 'x' })).toEqual({ a: '1', b: 'true', c: 'x' })
    expect(paramText({})).toBeUndefined()
    expect(paramText(undefined)).toBeUndefined()
  })
})

describe('useEvidenceDrawer', () => {
  beforeEach(() => { _resetEvidenceForTests(); _resetRunMemoryForTests() })

  it('opens with a target, replaces it when opened again, and closes', () => {
    const d = useEvidenceDrawer()
    expect(d.target.value).toBeNull()
    openEvidence({ title: 'A', receiptId: 'r_1' })
    expect(d.target.value?.title).toBe('A')
    openEvidence({ title: 'B', note: 'not_kept' })
    expect(d.target.value?.title).toBe('B')
    expect(d.target.value?.receiptId).toBeUndefined()
    closeEvidence()
    expect(d.target.value).toBeNull()
  })

  it('fills in what rememberRun kept for the receipt when the opener gives none', () => {
    rememberRun('r_1', { sql: 'SELECT 1' })
    openEvidence({ title: 'A', receiptId: 'r_1' })
    expect(useEvidenceDrawer().target.value?.memory).toEqual({ sql: 'SELECT 1' })
    openEvidence({ title: 'B', receiptId: 'r_1', memory: { sql: 'SELECT 2' } })
    expect(useEvidenceDrawer().target.value?.memory).toEqual({ sql: 'SELECT 2' })
  })

  it('opened from an id: the title is the id and there is no value', () => {
    openEvidenceById('r_abc')
    expect(useEvidenceDrawer().target.value).toMatchObject({ title: 'r_abc', receiptId: 'r_abc', fromId: true })
  })

  it('close hands focus back to the element that opened it', () => {
    const btn = document.createElement('button')
    document.body.appendChild(btn)
    openEvidence({ title: 'A', receiptId: 'r_1' }, btn)
    const other = document.createElement('input')
    document.body.appendChild(other)
    other.focus()
    closeEvidence()
    expect(document.activeElement).toBe(btn)
    btn.remove()
    other.remove()
  })

  it('takes the element from a click event', () => {
    const btn = document.createElement('button')
    document.body.appendChild(btn)
    let ev!: MouseEvent
    btn.addEventListener('click', (e) => { ev = e; openEvidence({ title: 'A', receiptId: 'r_1' }, e) })
    btn.click()
    expect(ev).toBeDefined()
    closeEvidence()
    expect(document.activeElement).toBe(btn)
    btn.remove()
  })

  it('does not focus an opener that has left the page', () => {
    const btn = document.createElement('button')
    document.body.appendChild(btn)
    openEvidence({ title: 'A', receiptId: 'r_1' }, btn)
    btn.remove()
    expect(() => closeEvidence()).not.toThrow()
  })
})

void flushPromises
