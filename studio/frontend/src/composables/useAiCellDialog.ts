// useAiCellDialog — the AI-cell authoring dialog's state machine (#66 part
// B). Drives the AgentTask binding, which streams "agent:task" events
// (distinct from useAgentChat's "agent:stream" — see internal/llm/task.go's
// TaskEvent doc comment) and resolves with a TaskEnvelope: {outcome: "cell"
// | "needs_info" | "unresolved", ...}.
//
// States: 'closed' -> 'prompt' -> 'working' -> one of 'proposal' /
// 'needs_info' / 'unresolved'. From any of the three terminal states, a
// follow-up (REFINE / ANSWER-AND-RETRY / TRY AGAIN) goes back to 'working'
// with the SAME taskId (captured once, at open) — never a fresh one — so a
// stale result from a superseded call can be told apart from a current one.
//
// Pure, host-agnostic functions (decideApplyAction, capFollowUp) are
// exported separately so NotebookView.spec.ts-style tests can exercise the
// apply/cap rules without mounting a component or mocking Wails.
import { reactive } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { capPromptList } from '../lib/notebookMerge'
import { errorText } from '../lib/errors'
import { App } from '../lib/bindings'

type DialogState = 'closed' | 'prompt' | 'working' | 'proposal' | 'needs_info' | 'unresolved'

interface TaskCellArg {
  type: 'sql' | 'markdown'
  sql?: string
  view?: string
  label?: string
  source?: string
  // The connection the cell runs on ('' / undefined = the workspace default).
  connection?: string
}

export interface FollowUpTurn {
  role: 'user' | 'assistant'
  text: string
}

export interface EnvelopeCell {
  type: 'sql' | 'markdown'
  sql?: string
  view?: string
  label?: string
  markdown?: string
  // The connection the proposal's SQL was previewed on; Apply must keep it.
  connection?: string
}

interface TaskEnvelope {
  outcome: 'cell' | 'needs_info' | 'unresolved'
  cell?: EnvelopeCell
  explanation?: string
  checked?: string[]
  questions?: string[]
  missingData?: string[]
  notes?: string
}

// MAX_FOLLOWUP_TURNS: the follow-up thread sent as request context is capped
// at 12 turns, keeping the FIRST turn (the original ask, which every
// REFINE/ANSWER builds on) and the most recent ones — never silently
// dropping the turn that framed the whole conversation.
const MAX_FOLLOWUP_TURNS = 12

// capFollowUp trims turns to at most MAX_FOLLOWUP_TURNS entries, keeping
// turns[0] (if any) plus the most recent entries, dropping from just after
// the first. A no-op when turns already fits.
export function capFollowUp(turns: FollowUpTurn[]): FollowUpTurn[] {
  if (turns.length <= MAX_FOLLOWUP_TURNS) return turns
  const first = turns[0]
  const keepFromEnd = MAX_FOLLOWUP_TURNS - 1
  const tail = turns.slice(turns.length - keepFromEnd)
  return [first, ...tail]
}

type ApplyAction = { kind: 'insert' } | { kind: 'replace' }

// decideApplyAction is the apply-time type-safety rule (C2 in the ported
// design): in 'create' mode there is nothing to replace, always insert. In
// 'edit' mode, replace ONLY when the proposed cell's type matches the
// target cell's CURRENT type (read live, not the type captured when the
// dialog opened — the user could have changed the cell's content, though
// not its type, while the task ran); a type mismatch (or a target cell that
// no longer exists — see apply below) must never destroy the existing cell,
// so it inserts the proposal beside it instead.
export function decideApplyAction(
  mode: 'create' | 'edit',
  targetCellType: 'sql' | 'markdown' | 'a2ui' | null,
  proposedType: 'sql' | 'markdown',
): ApplyAction {
  if (mode === 'edit' && targetCellType === proposedType) return { kind: 'replace' }
  return { kind: 'insert' }
}

interface CellSnapshot {
  source: string
  label?: string
  view?: string
  connection?: string
}

/** Whether two snapshots represent the same cell content. Both null (no
 * target, or target vanished) counts as equal — nothing to compare. */
