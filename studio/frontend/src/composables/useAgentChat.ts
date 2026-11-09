// useAgentChat — shared conversational-agent state used by both the slide-in
// AgentPanel and the dedicated Agent page (W0.5).
//
// It drives the streaming AgentChatStream binding: each send emits a turn-scoped
// "agent:stream" Wails event sequence (delta | tool | tool_done | final | error)
// that we render live (typing text + tool-use chips that go running -> done),
// while the binding's resolved result is treated as the authoritative final.
// Conversation history is threaded back to the backend for multi-turn memory.
import { ref, computed, getCurrentScope, onScopeDispose } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import type { A2UIPageT } from '../a2ui/types'
import { renderSafeMarkdown } from '../lib/markdown'
import { errorText } from '../lib/errors'
import { App } from '../lib/bindings'
import { hasWailsRuntime } from '../lib/wails'
import type { NumberMark } from '../lib/numberMarks'
import { rememberRun, paramText } from './useReceipt'

// PendingApproval mirrors internal/agents.ApprovalRequest (#79), plus a
// client-only `state` this composable drives as the user answers the card
// (or it times out/gets cancelled) — see resolveApproval.
export interface PendingApproval {
  id: string
  summary: string
  sql?: string
  where?: string
  destructive: boolean
  // Connections names the changed connections that are not local files
  // (Postgres, MotherDuck, ...): the card says where the change goes (#117).
  connections?: string[]
  // rows is the policy's estimate of rows affected (-1 or absent: unknown).
  rows?: number
  // 'gone' is local-only: ResolveAgentApproval itself failed (e.g. the
  // request already resolved/expired server-side before the click landed)
  // — see resolveApproval. Every other state mirrors agent:approval_done's
  // outcome.
  state: 'pending' | 'sending' | 'approved' | 'declined' | 'expired' | 'cancelled' | 'gone'
  // error is a plain-language reason the card could not be answered.
  error?: string
}

// errorHint says what to check for a failed agent turn, from the provider that
// is configured now (read at the moment of the error, so a change in Settings
// is already reflected). A local server or Ollama is a process that may not be
// running; a cloud provider usually needs its key or model checked.
async function errorHint(): Promise<string> {
  try {
    const s = await App.GetLLMSettings()
    if (s.provider === 'local' || s.provider === 'ollama') return `Is the model server at ${s.endpoint} running?`
    return 'Check the API key and model in Settings > App.'
  } catch {
    return 'Check the model settings in Settings > App.'
  }
}

// The Go side already prefixes "agent error: "; the panel adds its own.
function stripPrefix(t: string | undefined): string { return String(t ?? '').replace(/^agent error:\s*/i, '') }

async function addHint(msg: { role: string; text: string }) {
  const hint = await errorHint()
  if (msg.role === 'error' && !msg.text.includes(hint)) msg.text += ' ' + hint
}

export interface ToolStep {
  id?: string
  tool: string
  input?: string
  output?: string
  status: 'running' | 'done' | 'error'
  // page holds the validated A2UI page a successful present_data call
  // returned (#67), parsed from its JSON output, so the panel can render it
  // inline instead of the raw JSON tool-result text. Never set for a denied
  // or failed call (isError), and never set for any other tool.
  page?: A2UIPageT
  // notebookLink holds the {id, title} a successful add_query_cell/
  // create_dashboard call returned (#67 deferred part), so the panel can
  // offer an "Open notebook" link instead of leaving the user to find the
  // new/updated notebook themselves. Never set for a denied/failed call or
  // any other tool.
  notebookLink?: { id: string; title: string }
  // approval holds the pending/resolved write-approval card for this step
  // (#79), correlated to it by tool-call id (ApprovalRequest.callId ==
  // ToolStep.id). Undefined for every step that never needed approval.
  approval?: PendingApproval
  // receipt is the run this call made (run_query, run_saved_query, preview_query):
  // the receipt id and how many rows the model was shown. The chip offers
  // "Open evidence" on it.
  receipt?: ChatRun
}

// ChatRun mirrors the studio ChatRun: a read of the turn that made a receipt.
export interface ChatRun {
  callId: string
  tool: string
  receiptId: string
  rows: number
  truncated?: boolean
}

