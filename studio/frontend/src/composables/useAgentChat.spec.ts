// Tests for the useAgentChat composable (W0.5): streaming event handling,
// running->done tool chips, multi-turn history, and markdown sanitization.
import { describe, it, expect, beforeEach, vi } from 'vitest'

// Capture the handler registered via EventsOn so tests can drive a stream.
let streamHandler: ((p: any) => void) | null = null
vi.mock('../wailsjs/runtime/runtime', () => ({
  EventsOn: (_name: string, cb: (p: any) => void) => {
    streamHandler = cb
    return () => { streamHandler = null }
  },
}))

import { useAgentChat, renderMarkdown } from './useAgentChat'

let llmSettings: any = { provider: 'local', endpoint: 'http://localhost:8080/v1' }

function setBinding(impl: (turnId: string, msg: string, pinned: string, history: any[]) => Promise<any>) {
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: { AgentChatStream: vi.fn(impl), GetLLMSettings: vi.fn(async () => llmSettings) } } }
  return (window as any).go.main.App.AgentChatStream
}

describe('useAgentChat (W0.5)', () => {
  beforeEach(() => { streamHandler = null; useAgentChat().resetAll() })

  it('streams deltas + running->done tool chips and finalizes', async () => {
    const binding = setBinding(async (turnId) => {
      // Simulate the backend's event sequence for this turn.
      streamHandler!({ turnId, kind: 'delta', text: 'Hel' })
      streamHandler!({ turnId, kind: 'tool', id: 't1', tool: 'list_tables', input: '' })
      // Mid-flight the chip should be "running".
      streamHandler!({ turnId, kind: 'tool_done', id: 't1', tool: 'list_tables', output: 'customers' })
      streamHandler!({ turnId, kind: 'delta', text: 'lo' })
      streamHandler!({ turnId, kind: 'final', text: 'Hello. You have customers.' })
      return { text: 'Hello. You have customers.', steps: [] }
    })

    const chat = useAgentChat()
    await chat.send('hi')

    expect(binding).toHaveBeenCalledTimes(1)
    expect(chat.messages.value).toHaveLength(2)
    const agent = chat.messages.value[1]
    expect(agent.role).toBe('agent')
    expect(agent.text).toBe('Hello. You have customers.')
    expect(agent.streaming).toBe(false)
    expect(agent.steps).toHaveLength(1)
    expect(agent.steps[0].tool).toBe('list_tables')
    expect(agent.steps[0].status).toBe('done')
    expect(agent.steps[0].output).toBe('customers')
  })

  it('marks a denied/errored tool call as status "error", not "done"', async () => {
    setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'tool', id: 't1', tool: 'create_table', input: '{}' })
      streamHandler!({ turnId, kind: 'tool_done', id: 't1', tool: 'create_table', output: 'denied', isError: true })
      streamHandler!({ turnId, kind: 'final', text: 'Cannot do that.' })
      return { text: 'Cannot do that.', steps: [] }
    })

    const chat = useAgentChat()
    await chat.send('create a table')

    const agent = chat.messages.value[1]
    expect(agent.steps).toHaveLength(1)
    expect(agent.steps[0].status).toBe('error')
    expect(agent.steps[0].output).toBe('denied')
  })

  it('threads multi-turn history to the backend', async () => {
    const binding = setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'final', text: 'reply' })
      return { text: 'reply', steps: [] }
    })

    const chat = useAgentChat()
    await chat.send('first question')
    await chat.send('second question')

    expect(binding).toHaveBeenCalledTimes(2)
    const secondCallHistory = binding.mock.calls[1][3]
    // History sent on the 2nd turn includes the 1st user + agent messages.
    expect(secondCallHistory).toEqual([
      { role: 'user', text: 'first question' },
      { role: 'agent', text: 'reply' },
    ])
  })

  it('passes pinned context and renders it', async () => {
    const binding = setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'final', text: 'ok' })
      return { text: 'ok', steps: [] }
    })
    const chat = useAgentChat()
    chat.pin('the tables screen (/tables)')
    expect(chat.pinnedContext.value).toContain('tables')
    await chat.send('what is here?')
    expect(binding.mock.calls[0][2]).toContain('tables')
  })

  it('surfaces a graceful error when the binding rejects', async () => {
    setBinding(async () => { throw new Error('agent error: connection refused') })
    const chat = useAgentChat()
    await chat.send('hi')
    const agent = chat.messages.value[1]
    expect(agent.role).toBe('error')
    expect(agent.text).toContain('connection refused')
    expect(chat.sending.value).toBe(false)
  })

  it('does not send empty messages', async () => {
    const binding = setBinding(async () => ({ text: 'x', steps: [] }))
    const chat = useAgentChat()
    await chat.send('   ')
    expect(binding).not.toHaveBeenCalled()
  })

  // present_data (#67): a successful tool_done for present_data should parse
  // its JSON output into a page the panel renders inline; a denied/failed
  // call, or any other tool, must never populate `page`.
  it('parses a successful present_data result into step.page', async () => {
    const pageJSON = JSON.stringify({ title: 'Customers', components: [{ id: 'h', type: 'heading', text: 'Customers' }] })
    setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'tool', id: 't1', tool: 'present_data', input: '{}' })
      streamHandler!({ turnId, kind: 'tool_done', id: 't1', tool: 'present_data', output: pageJSON })
      streamHandler!({ turnId, kind: 'final', text: 'Here you go.' })
      return { text: 'Here you go.', steps: [] }
    })
    const chat = useAgentChat()
    await chat.send('show me customers')
    const step = chat.messages.value[1].steps[0]
    expect(step.page).toEqual({ title: 'Customers', components: [{ id: 'h', type: 'heading', text: 'Customers' }] })
  })

  it('does not set page for a denied present_data call', async () => {
    setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'tool', id: 't1', tool: 'present_data', input: '{}' })
      streamHandler!({ turnId, kind: 'tool_done', id: 't1', tool: 'present_data', output: 'components[0].type: unknown type "pieChart"', isError: true })
      streamHandler!({ turnId, kind: 'final', text: 'Sorry, that failed.' })
      return { text: 'Sorry, that failed.', steps: [] }
    })
    const chat = useAgentChat()
    await chat.send('show me a pie chart')
    const step = chat.messages.value[1].steps[0]
    expect(step.status).toBe('error')
    expect(step.page).toBeUndefined()
  })

  // #70 grounding check: a "verifying" event sets a verifying flag, cleared
  // once the turn's own "final" event arrives.
  it('sets verifying=true on a "verifying" event and clears it on "final"', async () => {
    setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'delta', text: 'roughly 40' })
      streamHandler!({ turnId, kind: 'verifying' })
      streamHandler!({ turnId, kind: 'tool', id: 't1', tool: 'run_query', input: '{}' })
      streamHandler!({ turnId, kind: 'tool_done', id: 't1', tool: 'run_query', output: '[{"n":3}]' })
      streamHandler!({ turnId, kind: 'final', text: 'exactly 3, verified' })
      return { text: 'exactly 3, verified', steps: [] }
    })
    const chat = useAgentChat()
    await chat.send('how many rows roughly')
    const agent = chat.messages.value[1]
    expect(agent.verifying).toBe(false)
    expect(agent.text).toBe('exactly 3, verified')
  })

  // Review fix (round 2): a "verifying" event with NO matching "final"/
  // "error" ever following it (e.g. the binding's own authoritative result
  // settles the turn without that follow-up event landing) must not leave
  // "Agent · verifying" stuck forever — the finally block clears it
  // unconditionally once the AgentChatStream promise itself settles.
  it('clears verifying once the turn settles even if no final/error event follows it', async () => {
    setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'verifying' })
      // No "final"/"error" event ever emitted — only the binding's own
      // resolved result settles the turn.
      return { text: 'settled without a final event', steps: [] }
    })
    const chat = useAgentChat()
    await chat.send('how many rows roughly')
    const agent = chat.messages.value[1]
    expect(agent.verifying).toBe(false)
    expect(agent.text).toBe('settled without a final event')
  })

  // propose_schema (#68 follow-up): there is no dedicated Schema Builder
  // diagram screen any more — a proposed schema renders inline the same way
  // present_data does.
  it('parses a successful propose_schema result into step.page', async () => {
    const pageJSON = JSON.stringify({
      title: 'Proposed schema',
      components: [
        { id: 'h1', type: 'heading', text: 'Proposed schema', level: 1 },
        { id: 'h2', type: 'heading', text: 'customers', level: 2 },
        { id: 't1', type: 'table', columns: ['Column', 'Type', 'Constraints'], rows: [['id', 'ID', 'PRIMARY KEY']] },
      ],
    })
    setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'tool', id: 't1', tool: 'propose_schema', input: '{}' })
      streamHandler!({ turnId, kind: 'tool_done', id: 't1', tool: 'propose_schema', output: pageJSON })
      streamHandler!({ turnId, kind: 'final', text: 'Want me to build this?' })
      return { text: 'Want me to build this?', steps: [] }
    })
    const chat = useAgentChat()
    await chat.send('design my database')
    const step = chat.messages.value[1].steps[0]
    expect(step.page?.title).toBe('Proposed schema')
    expect(step.page?.components).toHaveLength(3)
  })

  it('does not set page for a denied propose_schema call', async () => {
    setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'tool', id: 't1', tool: 'propose_schema', input: '{}' })
      streamHandler!({ turnId, kind: 'tool_done', id: 't1', tool: 'propose_schema', output: 'propose_schema needs at least one table', isError: true })
      streamHandler!({ turnId, kind: 'final', text: 'Sorry, that failed.' })
      return { text: 'Sorry, that failed.', steps: [] }
    })
    const chat = useAgentChat()
    await chat.send('design nothing')
    const step = chat.messages.value[1].steps[0]
    expect(step.status).toBe('error')
    expect(step.page).toBeUndefined()
  })

  it('does not set page for a non-present_data tool, even with JSON-shaped output', async () => {
    setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'tool', id: 't1', tool: 'run_query', input: '{}' })
      streamHandler!({ turnId, kind: 'tool_done', id: 't1', tool: 'run_query', output: '{"title":"x","components":[]}' })
      streamHandler!({ turnId, kind: 'final', text: 'done' })
      return { text: 'done', steps: [] }
    })
    const chat = useAgentChat()
    await chat.send('run a query')
    const step = chat.messages.value[1].steps[0]
    expect(step.page).toBeUndefined()
  })
})

