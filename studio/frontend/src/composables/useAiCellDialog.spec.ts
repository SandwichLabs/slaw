// Tests for useAiCellDialog (#66 part B): the AI-cell dialog state machine,
// apply rules (type mismatch never destroys a cell; a stale task never
// applies), and the follow-up cap. Mirrors useAgentChat.spec.ts's pattern:
// mock EventsOn to capture the registered handler, mock the AgentTask
// binding on window.go.main.App.
import { describe, it, expect, beforeEach, vi } from 'vitest'

let taskHandler: ((p: any) => void) | null = null
vi.mock('../wailsjs/runtime/runtime', () => ({
  EventsOn: (_name: string, cb: (p: any) => void) => {
    taskHandler = cb
    return () => { taskHandler = null }
  },
}))

import { useAiCellDialog, decideApplyAction, capFollowUp, type FollowUpTurn } from './useAiCellDialog'

function setBinding(impl: (req: any) => Promise<any>) {
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: { AgentTask: vi.fn(impl), CancelAgentTask: vi.fn() } } }
  return (window as any).go.main.App.AgentTask
}

function makeHost(
  initialCells: Array<{ id: string; type: 'sql' | 'markdown' | 'a2ui'; source?: string; view?: string; label?: string }> = [],
  opts: { tables?: string[]; notebookSummary?: string[] } = {},
) {
  const cells = { value: initialCells as any[] }
  const inserted: any[] = []
  const replaced: Array<{ id: string; ec: any }> = []
  return {
    cells,
    insertCell: vi.fn((ec: any) => { inserted.push(ec); cells.value.push({ id: 'new_' + inserted.length, type: ec.type }) }),
    replaceCell: vi.fn((id: string, ec: any) => { replaced.push({ id, ec }) }),
    snapshotCell: vi.fn((id: string) => {
      const c = cells.value.find((c: any) => c.id === id)
      return c ? { source: c.source, label: c.label, view: c.view } : null
    }),
    getTables: vi.fn(async () => opts.tables ?? []),
    getNotebookSummary: vi.fn(() => opts.notebookSummary ?? []),
    inserted,
    replaced,
  }
}

describe('decideApplyAction (C2: type mismatch never destroys a cell)', () => {
  it('create mode always inserts', () => {
    expect(decideApplyAction('create', null, 'sql')).toEqual({ kind: 'insert' })
    expect(decideApplyAction('create', 'sql', 'sql')).toEqual({ kind: 'insert' })
  })

  it('edit mode replaces when types match', () => {
    expect(decideApplyAction('edit', 'sql', 'sql')).toEqual({ kind: 'replace' })
    expect(decideApplyAction('edit', 'markdown', 'markdown')).toEqual({ kind: 'replace' })
  })

  it('edit mode inserts beside on a type mismatch, never replaces', () => {
    expect(decideApplyAction('edit', 'sql', 'markdown')).toEqual({ kind: 'insert' })
    expect(decideApplyAction('edit', 'markdown', 'sql')).toEqual({ kind: 'insert' })
    expect(decideApplyAction('edit', 'a2ui', 'sql')).toEqual({ kind: 'insert' })
  })

  it('a vanished target (null type) inserts rather than throwing', () => {
    expect(decideApplyAction('edit', null, 'sql')).toEqual({ kind: 'insert' })
  })
})

describe('capFollowUp (12-turn cap, keeping the first)', () => {
  function turns(n: number): FollowUpTurn[] {
    return Array.from({ length: n }, (_, i) => ({ role: i % 2 === 0 ? 'user' : 'assistant', text: `t${i}` }))
  }

  it('is a no-op under the cap', () => {
    const t = turns(10)
    expect(capFollowUp(t)).toEqual(t)
  })

  it('keeps the first turn and the most recent ones once over the cap', () => {
    const t = turns(20)
    const capped = capFollowUp(t)
    expect(capped).toHaveLength(12)
    expect(capped[0]).toEqual(t[0])
    expect(capped[capped.length - 1]).toEqual(t[19])
    // Nothing from the dropped middle range should survive.
    expect(capped.find((x) => x.text === 't5')).toBeUndefined()
  })
})

