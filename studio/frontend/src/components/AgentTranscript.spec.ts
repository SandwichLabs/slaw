// The transcript's receipts (R5): the chip's receipt line with Open evidence, and
// the check and dotted marks on a finished answer's numbers.
import { describe, it, expect, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import AgentTranscript from './AgentTranscript.vue'
import { useEvidenceDrawer, _resetEvidenceForTests } from '../composables/useEvidenceDrawer'
import { rememberRun, _resetRunMemoryForTests } from '../composables/useReceipt'
import type { ChatMessage } from '../composables/useAgentChat'

const ID = 'r_0123456789abcdef0123'

function agent(over: Partial<ChatMessage>): ChatMessage {
  return { role: 'agent', text: '', steps: [], ...over }
}

describe('AgentTranscript receipts', () => {
  beforeEach(() => { _resetEvidenceForTests(); _resetRunMemoryForTests() })

  it('shows the receipt id, the row count and Open evidence on a read chip', async () => {
    rememberRun(ID, { sql: 'SELECT 1', params: { id: '7' } })
    const w = mount(AgentTranscript, {
      props: { messages: [agent({ text: 'ok', steps: [{ id: 'c1', tool: 'run_query', input: '{}', status: 'done', receipt: { callId: 'c1', tool: 'run_query', receiptId: ID, rows: 3 } }] })] },
    })
    const line = w.get('[data-testid="agent-receipt"]')
    expect(line.text()).toContain(ID)
    expect(w.get('[data-testid="agent-receipt-rows"]').text()).toBe('3 rows shown')
    await w.get('[data-testid="agent-open-evidence"]').trigger('click')
    const { target } = useEvidenceDrawer()
    expect(target.value?.receiptId).toBe(ID)
    expect(target.value?.fromId).toBe(true)
    expect(target.value?.memory?.sql).toBe('SELECT 1')
  })

  it('shows no receipt line on a chip with no receipt, and says when the rows were cut', () => {
    const w = mount(AgentTranscript, {
      props: { messages: [agent({ text: 'ok', steps: [
        { id: 'c1', tool: 'list_connections', status: 'done' },
        { id: 'c2', tool: 'preview_query', status: 'done', receipt: { callId: 'c2', tool: 'preview_query', receiptId: ID, rows: 1, truncated: true } },
      ] })] },
    })
    expect(w.findAll('[data-testid="agent-receipt"]')).toHaveLength(1)
    expect(w.get('[data-testid="agent-receipt-rows"]').text()).toBe('1 row shown (cut)')
  })
})

describe('AgentTranscript number marks', () => {
  beforeEach(() => { _resetEvidenceForTests(); _resetRunMemoryForTests() })

  const numbers = [
    { text: '$1.2M', matched: true, receiptId: ID, column: 'revenue' },
    { text: '12.5%', matched: false },
    { text: '98,765', matched: true, receiptId: ID },
  ]
  const text = 'Revenue was $1.2M, up 12.5%.\n\n`SELECT 98,765`\n\nSee [98,765 rows](https://x.test).'

  it('draws a check on a matched number and a dotted mark on one that is not from a query, with a legend', async () => {
    const w = mount(AgentTranscript, { props: { messages: [agent({ text, numbers })] } })
    const md = w.get('[data-testid="agent-answer"]')
    expect(md.findAll('.num-ok')).toHaveLength(1)
    expect(md.get('.num-ok').text()).toBe('$1.2M')
    expect(md.get('.num-none').text()).toBe('12.5%')
    expect(md.findAll('button.num-check')).toHaveLength(1)
    // Code and links are never decorated.
    expect(md.find('code .num-ok, a .num-ok, code button, a button').exists()).toBe(false)
    const legend = w.get('[data-testid="agent-number-legend"]').text()
    expect(legend).toContain('matches a query result')
    expect(legend).toContain('not from a query')
  })

  it('opens the evidence of the read behind a check', async () => {
    const w = mount(AgentTranscript, { props: { messages: [agent({ text, numbers })] }, attachTo: document.body })
    await w.get('button.num-check').trigger('click')
    const { target } = useEvidenceDrawer()
    expect(target.value?.receiptId).toBe(ID)
    expect(target.value?.fromId).toBe(true)
    w.unmount()
  })

  it('does not decorate while the answer is streaming, or with no verdicts', () => {
    const streaming = mount(AgentTranscript, { props: { messages: [agent({ text, numbers, streaming: true })] } })
    expect(streaming.find('.num-ok, .num-none').exists()).toBe(false)
    expect(streaming.find('[data-testid="agent-number-legend"]').exists()).toBe(false)
    const plain = mount(AgentTranscript, { props: { messages: [agent({ text })] } })
    expect(plain.find('.num-ok, .num-none').exists()).toBe(false)
  })

  it('leaves the answer text exactly as written', () => {
    const w = mount(AgentTranscript, { props: { messages: [agent({ text: 'Revenue was $1.2M, up 12.5%.', numbers })] } })
    const md = w.get('[data-testid="agent-answer"]').element.cloneNode(true) as HTMLElement
    md.querySelectorAll('button.num-check').forEach((b) => b.remove())
    expect(md.textContent?.trim()).toBe('Revenue was $1.2M, up 12.5%.')
  })
})
