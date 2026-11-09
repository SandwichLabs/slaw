// Receipts in the chat (R5): the conversation id round trip, the receipt on a
// tool chip, and Go's verdict on the answer's numbers kept on the message.
import { describe, it, expect, beforeEach, vi } from 'vitest'

const handlers = new Map<string, (p: any) => void>()
vi.mock('../wailsjs/runtime/runtime', () => ({
  EventsOn: (name: string, cb: (p: any) => void) => {
    handlers.set(name, cb)
    return () => { handlers.delete(name) }
  },
}))

import { useAgentChat } from './useAgentChat'
import { recallRun, _resetRunMemoryForTests } from './useReceipt'

const ID = 'r_0123456789abcdef0123'

function setBinding(impl: (turnId: string, msg: string, pinned: string, history: any[], conv: string) => Promise<any>) {
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: { AgentChatStream: vi.fn(impl), GetLLMSettings: vi.fn(async () => ({ provider: 'local', endpoint: 'x' })) } } }
  return (window as any).go.main.App.AgentChatStream
}

describe('useAgentChat conversation id', () => {
  beforeEach(() => { handlers.clear(); _resetRunMemoryForTests(); useAgentChat().resetAll() })

  it('sends "" first, then the id Go returned, and clears it on New chat', async () => {
    let n = 0
    const binding = setBinding(async () => ({ text: 'ok', steps: [], conversationId: `s-conv-${++n}` }))
    const chat = useAgentChat()
    await chat.send('one')
    await chat.send('two')
    expect(binding.mock.calls[0][4]).toBe('')
    expect(binding.mock.calls[1][4]).toBe('s-conv-1')

    chat.reset() // New chat
    await chat.send('three')
    expect(binding.mock.calls[2][4]).toBe('')
    await chat.send('four')
    expect(binding.mock.calls[3][4]).toBe('s-conv-3')
  })

  it('takes the id from the stream, so a stopped turn still continues its conversation', async () => {
    const binding = setBinding(async (turnId) => {
      handlers.get('agent:stream')!({ turnId, kind: 'delta', text: 'x', conversationId: 's-early' })
      throw new Error('stopped')
    })
    const chat = useAgentChat()
    await chat.send('one')
    binding.mockImplementation(async () => ({ text: 'ok', steps: [], conversationId: 's-early' }))
    await chat.send('two')
    expect(binding.mock.calls[1][4]).toBe('s-early')
  })

  it('forgets the id when the workspace changes', async () => {
    const binding = setBinding(async () => ({ text: 'ok', steps: [], conversationId: 's-conv-1' }))
    const chat = useAgentChat()
    await chat.send('one')
    chat.resetAll()
    await chat.send('two')
    expect(binding.mock.calls[1][4]).toBe('')
  })
})

describe('useAgentChat receipts', () => {
  beforeEach(() => { handlers.clear(); _resetRunMemoryForTests(); useAgentChat().resetAll() })

  it('puts the receipt of a read on its chip and remembers the run for the drawer', async () => {
    setBinding(async (turnId) => {
      handlers.get('agent:stream')!({ turnId, kind: 'tool', id: 'c1', tool: 'run_query', input: '{"sql":"SELECT 1","params":{"id":7}}' })
      handlers.get('agent:receipt')!({ turnId, run: { callId: 'c1', tool: 'run_query', receiptId: ID, rows: 3 } })
      handlers.get('agent:stream')!({ turnId, kind: 'tool_done', id: 'c1', tool: 'run_query', output: '{}' })
      return { text: 'done', steps: [], conversationId: 'c' }
    })
    const chat = useAgentChat()
    await chat.send('q')
    const step = chat.messages.value[1].steps[0]
    expect(step.receipt).toEqual({ callId: 'c1', tool: 'run_query', receiptId: ID, rows: 3 })
    expect(step.status).toBe('done')
    expect(recallRun(ID)).toEqual({ sql: 'SELECT 1', connection: undefined, params: { id: '7' } })
  })

  it('merges a receipt that arrives before its chip, and the result list as a fallback', async () => {
    setBinding(async (turnId) => {
      handlers.get('agent:receipt')!({ turnId, run: { callId: 'c1', tool: 'run_saved_query', receiptId: ID, rows: 1 } })
      handlers.get('agent:stream')!({ turnId, kind: 'tool', id: 'c1', tool: 'run_saved_query', input: '{"id":"q"}' })
      return {
        text: 'done', steps: [], conversationId: 'c',
        runs: [{ callId: 'c1', tool: 'run_saved_query', receiptId: ID, rows: 1 }, { callId: 'c2', tool: 'preview_query', receiptId: 'r_aaaaaaaaaaaaaaaaaaaa', rows: 5, truncated: true }],
      }
    })
    const chat = useAgentChat()
    await chat.send('q')
    const steps = chat.messages.value[1].steps
    expect(steps.map((s) => s.id)).toEqual(['c1', 'c2'])
    expect(steps[0].receipt?.receiptId).toBe(ID)
    expect(steps[1].receipt?.truncated).toBe(true)
  })

  it('ignores another turn\'s receipt event', async () => {
    setBinding(async (turnId) => {
      handlers.get('agent:receipt')!({ turnId: 'someone-else', run: { callId: 'c1', tool: 'run_query', receiptId: ID, rows: 1 } })
      handlers.get('agent:stream')!({ turnId, kind: 'tool', id: 'c1', tool: 'run_query', input: '{}' })
      return { text: 'done', steps: [], conversationId: 'c' }
    })
    const chat = useAgentChat()
    await chat.send('q')
    expect(chat.messages.value[1].steps[0].receipt).toBeUndefined()
  })

  it('keeps Go\'s number verdicts on the finished message only', async () => {
    setBinding(async () => ({
      text: 'Revenue was 1,234.', steps: [], conversationId: 'c',
      numbers: [{ text: '1,234', matched: true, receiptId: ID, column: 'revenue' }],
    }))
    const chat = useAgentChat()
    await chat.send('q')
    const m = chat.messages.value[1]
    expect(m.numbers).toEqual([{ text: '1,234', matched: true, receiptId: ID, column: 'revenue' }])
    expect(m.text).toBe('Revenue was 1,234.')
  })
})
