// A streaming delta re-renders the message that is streaming, never the history.
import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { reactive, nextTick } from 'vue'

const calls: string[] = []
vi.mock('../lib/markdown', async (orig) => {
  const real = await orig<typeof import('../lib/markdown')>()
  return { ...real, renderSafeMarkdown: (t: string) => { calls.push(t); return real.renderSafeMarkdown(t) } }
})

import AgentTranscript from './AgentTranscript.vue'
import type { ChatMessage } from '../composables/useAgentChat'

const ID = 'r_0123456789abcdef0123'

describe('AgentTranscript rendering', () => {
  it('renders a finished message once while a later one streams', async () => {
    const messages = reactive<ChatMessage[]>([
      { role: 'agent', text: 'Revenue was 1,234.', steps: [], numbers: [{ text: '1,234', matched: true, receiptId: ID, column: 'revenue' }] },
      { role: 'agent', text: 'Thin', steps: [], streaming: true },
    ])
    const w = mount(AgentTranscript, { props: { messages } })
    calls.length = 0
    for (const t of ['Thinking', 'Thinking of', 'Thinking of it']) {
      messages[1].text = t
      await nextTick()
    }
    expect(calls.filter((c) => c === 'Revenue was 1,234.')).toHaveLength(0)
    expect(calls).toEqual(['Thinking', 'Thinking of', 'Thinking of it'])
    expect(w.find('button.num-check').exists()).toBe(true)
    expect(w.get('button.num-check').attributes('title')).toBe('Matches a value in a query result (column revenue). Open evidence.')

    // A changed text or new verdicts render again.
    messages[0].text = 'Revenue was 1,234 units.'
    await nextTick()
    expect(calls.filter((c) => c === 'Revenue was 1,234 units.')).toHaveLength(1)
    w.unmount()
  })
})
