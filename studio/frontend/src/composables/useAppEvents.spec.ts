// useAppEvents — the before-close handshake: flush every pending save, then Quit;
// a failed or too-slow flush asks the person instead of quitting silently (#62).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

const App = vi.hoisted(() => ({ Quit: vi.fn(async () => {}), HoldQuit: vi.fn(async () => {}), CancelQuit: vi.fn(async () => {}) }))
vi.mock('../lib/bindings', () => ({ App }))
const askConfirm = vi.hoisted(() => vi.fn(async (_o: any) => false))
vi.mock('./useConfirm', () => ({ askConfirm }))
import { register, _resetForTests } from './usePendingSaves'
import { handleBeforeClose, SLOW_FLUSH_MS, FLUSH_TIMEOUT_MS } from './useAppEvents'

describe('handleBeforeClose', () => {
  beforeEach(() => { vi.useFakeTimers(); _resetForTests(); vi.clearAllMocks() })
  afterEach(() => { vi.useRealTimers() })

  it('quits cleanly when every flush succeeds', async () => {
    register(() => 'nb', async () => {})
    await handleBeforeClose()
    expect(askConfirm).not.toHaveBeenCalled()
    expect(App.Quit).toHaveBeenCalledTimes(1)
  })

  it('a slow save that finishes inside the bounded wait still quits cleanly', async () => {
    register(() => 'nb', () => new Promise<void>((r) => setTimeout(r, SLOW_FLUSH_MS + 2000)))
    const p = handleBeforeClose()
    await vi.advanceTimersByTimeAsync(SLOW_FLUSH_MS + 2500)
    await p
    expect(App.HoldQuit).toHaveBeenCalled() // the Go watchdog was stopped while we kept waiting
    expect(askConfirm).not.toHaveBeenCalled()
    expect(App.Quit).toHaveBeenCalledTimes(1)
  })

  it('a flush that never finishes resolves as a failure: the prompt names it, Quit waits', async () => {
    register(() => 'Budget notebook', () => new Promise<void>(() => {}))
    const p = handleBeforeClose()
    await vi.advanceTimersByTimeAsync(FLUSH_TIMEOUT_MS + 10)
    await p
    expect(askConfirm).toHaveBeenCalledTimes(1)
    expect(askConfirm.mock.calls[0][0].message).toContain("couldn't be saved")
    expect(App.Quit).not.toHaveBeenCalled()
    expect(App.CancelQuit).toHaveBeenCalled()
  })

  it('names the slow editor, not a notebook, when another kind of editor is late', async () => {
    register(() => 'notebook', async () => {})
    register(() => 'Query "orders"', () => new Promise<void>(() => {}))
    const p = handleBeforeClose()
    await vi.advanceTimersByTimeAsync(FLUSH_TIMEOUT_MS + 10)
    await p
    const msg = askConfirm.mock.calls[0][0].message
    expect(msg).toContain('Query "orders"')
    expect(msg).not.toContain('notebook')
  })

  it('Quit anyway after a timeout quits', async () => {
    askConfirm.mockResolvedValueOnce(true)
    register(() => 'nb', () => new Promise<void>(() => {}))
    const p = handleBeforeClose()
    await vi.advanceTimersByTimeAsync(FLUSH_TIMEOUT_MS + 10)
    await p
    expect(App.Quit).toHaveBeenCalledTimes(1)
  })

  it('a rejected flush is a failure too', async () => {
    register(() => 'nb', async () => { throw new Error('disk full') })
    await handleBeforeClose()
    expect(askConfirm.mock.calls[0][0].message).toContain('disk full')
    expect(App.Quit).not.toHaveBeenCalled()
  })
})