export interface ChatMessage {
  role: 'user' | 'agent' | 'error'
  text: string
  steps: ToolStep[]
  streaming?: boolean
  // verifying is set true between a "verifying" stream event (#70's
  // grounding check re-sending an ungrounded answer for one recheck pass)
  // and the next delta/final/error event, so the panel can show a
  // "Verifying…" state instead of leaving the first (about-to-be-replaced)
  // answer sitting there looking finished.
  verifying?: boolean
  // cancelled marks a turn the person (or a workspace close) stopped: its end
  // state, shown as "Cancelled" instead of an error.
  cancelled?: boolean
  // numbers is Go's verdict on each distinct number of the finished answer: it
  // matches a value one of this turn's reads showed (with that read's receipt),
  // or it is not from a query. The transcript draws them over the text.
  numbers?: NumberMark[]
}

// ApprovalRequestPayload is what "agent:approval" carries under its
// `request` key. Go's internal/agents.ApprovalRequest has a few more fields
// (Table, Mode, Rows) that exist for the Go-side summary text and MCP/debug
// logging but that nothing here renders, so they're left out of this type —
// the wire payload still carries them; they're just not worth naming twice.
interface ApprovalRequestPayload {
  id: string
  callId: string
  tool: string
  summary: string
  sql?: string
  where?: string
  destructive: boolean
  connections?: string[]
  rows?: number
}

interface ApprovalPayload {
  turnId: string
  request: ApprovalRequestPayload
}

interface ApprovalDonePayload {
  turnId: string
  id: string
  outcome: 'approved' | 'declined' | 'expired' | 'cancelled'
}

interface ReceiptPayload {
  turnId: string
  run: ChatRun
}

interface StreamPayload {
  turnId: string
  // The conversation this turn belongs to (set on every event).
  conversationId?: string
  kind: 'delta' | 'tool' | 'tool_done' | 'final' | 'error' | 'verifying' | 'cancelled'
  text?: string
  id?: string
  tool?: string
  input?: string
  output?: string
  // tool_done only: the tool call was denied or failed (Output carries the
  // error message). Without this the panel had no way to distinguish a
  // denied call from a successful one — both rendered the same "done" chip.
  isError?: boolean
}

// renderMarkdown turns agent text into sanitized HTML. This is a thin
// re-export of the ONE shared safe renderer (lib/markdown.ts) — it used to
// carry its own marked/DOMPurify call here, a second copy that had drifted
// from Skills' sanitization (weaker FORBID_TAGS, no image/link handling).
// Consumers (AgentPanel, AgentView) must render the result inside a
// `.slaw-md-content` container (see lib/markdown.ts's slawMdContentClass)
// so installSafeLinkHandling's click interception actually applies to it.
export const renderMarkdown = renderSafeMarkdown

// parsePresentDataPage parses present_data's (and propose_schema's — #68
// follow-up: there is no dedicated Schema Builder diagram screen, so a
// proposed schema renders the same inline-A2UI way as present_data)
// tool_done output (a JSON-encoded internal/a2ui.Page) into an A2UIPageT for
// inline rendering, or returns undefined for anything else — a
// denied/errored call, an unrelated tool, or output that isn't the expected
// {title, components[]} shape. The page was already validated server side
// (internal/a2ui.Validate); this is just a parse, not a re-validation.
function parsePresentDataPage(tool: string | undefined, output: string | undefined, isError: boolean | undefined): A2UIPageT | undefined {
  if ((tool !== 'present_data' && tool !== 'propose_schema') || isError || !output) return undefined
  try {
    const parsed = JSON.parse(output)
    if (parsed && typeof parsed === 'object' && typeof parsed.title === 'string' && Array.isArray(parsed.components)) {
      return parsed as A2UIPageT
    }
  } catch {
    // Not JSON, or not a page — nothing to render inline.
  }
  return undefined
}

// parseNotebookLink parses add_query_cell/create_dashboard/add_markdown_cell's tool_done
// output ({notebook_id, cell_id, title, warnings}) into the {id, title} the
// panel's "Open notebook" link needs, or undefined for anything else (a
// denied/errored call, a non-matching tool, or unparseable output).
function parseNotebookLink(tool: string | undefined, output: string | undefined, isError: boolean | undefined): { id: string; title: string } | undefined {
  if ((tool !== 'add_query_cell' && tool !== 'create_dashboard' && tool !== 'add_markdown_cell') || isError || !output) return undefined
  try {
    const parsed = JSON.parse(output)
    if (parsed && typeof parsed.notebook_id === 'string' && parsed.notebook_id) {
      return { id: parsed.notebook_id, title: typeof parsed.title === 'string' ? parsed.title : 'notebook' }
    }
  } catch {
    // Not JSON, or not this shape — nothing to link.
  }
  return undefined
}