describe('renderMarkdown', () => {
  it('renders markdown to HTML', () => {
    const html = renderMarkdown('**bold** and `code`')
    expect(html).toContain('<strong>bold</strong>')
    expect(html).toContain('<code>code</code>')
  })

  it('sanitizes dangerous HTML (XSS guard for the v-html sink)', () => {
    const html = renderMarkdown('hi <img src=x onerror=alert(1)> <script>alert(2)</script>')
    expect(html).not.toContain('onerror')
    expect(html).not.toContain('<script>')
  })
})

// #79: agent write approval — attaches to the matching step by callId,
// survives arriving before the 'tool' event, and resolveApproval calls the
// binding.
describe('useAgentChat approvals (#79)', () => {
  it('attaches an agent:approval event to the step with the matching callId', async () => {
    const handlers: Record<string, (p: any) => void> = {}
    vi.doMock('../wailsjs/runtime/runtime', () => ({
      EventsOn: (name: string, cb: (p: any) => void) => { handlers[name] = cb; return () => { delete handlers[name] } },
    }))
    vi.resetModules()
    const { useAgentChat: freshUseAgentChat } = await import('./useAgentChat')

    ;(globalThis as any).window = (globalThis as any).window || {}
    const binding = vi.fn(async (turnId: string) => {
      handlers['agent:stream']({ turnId, kind: 'tool', id: 'call-1', tool: 'delete_record', input: '{}' })
      handlers['agent:approval']({ turnId, request: { id: 'appr-1', callId: 'call-1', tool: 'delete_record', summary: 'Delete 3 rows from orders', rows: 3, destructive: true, mode: 'read-write' } })
      return { text: 'ok', steps: [] }
    })
    ;(window as any).go = { main: { App: { AgentChatStream: binding, ResolveAgentApproval: vi.fn(async () => {}) } } }

    const chat = freshUseAgentChat()
    await chat.send('delete the void orders')

    const step = chat.messages.value[1].steps[0]
    expect(step.approval?.id).toBe('appr-1')
    expect(step.approval?.summary).toBe('Delete 3 rows from orders')
    expect(step.approval?.state).toBe('pending')
    // Only one step, not a duplicate from the approval event pre-creating one.
    expect(chat.messages.value[1].steps).toHaveLength(1)
  })

  it('does not duplicate the step when agent:approval arrives BEFORE the tool event', async () => {
    const handlers: Record<string, (p: any) => void> = {}
    vi.doMock('../wailsjs/runtime/runtime', () => ({
      EventsOn: (name: string, cb: (p: any) => void) => { handlers[name] = cb; return () => { delete handlers[name] } },
    }))
    vi.resetModules()
    const { useAgentChat: freshUseAgentChat } = await import('./useAgentChat')

    ;(globalThis as any).window = (globalThis as any).window || {}
    const binding = vi.fn(async (turnId: string) => {
      handlers['agent:approval']({ turnId, request: { id: 'appr-2', callId: 'call-2', tool: 'delete_record', summary: 'Delete rows', rows: 3, destructive: true, mode: 'read-write' } })
      handlers['agent:stream']({ turnId, kind: 'tool', id: 'call-2', tool: 'delete_record', input: '{}' })
      return { text: 'ok', steps: [] }
    })
    ;(window as any).go = { main: { App: { AgentChatStream: binding, ResolveAgentApproval: vi.fn(async () => {}) } } }

    const chat = freshUseAgentChat()
    await chat.send('delete the void orders')

    expect(chat.messages.value[1].steps).toHaveLength(1)
    expect(chat.messages.value[1].steps[0].approval?.id).toBe('appr-2')
    expect(chat.messages.value[1].steps[0].tool).toBe('delete_record')
  })

  it('agent:approval_done flips the approval state', async () => {
    const handlers: Record<string, (p: any) => void> = {}
    vi.doMock('../wailsjs/runtime/runtime', () => ({
      EventsOn: (name: string, cb: (p: any) => void) => { handlers[name] = cb; return () => { delete handlers[name] } },
    }))
    vi.resetModules()
    const { useAgentChat: freshUseAgentChat } = await import('./useAgentChat')

    ;(globalThis as any).window = (globalThis as any).window || {}
    const binding = vi.fn(async (turnId: string) => {
      handlers['agent:stream']({ turnId, kind: 'tool', id: 'call-3', tool: 'delete_record', input: '{}' })
      handlers['agent:approval']({ turnId, request: { id: 'appr-3', callId: 'call-3', tool: 'delete_record', summary: 'Delete rows', rows: 3, destructive: true, mode: 'read-write' } })
      handlers['agent:approval_done']({ turnId, id: 'appr-3', outcome: 'approved' })
      return { text: 'ok', steps: [] }
    })
    ;(window as any).go = { main: { App: { AgentChatStream: binding, ResolveAgentApproval: vi.fn(async () => {}) } } }

    const chat = freshUseAgentChat()
    await chat.send('delete the void orders')

    expect(chat.messages.value[1].steps[0].approval?.state).toBe('approved')
  })

  it('resolveApproval sets state to sending then calls the binding', async () => {
    const { useAgentChat: freshUseAgentChat } = await import('./useAgentChat')
    ;(globalThis as any).window = (globalThis as any).window || {}
    const resolveBinding = vi.fn(async () => {})
    ;(window as any).go = { main: { App: { ResolveAgentApproval: resolveBinding } } }

    const chat = freshUseAgentChat()
    const step = { id: 'call-4', tool: 'delete_record', status: 'running' as const, approval: { id: 'appr-4', summary: 's', destructive: true, state: 'pending' as const } }
    await chat.resolveApproval(step, true)

    expect(step.approval.state).toBe('sending')
    expect(resolveBinding).toHaveBeenCalledWith('appr-4', true)
  })

  it('error hint: local server names its endpoint; cloud points at Settings', async () => {
    llmSettings = { provider: 'ollama', endpoint: 'http://localhost:11434/v1' }
    setBinding(async () => { throw new Error('agent error: connection refused') })
    let chat = useAgentChat()
    chat.resetAll()
    await chat.send('hi')
    expect(chat.messages.value[1].text).toBe('Assistant error: connection refused. Is the model server at http://localhost:11434/v1 running?')
    expect(chat.messages.value[1].text).not.toContain('LLM server reachable')

    llmSettings = { provider: 'cloudflare', endpoint: '' }
    setBinding(async () => { throw new Error('401 unauthorized') })
    chat = useAgentChat()
    chat.resetAll()
    await chat.send('hi')
    expect(chat.messages.value[1].text).toBe('Assistant error: 401 unauthorized. Check the API key and model in Settings > App.')
    llmSettings = { provider: 'local', endpoint: 'http://localhost:8080/v1' }
  })

  it('an error event shows the hint once even when the binding also rejects', async () => {
    llmSettings = { provider: 'openai', endpoint: '' }
    setBinding(async (turnId) => {
      streamHandler!({ turnId, kind: 'error', text: 'bad key' })
      throw new Error('bad key')
    })
    const chat = useAgentChat()
    chat.resetAll()
    await chat.send('hi')
    const t = chat.messages.value[1].text
    expect(t.match(/Check the API key/g)).toHaveLength(1)
    llmSettings = { provider: 'local', endpoint: 'http://localhost:8080/v1' }
  })
})