describe('useAiCellDialog state machine', () => {
  beforeEach(() => { taskHandler = null })

  it('prompt -> working -> proposal on a "cell" outcome, tracking checked steps live', async () => {
    setBinding(async (req) => {
      taskHandler!({ taskId: req.taskId, kind: 'tool', id: '1', tool: 'introspect_schema' })
      taskHandler!({ taskId: req.taskId, kind: 'tool_done', id: '1', tool: 'introspect_schema', isError: false })
      taskHandler!({ taskId: req.taskId, kind: 'tool_done', id: '2', tool: 'write_cell', isError: false })
      return { outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 1', view: 'stat', label: 'One' }, explanation: 'it is one', checked: ['introspect_schema'] }
    })
    const host = makeHost()
    const dialog = useAiCellDialog(host)

    dialog.openCreate()
    expect(dialog.state.dialogState).toBe('prompt')

    const p = dialog.submit('give me a stat of 1')
    expect(dialog.state.dialogState).toBe('working')
    await p
    expect(dialog.state.dialogState).toBe('proposal')
    expect(dialog.state.envelope?.cell?.sql).toBe('SELECT 1')
    expect(dialog.state.checked).toContain('introspect_schema')
    // write_cell itself must never show up in the "checked" list.
    expect(dialog.state.checked).not.toContain('write_cell')
  })

  it('needs_info outcome moves to the needs_info state', async () => {
    setBinding(async () => ({ outcome: 'needs_info', questions: ['which table?'], notes: 'no region column' }))
    const dialog = useAiCellDialog(makeHost())
    dialog.openCreate()
    await dialog.submit('churn by customer segment')
    expect(dialog.state.dialogState).toBe('needs_info')
    expect(dialog.state.envelope?.questions).toEqual(['which table?'])
  })

  it('every follow-up of one dialog sends the dialog\'s one taskId (one agent session), a reopened dialog a new one', async () => {
    const calls: string[] = []
    setBinding(async (req) => {
      calls.push(req.taskId)
      return { outcome: 'needs_info', questions: ['which table?'] }
    })
    const dialog = useAiCellDialog(makeHost())
    dialog.openCreate()
    const id = dialog.state.taskId
    await dialog.submit('first ask')
    await dialog.submit('the orders table') // answer and retry
    await dialog.submit('and only open ones') // refine
    expect(calls).toEqual([id, id, id])
    expect(dialog.state.taskId).toBe(id)

    dialog.openCreate()
    await dialog.submit('another dialog')
    expect(calls[3]).not.toBe(id)
  })

  it('unresolved outcome moves to the unresolved state', async () => {
    setBinding(async () => ({ outcome: 'unresolved', notes: 'ran out of steps' }))
    const dialog = useAiCellDialog(makeHost())
    dialog.openCreate()
    await dialog.submit('something impossible')
    expect(dialog.state.dialogState).toBe('unresolved')
  })

  it('a binding rejection also resolves to unresolved, not an unhandled throw', async () => {
    setBinding(async () => { throw new Error('LLM unreachable') })
    const dialog = useAiCellDialog(makeHost())
    dialog.openCreate()
    await dialog.submit('anything')
    expect(dialog.state.dialogState).toBe('unresolved')
    expect(dialog.state.error).toContain('LLM unreachable')
  })

  it('apply() inserts a new cell in create mode', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 1' } }))
    const host = makeHost()
    const dialog = useAiCellDialog(host)
    dialog.openCreate()
    await dialog.submit('x')
    const ok = dialog.apply()
    expect(ok).toBe(true)
    expect(host.insertCell).toHaveBeenCalledTimes(1)
    expect(host.replaceCell).not.toHaveBeenCalled()
    expect(dialog.state.dialogState).toBe('closed') // apply resets the dialog
  })

  it('apply() replaces the target cell in edit mode when types match', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 2' } }))
    const host = makeHost([{ id: 'c1', type: 'sql', source: 'SELECT 1' }])
    const dialog = useAiCellDialog(host)
    dialog.openEdit({ id: 'c1', type: 'sql' })
    await dialog.submit('make it select 2')
    dialog.apply()
    expect(host.replaceCell).toHaveBeenCalledWith('c1', expect.objectContaining({ sql: 'SELECT 2' }))
    expect(host.insertCell).not.toHaveBeenCalled()
  })

  it('apply() inserts beside instead of replacing on a type mismatch — never destroys the cell', async () => {
    // A markdown cell being edited, but the agent (incorrectly, or per a
    // request it should have declined) proposes a sql cell.
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 1' } }))
    const host = makeHost([{ id: 'c1', type: 'markdown', source: '# notes' }])
    const dialog = useAiCellDialog(host)
    dialog.openEdit({ id: 'c1', type: 'markdown' })
    await dialog.submit('turn this into a query')
    dialog.apply()
    expect(host.insertCell).toHaveBeenCalledTimes(1)
    expect(host.replaceCell).not.toHaveBeenCalled()
    // The original markdown cell must still be there, untouched.
    expect(host.cells.value.find((c: any) => c.id === 'c1')).toBeTruthy()
  })

  it('a stale task (dialog reopened while a call was in flight) never applies', async () => {
    let resolveCall: (v: any) => void = () => {}
    setBinding(() => new Promise((resolve) => { resolveCall = resolve }))
    const host = makeHost()
    const dialog = useAiCellDialog(host)

    dialog.openCreate()
    const firstTaskId = dialog.state.taskId
    const inFlight = dialog.submit('first request')

    // The dialog is reopened (e.g. the user closed it and opened it again
    // for a different cell) before the first call resolves — a fresh taskId
    // is minted.
    dialog.openCreate()
    expect(dialog.state.taskId).not.toBe(firstTaskId)

    // The first call finally resolves — its result must be discarded, not
    // applied to the now-different dialog session.
    resolveCall({ outcome: 'cell', cell: { type: 'sql', sql: 'STALE' } })
    await inFlight

    expect(dialog.state.envelope).toBeNull()
    expect(dialog.state.dialogState).toBe('prompt')
    expect(host.insertCell).not.toHaveBeenCalled()
  })

  it('stale "agent:task" events (wrong taskId) are ignored', async () => {
    setBinding(async (req) => {
      // An event from some OTHER, unrelated taskId must never populate this
      // dialog's checked list.
      taskHandler!({ taskId: 'someone-elses-task', kind: 'tool_done', tool: 'introspect_schema', isError: false })
      taskHandler!({ taskId: req.taskId, kind: 'tool_done', tool: 'describe_table', isError: false })
      return { outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 1' } }
    })
    const dialog = useAiCellDialog(makeHost())
    dialog.openCreate()
    await dialog.submit('x')
    expect(dialog.state.checked).toEqual(['describe_table'])
  })

  it('close() resets to closed and drops any envelope', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 1' } }))
    const dialog = useAiCellDialog(makeHost())
    dialog.openCreate()
    await dialog.submit('x')
    expect(dialog.state.dialogState).toBe('proposal')
    dialog.close()
    expect(dialog.state.dialogState).toBe('closed')
    expect(dialog.state.envelope).toBeNull()
  })

  it('follow-up turns accumulate and are sent on the next submit', async () => {
    const seen: any[] = []
    setBinding(async (req) => {
      seen.push(req.followUp)
      return { outcome: 'needs_info', questions: ['which region column?'] }
    })
    const dialog = useAiCellDialog(makeHost())
    dialog.openCreate()
    await dialog.submit('revenue by segment')
    expect(seen[0]).toEqual([])
    await dialog.submit('use the region column') // ANSWER, same taskId
    expect(seen[1].length).toBeGreaterThan(0)
    expect(seen[1][0].text).toBe('revenue by segment')
  })

  // --- review fix, finding 6: apply must not overwrite a concurrent edit ---

  it('apply() inserts beside (not replace) when the target cell was edited while the task ran', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 2' } }))
    const host = makeHost([{ id: 'c1', type: 'sql', source: 'SELECT 1', label: 'orig', view: 'table' }])
    const dialog = useAiCellDialog(host)
    dialog.openEdit({ id: 'c1', type: 'sql' })
    await dialog.submit('make it select 2')
    // The user keeps editing the live cell while (in a real app) the task
    // was running — simulate that by mutating the host cell now, before
    // apply().
    host.cells.value[0].source = 'SELECT 1 -- user kept typing'

    const preview = dialog.previewApply()
    expect(preview?.cellDrifted).toBe(true)

    dialog.apply()
    expect(host.insertCell).toHaveBeenCalledTimes(1)
    expect(host.replaceCell).not.toHaveBeenCalled()
    expect(host.cells.value.find((c: any) => c.id === 'c1').source).toBe('SELECT 1 -- user kept typing')
    expect(dialog.state.applyNote).toContain('changed while this was running')
  })

  it('apply() still replaces when the target cell is unchanged', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 2' } }))
    const host = makeHost([{ id: 'c1', type: 'sql', source: 'SELECT 1', label: 'orig', view: 'table' }])
    const dialog = useAiCellDialog(host)
    dialog.openEdit({ id: 'c1', type: 'sql' })
    await dialog.submit('make it select 2')
    dialog.apply()
    expect(host.replaceCell).toHaveBeenCalledWith('c1', expect.objectContaining({ sql: 'SELECT 2' }))
    expect(host.insertCell).not.toHaveBeenCalled()
  })

  it('previewApply reports typeMismatch for a C2 case before apply is clicked', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 1' } }))
    const host = makeHost([{ id: 'c1', type: 'markdown', source: '# notes' }])
    const dialog = useAiCellDialog(host)
    dialog.openEdit({ id: 'c1', type: 'markdown' })
    await dialog.submit('turn this into a query')
    const preview = dialog.previewApply()
    expect(preview?.typeMismatch).toBe(true)
    expect(preview?.action).toEqual({ kind: 'insert' })
  })

  // --- review fix, finding 7: tables/notebookSummary must actually be sent ---

  it('sends tables (loaded at open) and notebookSummary (computed at submit) with the request', async () => {
    const seen: any[] = []
    setBinding(async (req) => {
      seen.push(req)
      return { outcome: 'needs_info', questions: ['?'] }
    })
    const host = makeHost([{ id: 'c1', type: 'sql', source: 'SELECT 1', label: 'Revenue' }], { tables: ['sales', 'customers'] })
    ;(host.getNotebookSummary as any).mockReturnValue(['Revenue'])
    const dialog = useAiCellDialog(host)
    dialog.openCreate()
    await Promise.resolve() // let loadTables() resolve
    await Promise.resolve()
    expect(dialog.state.tables).toEqual(['sales', 'customers'])

    await dialog.submit('x')
    expect(seen[0].tables).toEqual(['sales', 'customers'])
    expect(seen[0].notebookSummary).toEqual(['Revenue'])
  })

  // --- coordinator follow-up #1: insert beside the target, not at the end ---

  it('apply() passes the live target id as afterCellId on the drift path, so the host can insert right after it', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 2' } }))
    const host = makeHost([
      { id: 'c0', type: 'sql', source: 'unrelated' },
      { id: 'c1', type: 'sql', source: 'SELECT 1', label: 'orig', view: 'table' },
      { id: 'c2', type: 'sql', source: 'also unrelated' },
    ])
    const dialog = useAiCellDialog(host)
    dialog.openEdit({ id: 'c1', type: 'sql' })
    await dialog.submit('make it select 2')
    host.cells.value[1].source = 'SELECT 1 -- drifted'

    dialog.apply()
    expect(host.insertCell).toHaveBeenCalledWith(expect.objectContaining({ sql: 'SELECT 2' }), 'c1')
  })

  it('apply() passes the live target id as afterCellId on the C2 type-mismatch path', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 1' } }))
    const host = makeHost([{ id: 'c1', type: 'markdown', source: '# notes' }])
    const dialog = useAiCellDialog(host)
    dialog.openEdit({ id: 'c1', type: 'markdown' })
    await dialog.submit('turn this into a query')
    dialog.apply()
    expect(host.insertCell).toHaveBeenCalledWith(expect.objectContaining({ sql: 'SELECT 1' }), 'c1')
  })

  it('apply() passes no afterCellId in create mode (nothing to be "below")', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 1' } }))
    const host = makeHost([{ id: 'c1', type: 'sql', source: 'x' }])
    const dialog = useAiCellDialog(host)
    dialog.openCreate()
    await dialog.submit('x')
    dialog.apply()
    expect(host.insertCell).toHaveBeenCalledWith(expect.objectContaining({ sql: 'SELECT 1' }), undefined)
  })
})

