<script setup lang="ts">
// AgentTranscript — the agent conversation's messages (F4), shared by the
// slide-in AgentPanel and the full-page AgentView: who said it, the user bubble,
// the markdown answer (sanitized), "Thinking…" / "Verifying…" notes, and each
// tool step as a chip with its inline A2UI result or a link to the notebook it
// changed. Renders the message rows only; the scrolling stream, the empty state
// and the input are the host's. `compact` is the narrow drawer's sizing.
import Icon from './Icon.vue'
import ToolStepChip from './ToolStepChip.vue'
import A2UIPage from '../a2ui/A2UIPage.vue'
import { computed } from 'vue'
import { renderMarkdown, type ChatMessage, type ToolStep } from '../composables/useAgentChat'
import { decorateNumbers, type Decorated } from '../lib/numberMarks'
import { openEvidence } from '../composables/useEvidenceDrawer'

const props = defineProps<{ messages: ChatMessage[]; compact?: boolean }>()
const emit = defineEmits<{ resolve: [step: ToolStep, ok: boolean]; openNotebook: [id: string] }>()

// Each answer rendered once per change, with Go's verdict on a finished answer's
// numbers drawn over the text: a check after a number a read of this turn showed,
// a dotted mark under one that is not from a query (lib/numberMarks.ts). Nothing
// is decorated while the answer streams.
// A finished message is rendered once, keyed on its text and the identity of its
// numbers, so a streaming delta re-renders only the message that is streaming.
const rendered = new WeakMap<object, { text: string; numbers: unknown; view: Decorated }>()
function answerOf(m: ChatMessage): Decorated {
  if (m.role === 'user' || m.role === 'error' || !m.text) return { html: '', matched: 0, unmatched: 0 }
  const draw = (): Decorated => {
    const html = renderMarkdown(m.text)
    return m.streaming || !m.numbers?.length ? { html, matched: 0, unmatched: 0 } : decorateNumbers(html, m.numbers)
  }
  if (m.streaming) return draw()
  const hit = rendered.get(m)
  if (hit && hit.text === m.text && hit.numbers === m.numbers) return hit.view
  const view = draw()
  rendered.set(m, { text: m.text, numbers: m.numbers, view })
  return view
}
const answers = computed(() => props.messages.map(answerOf))

// A check opens the evidence of the read that showed the number.
function onAnswerClick(e: MouseEvent) {
  const btn = (e.target as HTMLElement | null)?.closest<HTMLElement>('button[data-receipt]')
  const id = btn?.getAttribute('data-receipt')
  if (btn && id) openEvidence({ title: id, receiptId: id, fromId: true }, btn)
}
</script>

<template>
  <div v-for="(m, i) in messages" :key="i" class="amsg" :class="{ 'amsg--compact': compact }">
    <div class="av" :class="m.role === 'user' ? 'user' : 'bot'">
      {{ m.role === 'user' ? 'You' : 'AI' }}
    </div>
    <div class="body">
      <div class="who">
        {{ m.role === 'user' ? 'You' : m.role === 'error' ? 'Assistant · error' : m.cancelled ? 'Assistant · cancelled' : (m.verifying ? 'Assistant · verifying' : m.streaming ? 'Assistant · streaming' : 'Assistant') }}
      </div>
      <div v-if="m.role === 'user'" class="user-bubble">{{ m.text }}</div>
      <p v-else-if="m.role === 'error'" class="agent-text agent-error">{{ m.text }}</p>
      <template v-else-if="m.text">
        <div class="agent-text agent-md slaw-content slaw-md-content" data-testid="agent-answer" @click="onAnswerClick" v-html="answers[i].html"></div>
        <p v-if="answers[i].matched || answers[i].unmatched" class="agent-legend" data-testid="agent-number-legend">
          <span v-if="answers[i].matched"><span class="legend-check" aria-hidden="true">&#10003;</span> matches a value in a query result</span>
          <span v-if="answers[i].unmatched"><span class="legend-dot" aria-hidden="true">n</span> not from a query</span>
        </p>
      </template>
      <p v-else-if="m.streaming && !m.cancelled" class="agent-text agent-dim">Thinking…</p>
      <p v-if="m.cancelled" class="agent-text agent-dim" data-testid="agent-cancelled">Cancelled</p>
      <p v-if="m.verifying" class="agent-text agent-dim" data-testid="agent-verifying">Verifying that answer against the data…</p>

      <template v-for="(s, si) in m.steps" :key="si">
        <ToolStepChip :step="s" :preview-len="compact ? 48 : 80" @resolve="(ok) => emit('resolve', s, ok)" />
        <A2UIPage v-if="s.page" :page="s.page" class="agent-a2ui-embed" data-testid="agent-a2ui" />
        <button
          v-if="s.notebookLink"
          class="agent-pinbtn"
          style="margin: 4px 0;"
          data-testid="agent-open-notebook"
          @click="emit('openNotebook', s.notebookLink.id)"
        ><Icon name="fileText" :size="13" /> Open notebook — {{ s.notebookLink.title }}</button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.agent-text { white-space: pre-wrap; }