// #2: the chat is a singleton shared by every consumer and wiped per workspace.
describe('useAgentChat singleton + workspace scoping (#2)', () => {
  async function fresh() {
    const handlers: Record<string, ((p: any) => void)[]> = {}
    vi.doMock('../wailsjs/runtime/runtime', () => ({
      EventsOn: (name: string, cb: (p: any) => void) => { (handlers[name] ||= []).push(cb); return () => { handlers[name] = handlers[name].filter((h) => h !== cb) } },
    }))
    vi.resetModules()
    ;(window as any).runtime = {}
    const mod = await import('./useAgentChat')
    const fire = (n: string, p?: any) => (handlers[n] || []).slice().forEach((h) => h(p))
    return { ...mod, fire }
  }

  it('two consumers see the same conversation', async () => {
    const { useAgentChat: u } = await fresh()
    ;(window as any).go = { main: { App: { AgentChatStream: vi.fn(async () => ({ text: 'yo', steps: [] })) } } }
    const a = u(), b = u()
    await a.send('hello')
    expect(b.messages.value).toHaveLength(2)
    expect(b.messages.value[0].text).toBe('hello')
  })

  it.each(['workspace:opened', 'workspace:closed'])('%s wipes the transcript and unlocks sending; the old turn cannot leak in', async (evt) => {
    const { useAgentChat: u, fire } = await fresh()
    let finish!: (v: any) => void
    ;(window as any).go = { main: { App: { AgentChatStream: vi.fn(() => new Promise((r) => { finish = r })) } } }
    const chat = u()
    chat.pin('the notebooks screen')
    const turn = chat.send('old workspace question')
    expect(chat.sending.value).toBe(true)

    fire(evt)
    expect(chat.messages.value).toHaveLength(0)
    expect(chat.sending.value).toBe(false)
    expect(chat.pinnedContext.value).toBe('')

    // New workspace starts a turn; the old turn settling must not unlock it.
    const second = vi.fn(() => new Promise<any>(() => {}))
    ;(window as any).go.main.App.AgentChatStream = second
    void chat.send('new workspace question')
    expect(chat.sending.value).toBe(true)
    finish({ text: 'stale', steps: [] })
    await turn
    expect(chat.sending.value).toBe(true)
    expect(chat.messages.value.map((m) => m.text)).toEqual(['new workspace question', ''])
  })
})