describe('useAiCellDialog keeps the proposal\'s connection (#117)', () => {
  it('sends the edited cell\'s connection to the task and hands the proposal\'s connection to the host unchanged', async () => {
    const task = setBinding(async () => ({
      outcome: 'cell',
      cell: { type: 'sql', sql: 'SELECT * FROM orders', connection: 'prod_pg' },
      explanation: 'ok',
    }))
    const host = makeHost([{ id: 'c1', type: 'sql', source: 'SELECT 1', connection: 'prod_pg' } as any])
    const d = useAiCellDialog(host as any)
    d.openEdit({ id: 'c1', type: 'sql' })
    await d.submit('show orders')
    expect(task).toHaveBeenCalledWith(expect.objectContaining({ cell: expect.objectContaining({ connection: 'prod_pg' }) }))
    expect(d.apply()).toBe(true)
    expect(host.replaceCell).toHaveBeenCalledWith('c1', expect.objectContaining({ connection: 'prod_pg' }))
  })

  it('treats a changed connection as drift: the cell is not silently overwritten', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 2' }, explanation: '' }))
    const host = makeHost([{ id: 'c1', type: 'sql', source: 'SELECT 1', connection: 'a' } as any])
    host.snapshotCell = vi.fn((id: string) => {
      const c: any = host.cells.value.find((x: any) => x.id === id)
      return { source: c.source, label: c.label, view: c.view, connection: c.connection }
    })
    const d = useAiCellDialog(host as any)
    d.openEdit({ id: 'c1', type: 'sql' })
    await d.submit('x')
    ;(host.cells.value[0] as any).connection = 'b'
    expect(d.previewApply()?.cellDrifted).toBe(true)
  })
})