.agent-md { white-space: normal; }
.agent-md :deep(p) { margin: 0 0 6px; }
.agent-md :deep(pre) {
  background: var(--slaw-code-bg);
  color: var(--slaw-code-fg);
  padding: 10px 12px;
  border-radius: var(--sh-radius-sm);
  overflow-x: auto;
  font-family: var(--sh-font-mono);
  font-size: 12.5px;
}
.amsg--compact .agent-md :deep(pre) { padding: 8px 10px; font-size: 12px; }
.agent-md :deep(code) { font-family: var(--sh-font-mono); font-size: 12.5px; }
.amsg--compact .agent-md :deep(code) { font-size: 12px; }
/* The global `.sh code` rule (schoolhouse/colors_and_type.css) gives inline code
   its own light --sh-paper-2 background + ink border. Inside a fenced block that
   collides with the dark <pre> above it: the <code> painted its own near-white
   box under near-white inherited text. Reset it like the Pages renderer's
   `.slaw-content pre code` already does, so fenced code reads light on dark. */
.agent-md :deep(pre code) {
  background: transparent;
  border: none;
  padding: 0;
  color: inherit;
}
/* Each host keeps the markdown extras it always had: tables in the page, lists in the drawer. */
.amsg:not(.amsg--compact) .agent-md :deep(table) { border-collapse: collapse; }
.amsg:not(.amsg--compact) .agent-md :deep(th), .amsg:not(.amsg--compact) .agent-md :deep(td) { border: 1px solid var(--sh-line); padding: 4px 8px; }
.amsg--compact .agent-md :deep(ul), .amsg--compact .agent-md :deep(ol) { margin: 4px 0; padding-left: 20px; }
.agent-md :deep(.num-none) {
  text-decoration: underline dotted;
  text-decoration-color: var(--sh-graphite);
  text-underline-offset: 3px;
  cursor: help;
}
.agent-md :deep(.num-check) {
  display: inline-block;
  margin: 0 2px 0 3px;
  padding: 0 4px;
  border: 1px solid var(--sh-success);
  border-radius: var(--sh-radius-sm);
  background: transparent;
  color: var(--sh-success);
  font: inherit;
  font-size: 0.8em;
  font-weight: 700;
  line-height: 1.3;
  cursor: pointer;
}
.agent-md :deep(.num-check:hover) { background: color-mix(in srgb, var(--sh-success) 12%, transparent); }
.agent-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 2px 14px;
  margin: 0 0 6px;
  font-size: 10.5px;
  color: var(--sh-graphite);
}
.legend-check { color: var(--sh-success); font-weight: 700; }
.legend-dot { text-decoration: underline dotted; text-underline-offset: 3px; }
.agent-error { color: var(--sh-error); }
.agent-dim { color: var(--sh-graphite); font-style: italic; }
/* present_data's inline A2UI result (#67) — a compact card in the transcript,
   distinct from the plain-text agent bubble above it. */
.agent-a2ui-embed {
  border: var(--sh-border-thin);
  border-radius: var(--sh-radius);
  background: var(--sh-surface);
  padding: 10px 12px;
  margin: 4px 0 8px;
}
.agent-pinbtn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border: 1.5px solid var(--sh-ink);
  border-radius: var(--sh-radius-sm);
  background: var(--sh-surface);
  cursor: pointer;
  font: inherit;
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
</style>