function snapshotsEqual(a: CellSnapshot | null, b: CellSnapshot | null): boolean {
  if (a === b) return true
  if (!a || !b) return false
  return a.source === b.source && a.label === b.label && a.view === b.view && (a.connection ?? '') === (b.connection ?? '')
}

interface ApplyPreview {
  action: ApplyAction
  // typeMismatch: edit mode, target exists, but the proposed type differs —
  // C2's "never destroy the cell" case, ported from ai-cell.js.
  typeMismatch: boolean
  // cellDrifted: edit mode, the target cell's source/label/view changed
  // since the task started (the user kept editing while it ran) — applying
  // would silently discard that edit, so this also forces an insert.
  cellDrifted: boolean
  targetMissing: boolean
}

interface DialogHost {
  cells: { value: Array<{ id: string; type: 'sql' | 'markdown' | 'a2ui' }> }
  // afterCellId: when apply() couldn't replace (drift or a C2 type
  // mismatch) but there WAS a live target, the new cell is inserted right
  // after that target's CURRENT position — not appended at the end — so
  // "added as a new cell below" (the applyNote wording) is literally true.
  // Undefined in create mode (no target to be "below").
  insertCell: (envelopeCell: EnvelopeCell, afterCellId?: string) => void
  replaceCell: (cellId: string, envelopeCell: EnvelopeCell) => void
  // snapshotCell/getTables/getNotebookSummary are optional so a minimal test
  // host doesn't have to implement them; useAiCellDialog degrades to "no
  // drift detection" / "no context sent" when omitted, never throws.
  snapshotCell?: (id: string) => CellSnapshot | null
  getTables?: () => Promise<string[]> | string[]
  getNotebookSummary?: () => string[]
}

let seq = 0
function nextTaskId(): string {
  seq += 1
  return `task-${seq}-${Date.now()}`
}