describe('Apply keeps the original cell\'s connection when the proposal has none (#117)', () => {
  it('fills it in on replace, not on insert', async () => {
    setBinding(async () => ({ outcome: 'cell', cell: { type: 'sql', sql: 'SELECT 2' }, explanation: '' }))
    const host = makeHost([{ id: 'c1', type: 'sql', source: 'SELECT 1', connection: 'prod_pg' } as any])
    const d = useAiCellDialog(host as any)
    d.openEdit({ id: 'c1', type: 'sql' })
    await d.submit('x')
    d.apply()
    expect(host.replaceCell).toHaveBeenCalledWith('c1', expect.objectContaining({ sql: 'SELECT 2', connection: 'prod_pg' }))
  })
})

describe('useAiCellDialog openCreate(description)', () => {
  it('carries the description as a prefill, runs nothing, and clears it on close or a plain open', () => {
    const askTask = setBinding(async () => ({ outcome: 'unresolved' }))
    const d = useAiCellDialog(makeHost())
    d.openCreate('revenue by region')
    expect(d.state.dialogState).toBe('prompt')
    expect(d.state.prefill).toBe('revenue by region')
    expect(askTask).not.toHaveBeenCalled()
    d.close()
    expect(d.state.prefill).toBe('')
    d.openCreate('x')
    d.openCreate()
    expect(d.state.prefill).toBe('')
  })
})
