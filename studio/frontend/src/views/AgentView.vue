<script setup lang="ts">
import { ref, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { notebookPath } from '../lib/libraryRoutes'
import Icon from '../components/Icon.vue'
import { useAgentChat } from '../composables/useAgentChat'
import AgentTranscript from '../components/AgentTranscript.vue'
import AgentSandboxIntro from '../components/AgentSandboxIntro.vue'

// AgentView is the full-page wizard chat (W0.5) — the same conversation engine
// as the slide-in AgentPanel (shared useAgentChat composable), given room for
// longer sessions, schema work, and report/query building.
const router = useRouter()
const { messages, sending, stoppable, send, reset, cancel, setScrollHandler, resolveApproval } = useAgentChat()

function openNotebook(id: string) {
  router.push(notebookPath(id))
}

const draft = ref('')
const streamEl = ref<HTMLElement | null>(null)

async function scrollToBottom() {
  await nextTick()
  if (streamEl.value) streamEl.value.scrollTop = streamEl.value.scrollHeight
}
setScrollHandler(scrollToBottom)

async function submit() {
  const text = draft.value
  if (!text.trim() || sending.value) return
  draft.value = ''
  await send(text)
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    submit()
  }
}
</script>

<template>
  <div class="agent-view" data-testid="agent-view">
    <div class="agent-view__head">
      <div>
        <div class="crumb"><span>Studio</span><span class="sep">/</span><span>Assistant</span></div>
        <h1 class="agent-view__title"><Icon name="bot" :size="22" /> Assistant</h1>
      </div>
      <button class="btn btn-inverted" :disabled="messages.length === 0 || sending" @click="reset">
        <Icon name="refresh" :size="14" /> New chat
      </button>
    </div>

    <div ref="streamEl" class="agent-view__stream">
      <div v-if="messages.length === 0" class="agent-view__empty">
        <Icon name="sparkle" :size="28" />
        <AgentSandboxIntro />
      </div>

      <AgentTranscript :messages="messages" @resolve="resolveApproval" @open-notebook="openNotebook" />
    </div>

    <div class="agent-view__input">
      <textarea
        v-model="draft"
        class="box agent-textarea"
        data-testid="agent-input"
        placeholder="Ask about your data, or ask for a query, chart or notebook…"
        @keydown="onKeydown"
      ></textarea>
      <div class="agent-view__inputrow">
        <span class="agent-hint">⏎ Send · ⇧⏎ Newline</span>
        <div class="agent-actions">
          <button v-if="stoppable" class="btn btn-inverted" data-testid="agent-cancel" @click="cancel">
            <Icon name="x" :size="14" /> Stop
          </button>
          <button class="btn btn-primary" :disabled="sending || !draft.trim()" data-testid="agent-send" @click="submit">
            <Icon name="send" :size="14" /> {{ sending ? 'Sending…' : 'Send' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.agent-view {
  height: 100%;
  display: flex;
  flex-direction: column;
  min-height: 0;
  background: var(--sh-paper);
}
.agent-view__head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  padding: 20px 28px 16px;
  border-bottom: 2px solid var(--sh-ink);
  background: var(--sh-surface);
}
.crumb {
  font-family: var(--sh-font-mono);
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--sh-graphite);
  display: flex;
  gap: 8px;
}
.agent-view__title {
  font-family: var(--sh-font-display);
  font-weight: 600;
  font-size: 26px;
  letter-spacing: -0.015em;
  margin: 6px 0 0;
  display: flex;
  align-items: center;
  gap: 10px;
}
.agent-view__stream {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 24px 28px;
  display: flex;
  flex-direction: column;
  gap: 18px;
  max-width: 920px;
  width: 100%;
  margin: 0 auto;
}
.agent-view__empty {
  color: var(--sh-graphite);
  font-size: 15px;
  line-height: 1.55;
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-width: 560px;
}
.agent-view__input {
  border-top: 2px solid var(--sh-ink);
  background: var(--sh-paper-2);
  padding: 14px 28px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: 920px;
  width: 100%;
  margin: 0 auto;
}
.agent-view__inputrow {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.agent-textarea {
  width: 100%;
  min-height: 68px;
  resize: vertical;
  color: var(--sh-ink);
  font-family: inherit;
  background: var(--sh-surface);
  border: 2px solid var(--sh-ink);
  border-radius: var(--sh-radius-sm);
  padding: 12px 14px;
  font-size: 14px;
}
.agent-hint {
  font-family: var(--sh-font-mono);
  font-size: 10px;
  color: var(--sh-graphite);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.agent-actions { display: flex; gap: 8px; }
</style>