export function useAiCellDialog(host: DialogHost) {
  const state = reactive({
    dialogState: 'closed' as DialogState,
    mode: 'create' as 'create' | 'edit',
    targetCellId: null as string | null,
    targetCellType: null as 'sql' | 'markdown' | 'a2ui' | null,
    // targetSnapshot is captured on the FIRST submit of a dialog session
    // (review fix, finding 6) — "at submit", not at open, since the user
    // could still be editing the cell in the moment between opening the
    // dialog and actually asking for something. Held fixed for the rest of
    // the session (follow-ups don't re-snapshot) so drift is judged against
    // what the task actually started from.
    targetSnapshot: null as CellSnapshot | null,
    taskId: '',
    description: '',
    working: false,
    checked: [] as string[],
    envelope: null as TaskEnvelope | null,
    followUp: [] as FollowUpTurn[],
    error: '',
    // titleOverride lets a caller (the empty-sql-cell "generate it with AI"
    // link) show "Generate with AI" even though it's mechanically an edit-
    // mode dialog targeting that (empty) cell (review fix, finding 9).
    titleOverride: null as string | null,
    // tables is populated at open (loadTables) so the prompt state can show
    // "Loaded tables: ..." before the user even submits (review fix,
    // finding 7) — it is also what's actually sent with the request.
    tables: [] as string[],
    // prefill is the description openCreate was handed (the New dialog's "describe it"); the prompt box starts with it, nothing runs until Generate.
    prefill: '',
    applyNote: '', // set after an apply that inserted-beside due to drift; the host shows this then clears it
  })

  let unsub: (() => void) | null = null

  function stopListening() {
    unsub?.()
    unsub = null
  }

  function reset() {
    stopListening()
    state.dialogState = 'closed'
    state.taskId = ''
    state.description = ''
    state.working = false
    state.checked = []
    state.envelope = null
    state.followUp = []
    state.error = ''
    state.targetCellId = null
    state.targetCellType = null
    state.targetSnapshot = null
    state.titleOverride = null
    state.tables = []
    state.prefill = ''
  }

  async function loadTables() {
    if (!host.getTables) return
    try {
      state.tables = capPromptList(await host.getTables())
    } catch {
      state.tables = []
    }
  }

  function openCreate(description?: string) {
    reset()
    state.prefill = description ?? ''
    state.applyNote = '' // clear any note left over from a PRIOR session's apply
    state.mode = 'create'
    state.taskId = nextTaskId()
    state.dialogState = 'prompt'
    void loadTables()
  }

  function openEdit(cell: { id: string; type: 'sql' | 'markdown' | 'a2ui' }, titleOverride?: string) {
    reset()
    state.applyNote = ''
    state.mode = 'edit'
    state.targetCellId = cell.id
    state.targetCellType = cell.type
    state.taskId = nextTaskId()
    state.dialogState = 'prompt'
    state.titleOverride = titleOverride ?? null
    void loadTables()
  }

  function close() {
    // Closing cancels an in-flight turn (never leaves a background call that
    // could still race an apply after the dialog is gone) — the caller is
    // expected to also call App.CancelAgentTask(taskId)
    // before/after this; kept out of this pure-ish composable so it stays
    // testable without a Wails binding.
    reset()
  }

  function summarizeEnvelopeForFollowUp(env: TaskEnvelope): string {
    if (env.outcome === 'cell' && env.cell) {
      return env.cell.type === 'sql'
        ? `Proposed: ${env.cell.label || 'a sql cell'} — ${env.cell.sql}`
        : `Proposed a markdown cell: ${env.cell.markdown}`
    }
    if (env.outcome === 'needs_info') {
      return `Asked: ${(env.questions || []).join(' ')} ${env.notes || ''}`.trim()
    }
    return `Couldn't resolve it: ${env.notes || ''}`
  }

  // submit runs one task turn (the initial ask, or a follow-up). The
  // dispatch's own taskId (captured in a local const, from state.taskId AT
  // CALL TIME) is compared against state.taskId when the call resolves and
  // in every "agent:task" event — if the dialog was closed and reopened (a
  // fresh taskId minted) while this call was in flight, both the streamed
  // events and the final resolution are silently ignored rather than
  // mutating a dialog session that has moved on.
  async function submit(description: string) {
    const dispatchTaskId = state.taskId
    if (!dispatchTaskId) return
    state.description = description
    state.dialogState = 'working'
    state.working = true
    state.checked = []
    state.envelope = null
    state.error = ''

    // Snapshot the target ONCE, on the first submit (review fix, finding 6)
    // — not on every follow-up, so drift is judged against the content the
    // task actually started from.
    if (state.mode === 'edit' && state.targetSnapshot === null && state.targetCellId && host.snapshotCell) {
      state.targetSnapshot = host.snapshotCell(state.targetCellId)
    }

    stopListening()
    unsub = EventsOn('agent:task', (p: any) => {
      if (!p || p.taskId !== dispatchTaskId || state.taskId !== dispatchTaskId) return
      if (p.kind === 'tool_done' && !p.isError && p.tool !== 'write_cell' && p.tool !== 'need_more_info') {
        if (!state.checked.includes(p.tool)) state.checked.push(p.tool)
      }
    })

    const cellArg: TaskCellArg | undefined = state.mode === 'edit' && host.cells.value.find((c) => c.id === state.targetCellId)
      ? cellArgFromHost(host, state.targetCellId!)
      : undefined

    const notebookSummary = capPromptList(host.getNotebookSummary ? host.getNotebookSummary() : [])

    try {
      const envelope: TaskEnvelope = await App.AgentTask({
        taskId: dispatchTaskId,
        mode: state.mode,
        description,
        cell: cellArg,
        followUp: capFollowUp(state.followUp),
        tables: state.tables,
        notebookSummary,
      })
      if (state.taskId !== dispatchTaskId) return // stale — dialog moved on
      state.envelope = envelope
      state.checked = envelope.checked && envelope.checked.length ? envelope.checked : state.checked
      state.dialogState = envelope.outcome === 'cell' ? 'proposal' : envelope.outcome === 'needs_info' ? 'needs_info' : 'unresolved'
      state.working = false

      const nextFollowUp = [...state.followUp, { role: 'user' as const, text: description }, { role: 'assistant' as const, text: summarizeEnvelopeForFollowUp(envelope) }]
      state.followUp = capFollowUp(nextFollowUp)
    } catch (e: any) {
      if (state.taskId !== dispatchTaskId) return
      state.working = false
      state.dialogState = 'unresolved'
      state.error = errorText(e)
      state.envelope = { outcome: 'unresolved', notes: state.error, checked: state.checked }
    } finally {
      if (state.taskId === dispatchTaskId) stopListening()
    }
  }

  // previewApply computes what apply() WOULD do right now, without doing
  // it — used by the dialog to label the button ("Apply" vs "Insert as new
  // cell") and show the C2/drift explanation before the user clicks
  // anything. Returns null when there's nothing to preview (wrong state).
  function previewApply(): ApplyPreview | null {
    if (state.dialogState !== 'proposal' || !state.envelope?.cell) return null
    const proposed = state.envelope.cell
    const liveTarget = state.targetCellId ? host.cells.value.find((c) => c.id === state.targetCellId) : null
    const action = decideApplyAction(state.mode, liveTarget ? liveTarget.type : null, proposed.type)
    const currentSnapshot = state.mode === 'edit' && state.targetCellId && host.snapshotCell ? host.snapshotCell(state.targetCellId) : null
    const cellDrifted = state.mode === 'edit' && !!liveTarget && state.targetSnapshot !== null && !snapshotsEqual(state.targetSnapshot, currentSnapshot)
    return {
      action,
      typeMismatch: state.mode === 'edit' && !!liveTarget && action.kind === 'insert',
      cellDrifted,
      targetMissing: state.mode === 'edit' && !liveTarget,
    }
  }

  // apply commits the current envelope's cell into the notebook, per
  // decideApplyAction — PLUS the drift check (review fix, finding 6): if the
  // target cell's content changed since the task started, insert beside
  // instead of overwriting the user's concurrent edit, and record a note
  // the dialog shows before closing. Returns false (no-op) if there is
  // nothing to apply (wrong dialog state) or the dialog has gone stale
  // relative to expectedTaskId (the id the CALLER captured when it decided
  // to show an Apply button — belt-and-suspenders alongside submit's own
  // guard).
  function apply(expectedTaskId?: string): boolean {
    if (state.dialogState !== 'proposal' || !state.envelope?.cell) return false
    if (expectedTaskId && expectedTaskId !== state.taskId) return false

    const proposed = state.envelope.cell
    const liveTarget = state.targetCellId ? host.cells.value.find((c) => c.id === state.targetCellId) : null
    const preview = previewApply()
    const action = preview?.action ?? decideApplyAction(state.mode, liveTarget ? liveTarget.type : null, proposed.type)

    let note = ''
    if (action.kind === 'replace' && liveTarget && !preview?.cellDrifted) {
      // The proposal replaces this cell in place: when the model named no
      // connection, the cell keeps the one it had (#117).
      const keep = (liveTarget as { connection?: string }).connection
      host.replaceCell(liveTarget.id, proposed.type === 'sql' && !proposed.connection && keep ? { ...proposed, connection: keep } : proposed)
    } else {
      // Coordinator follow-up #1: when there IS a live target (the drift or
      // C2 type-mismatch path), insert right after it — never append at the
      // end — so "added as a new cell below" is literally true. No live
      // target (create mode, or the target vanished entirely) has nothing
      // to be "below", so it appends (afterCellId undefined).
      host.insertCell(proposed, liveTarget ? liveTarget.id : undefined)
      if (preview?.cellDrifted) note = 'The cell changed while this was running, so this was added as a new cell below.'
      else if (preview?.typeMismatch) note = 'The proposed cell is a different type from the one you were editing, so it was added as a new cell instead.'
    }
    state.applyNote = note
    reset()
    return true
  }

  return { state, openCreate, openEdit, close, submit, apply, previewApply, reset }
}

function cellArgFromHost(host: DialogHost, id: string): TaskCellArg | undefined {
  const cell: any = host.cells.value.find((c) => c.id === id)
  if (!cell) return undefined
  if (cell.type === 'sql') return { type: 'sql', sql: cell.source, view: cell.view, label: cell.label, ...(cell.connection ? { connection: cell.connection } : {}) }
  if (cell.type === 'markdown') return { type: 'markdown', source: cell.source }
  return undefined
}