// Cancel an in-flight turn.
describe('useAgentChat cancel', () => {
  beforeEach(() => { useAgentChat().resetAll() })

  it('cancel() calls CancelAgentQuery with the turn id and ends the turn as Cancelled', async () => {
    let turn = ''
    let reject!: (e: Error) => void
    const cancelBinding = vi.fn(async () => {})
    ;(window as any).go = { main: { App: {
      AgentChatStream: vi.fn((id: string) => { turn = id; return new Promise((_r, rej) => { reject = rej }) }),
      CancelAgentQuery: cancelBinding,
    } } }
    const chat = useAgentChat()
    const done = chat.send('do a thing')
    expect(chat.sending.value).toBe(true)
    streamHandler!({ turnId: turn, kind: 'tool', id: 't1', tool: 'create_dashboard', input: '{}' })

    await chat.cancel()
    expect(cancelBinding).toHaveBeenCalledWith(turn)
    // Go then emits "cancelled" and the binding rejects; neither may show an error.
    streamHandler!({ turnId: turn, kind: 'cancelled' })
    reject(new Error('agent turn cancelled: context canceled'))
    await done

    const m = chat.messages.value[1]
    expect(m.cancelled).toBe(true)
    expect(m.role).toBe('agent')
    expect(m.steps[0].status).toBe('error')
    expect(chat.sending.value).toBe(false)
  })

  it('cancel() closes a pending approval card and is a no-op when idle', async () => {
    const cancelBinding = vi.fn(async () => {})
    let turn = ''
    ;(window as any).go = { main: { App: {
      AgentChatStream: vi.fn((id: string) => { turn = id; return new Promise(() => {}) }),
      CancelAgentQuery: cancelBinding,
    } } }
    const chat = useAgentChat()
    await chat.cancel()
    expect(cancelBinding).not.toHaveBeenCalled()

    void chat.send('delete')
    streamHandler!({ turnId: turn, kind: 'tool', id: 'c1', tool: 'delete_record', input: '{}' })
    const step = chat.messages.value[1].steps[0]
    step.approval = { id: 'a1', summary: 's', destructive: true, state: 'pending' }
    await chat.cancel()
    expect(step.approval.state).toBe('cancelled')
  })
})