let turnSeq = 0
function nextTurnId(): string {
  turnSeq += 1
  // Avoid Math.random/crypto dependence for determinism in tests; a per-session
  // monotonic id is enough to scope events to a turn.
  return `turn-${turnSeq}-${turnSeq * 2654435761 % 1000000}`
}

// The conversation is a module singleton (SandwichLabs/slaw#2): the slide-in
// panel and the Agent page share ONE chat, and it outlives whichever view is
// mounted. The per-turn Wails listeners live in send()'s closure, not in a
// view, so a turn keeps streaming into this state while the person is on
// another screen and returning shows it live.
const messages = ref<ChatMessage[]>([])
const sending = ref(false)
const pinnedContext = ref('')
// busyElsewhere: Go refused a send because another turn is still running that this
// page has no handle on (e.g. after a webview reload). Offers Stop with no turn id.
const busyElsewhere = ref(false)
// The conversation the next turn continues: the id Go returned with the last
// turn, sent back with this one. Go accepts only ids it issued, and "" starts a
// new conversation (New chat).
let conversationId = ''

// epoch bumps whenever the chat is wiped for a workspace change. A turn
// started in an earlier epoch still finishes on the Go side, but it must not
// touch the new workspace's state (it would otherwise unlock `sending` early).
let epoch = 0

// The in-flight turn's id and message, for cancel().
let activeTurn: { id: string; msg: ChatMessage; epoch: number } | null = null

// Hosts register a scroll callback so the view stays pinned to the latest
// message; several views may be mounted at once (panel + page).
const scrollHandlers = new Set<() => void>()
function scroll() { scrollHandlers.forEach((fn) => fn()) }

// resetAll wipes the conversation for a workspace change: the transcript, the
// pinned context and the in-flight flag. Pending approval cards go with the
// transcript (their Go-side requests are cancelled by the workspace close).
function resetAll() {
  epoch += 1
  conversationId = ''
  activeTurn = null
  messages.value = []
  pinnedContext.value = ''
  sending.value = false
  busyElsewhere.value = false
}

// A conversation belongs to the workspace it was held in. Listen once for the
// workspace events, independent of any mounted view.
let tracking = false
function startTracking() {
  if (tracking || !hasWailsRuntime()) return
  tracking = true
  EventsOn('workspace:opened', resetAll)
  EventsOn('workspace:closed', resetAll)
}

// toolInputMemory is what the run behind a chip had in memory, from the tool's
// input: the SQL, the connection and the parameter values the agent sent.
function toolInputMemory(input: string | undefined) {
  try {
    const p = JSON.parse(input ?? '')
    if (!p || typeof p !== 'object') return {}
    return {
      sql: typeof p.sql === 'string' ? p.sql : undefined,
      connection: typeof p.connection === 'string' ? p.connection : undefined,
      params: p.params && typeof p.params === 'object' ? paramText(p.params as Record<string, unknown>) : undefined,
    }
  } catch {
    return {}
  }
}

// applyRun puts a read's receipt on its chip (merged by tool-call id: the event
// can come before the chip exists) and remembers the run's own side for the drawer.
function applyRun(msg: ChatMessage, run: ChatRun) {
  if (!run || !run.callId || !run.receiptId) return
  let step = msg.steps.find((s) => s.id === run.callId)
  if (!step) {
    step = { id: run.callId, tool: run.tool, status: 'running' }
    msg.steps.push(step)
    step = msg.steps[msg.steps.length - 1]
  }
  step.receipt = run
  rememberRun(run.receiptId, toolInputMemory(step.input))
}

// markCancelled ends a turn as "Cancelled": running chips stop, and any
// approval card that was still waiting is closed (Go answers it "cancelled").
function markCancelled(msg: ChatMessage) {
  msg.cancelled = true
  msg.verifying = false
  msg.steps.forEach((st) => {
    if (st.status === 'running') st.status = 'error'
    if (st.approval && (st.approval.state === 'pending' || st.approval.state === 'sending')) st.approval.state = 'cancelled'
  })
}