describe('useAgentChat cancel edge cases (review #42b)', () => {
  beforeEach(() => { useAgentChat().resetAll() })

  it('a late tool_done after cancel updates the same chip, no duplicate', async () => {
    let turn = ''
    let reject!: (e: Error) => void
    ;(window as any).go = { main: { App: {
      AgentChatStream: vi.fn((id: string) => { turn = id; return new Promise((_r, rej) => { reject = rej }) }),
      CancelAgentQuery: vi.fn(async () => {}),
    } } }
    const chat = useAgentChat()
    const done = chat.send('x')
    streamHandler!({ turnId: turn, kind: 'tool', id: 't1', tool: 'run_query', input: '{}' })
    await chat.cancel()
    streamHandler!({ turnId: turn, kind: 'tool_done', id: 't1', tool: 'run_query', output: 'rows' })
    streamHandler!({ turnId: turn, kind: 'cancelled' })
    reject(new Error('agent turn cancelled'))
    await done
    expect(chat.messages.value[1].steps).toHaveLength(1)
  })

  it('"already running" gets no model-server hint and offers Stop for whatever is running', async () => {
    const cancelBinding = vi.fn(async () => {})
    ;(window as any).go = { main: { App: {
      AgentChatStream: vi.fn(async () => { throw new Error('an assistant turn is already running; cancel it or wait for it to finish') }),
      GetLLMSettings: vi.fn(async () => ({ provider: 'local', endpoint: 'http://x' })),
      CancelAgentQuery: cancelBinding,
    } } }
    const chat = useAgentChat()
    await chat.send('again')
    expect(chat.messages.value[1].text).not.toContain('model server')
    expect(chat.stoppable.value).toBe(true)
    await chat.cancel()
    expect(cancelBinding).toHaveBeenCalledWith('')
    expect(chat.stoppable.value).toBe(false)
  })
})