export function useAgentChat() {
  startTracking()

  // setScrollHandler registers the host's callback and unregisters it when the
  // calling component's scope is disposed.
  function setScrollHandler(fn: () => void) {
    scrollHandlers.add(fn)
    if (getCurrentScope()) onScopeDispose(() => scrollHandlers.delete(fn))
  }

  function pin(context: string) { pinnedContext.value = context }
  function unpin() { pinnedContext.value = '' }
  function reset() { messages.value = []; conversationId = '' }

  async function send(text: string) {
    const msg = text.trim()
    if (!msg || sending.value) return
    sending.value = true
    busyElsewhere.value = false
    const myEpoch = epoch

    // History = the transcript so far (before this turn), for multi-turn memory.
    const history = messages.value
      .filter((m) => m.role !== 'error')
      .map((m) => ({ role: m.role === 'agent' ? 'agent' : 'user', text: m.text }))

    messages.value.push({ role: 'user', text: msg, steps: [] })
    messages.value.push({ role: 'agent', text: '', steps: [], streaming: true })
    // Grab the agent message back THROUGH the reactive array so the stream
    // handler's mutations (text deltas, tool steps) trigger reactivity. Holding
    // the raw pre-push object would update state silently — watchers (e.g. the
    // Schema Builder's propose_schema watcher) would only fire on the next
    // unrelated reactive write.
    const agentMsg = messages.value[messages.value.length - 1]
    scroll()

    const turnId = nextTurnId()
    activeTurn = { id: turnId, msg: agentMsg, epoch: myEpoch }

    // agent:approval / agent:approval_done (#79) run alongside agent:stream,
    // filtered on the same turnId. Merge-by-id (same reasoning as the 'tool'
    // case above, in reverse): the approval event can arrive BEFORE the
    // matching 'tool' stream event, so a step may not exist yet here either.
    const unsubApproval = EventsOn('agent:approval', (p: ApprovalPayload) => {
      if (!p || p.turnId !== turnId) return
      const req = p.request
      const approval: PendingApproval = {
        id: req.id, summary: req.summary, sql: req.sql, where: req.where,
        destructive: req.destructive, connections: req.connections, rows: req.rows, state: 'pending',
      }
      const step = agentMsg.steps.find((s) => s.id === req.callId)
      if (step) step.approval = approval
      else agentMsg.steps.push({ id: req.callId, tool: req.tool, status: 'running', approval })
      scroll()
    })
    const unsubApprovalDone = EventsOn('agent:approval_done', (p: ApprovalDonePayload) => {
      if (!p || p.turnId !== turnId) return
      const step = agentMsg.steps.find((s) => s.approval?.id === p.id)
      if (step?.approval) step.approval.state = p.outcome
      scroll()
    })

    const unsubReceipt = EventsOn('agent:receipt', (p: ReceiptPayload) => {
      if (!p || p.turnId !== turnId) return
      applyRun(agentMsg, p.run)
      scroll()
    })

    const unsub = EventsOn('agent:stream', (p: StreamPayload) => {
      if (!p || p.turnId !== turnId) return
      if (p.conversationId && myEpoch === epoch) conversationId = p.conversationId
      switch (p.kind) {
        case 'delta':
          agentMsg.text += p.text || ''
          break
        case 'tool': {
          // Merge by id instead of always pushing (#79): an "agent:approval"
          // event for this same call can arrive first and already have
          // created a placeholder step (see the agent:approval handler
          // below) — a plain push here would then duplicate it.
          const existing = agentMsg.steps.find((s) => s.id === p.id)
          if (existing) { existing.tool = p.tool || existing.tool; existing.input = p.input; existing.status = 'running' }
          else agentMsg.steps.push({ id: p.id, tool: p.tool || '?', input: p.input, status: 'running' })
          break
        }
        case 'tool_done': {
          const status = p.isError ? 'error' : 'done'
          const page = parsePresentDataPage(p.tool, p.output, p.isError)
          const notebookLink = parseNotebookLink(p.tool, p.output, p.isError)
          // By id alone: a cancel may already have flipped the chip, and fantasy still
          // reports the call's result afterwards.
          const step = agentMsg.steps.find((s) => s.id === p.id)
          if (step) { step.status = status; step.output = p.output; step.page = page; step.notebookLink = notebookLink }
          else agentMsg.steps.push({ id: p.id, tool: p.tool || '?', output: p.output, status, page, notebookLink })
          break
        }
        case 'final':
          if (p.text) agentMsg.text = p.text
          agentMsg.verifying = false
          break
        case 'cancelled':
          markCancelled(agentMsg)
          break
        case 'error':
          if (agentMsg.cancelled) break
          agentMsg.role = 'error'
          agentMsg.text = `Assistant error: ${stripPrefix(p.text)}.`
          void addHint(agentMsg)
          agentMsg.verifying = false
          break
        case 'verifying':
          // The agent's first answer asserted a fact with no grounding tool
          // call this turn (#70) and is being sent back once for
          // verification — the text currently shown is about to be
          // replaced by the recheck's final answer.
          agentMsg.verifying = true
          break
      }
      scroll()
    })

    try {
      const res = await App.AgentChatStream(turnId, msg, pinnedContext.value, history, conversationId)
      // Authoritative final result (covers non-streaming servers / missed events).
      if (agentMsg.cancelled) {
        // Stopped by the person: whatever streamed so far stays, nothing is overwritten.
      } else if (agentMsg.role !== 'error') {
        agentMsg.text = res?.text || agentMsg.text || '(no response)'
        if (res?.conversationId && myEpoch === epoch) conversationId = res.conversationId
        // The authoritative reads and the verdict on the answer's numbers.
        res?.runs?.forEach((r) => applyRun(agentMsg, r))
        agentMsg.numbers = res?.numbers?.length ? res.numbers : undefined
        // Mark any still-"running" chips as done now that the turn finished.
        agentMsg.steps.forEach((s) => { if (s.status === 'running') s.status = 'done' })
      }
    } catch (e: any) {
      if (agentMsg.cancelled) return
      const m = errorText(e)
      agentMsg.role = 'error'
      agentMsg.text = `Assistant error: ${stripPrefix(m)}.`
      if (/already running/i.test(m)) busyElsewhere.value = true
      else await addHint(agentMsg)
    } finally {
      if (activeTurn?.id === turnId) activeTurn = null
      agentMsg.streaming = false
      // Belt-and-suspenders (review fix, round 2): the "final"/"error" event
      // handlers above already clear verifying, but both are events that
      // can arrive (or be missed) independently of when the AgentChatStream
      // binding promise itself resolves — a "verifying" event with no
      // matching "final"/"error" ever following it (e.g. the binding
      // resolves from its OWN authoritative result before that event's
      // handler runs, or the event is dropped) left "Agent · verifying"
      // stuck on screen after the turn had actually finished. The binding
      // promise settling, one way or the other, is the true end of the
      // turn, so this is where verifying is unconditionally cleared.
      agentMsg.verifying = false
      unsub()
      unsubApproval()
      unsubApprovalDone()
      unsubReceipt()
      if (myEpoch === epoch) sending.value = false
      scroll()
    }
  }

  // cancel stops the in-flight turn: Go cancels its context (no further tool
  // runs, pending approval cards are answered "cancelled") and the transcript
  // ends the turn as "Cancelled" right away. The flag also makes the binding's
  // rejection that follows a non-error.
  async function cancel() {
    const turn = activeTurn
    if (!turn && busyElsewhere.value) {
      busyElsewhere.value = false
      try { await App.CancelAgentQuery('') } catch { /* nothing running */ }
      return
    }
    if (!turn || !sending.value) return
    markCancelled(turn.msg)
    try {
      await App.CancelAgentQuery(turn.id)
    } catch {
      // Nothing to cancel (the turn just finished): the send() result wins.
    }
  }

  // resolveApproval answers a pending card: optimistically flips it to
  // 'sending' (disabling both buttons immediately — see ApprovalCard), then
  // calls ResolveAgentApproval. The eventual real state ('approved'/
  // 'declined'/...) arrives via the "agent:approval_done" handler above; a
  // call error here means the click itself came too late — the request had
  // already expired or been cancelled server-side, or the backend simply
  // isn't reachable — and 'gone' says exactly that ("no longer waiting")
  // rather than falsely claiming the specific reason was a timeout.
  async function resolveApproval(step: ToolStep, approved: boolean) {
    if (!step.approval) return
    step.approval.state = 'sending'
    step.approval.error = undefined
    try {
      await App.ResolveAgentApproval(step.approval.id, approved)
    } catch {
      // Keep a specific 'cancelled'/'expired' that arrived first.
      if (step.approval.state === 'sending') step.approval.state = 'gone'
    }
  }

  const stoppable = computed(() => sending.value || busyElsewhere.value)

  return { messages, sending, stoppable, pinnedContext, send, pin, unpin, reset, resetAll, cancel, setScrollHandler, renderMarkdown, resolveApproval }
}
