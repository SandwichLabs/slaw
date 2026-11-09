<script setup lang="ts">
import { App } from '../lib/bindings'
// Settings > Workspace (SPEC 09 A): what belongs to the folder and travels with
// git (name, default connection, the team's agent limit), what is just yours
// (your own agent choice), and Studio's MCP switch for Claude and other tools.
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import Icon from './Icon.vue'
import AgentSettings from './AgentSettings.vue'
import ArtifactViewDecisions from './ArtifactViewDecisions.vue'
import ReceiptsSection from './ReceiptsSection.vue'
import { workspaceInfo, isReadOnly, refreshWorkspaceInfo } from '../composables/useWorkspace'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { errorText, settingsError } from '../lib/errors'
import { hasWailsRuntime } from '../lib/wails'
import { dotenv, dotenvBusy, dotenvError, allowDotEnv, denyDotEnv, revokeDotEnv, refreshDotEnv } from '../composables/useDotEnv'

const info = computed(() => workspaceInfo.value)

const dotenvLabel: Record<string, string> = {
  none: 'No .env file',
  undecided: 'Not decided yet',
  allowed: 'Allowed',
  changed: 'Changed since allowed',
  denied: 'Denied',
}

// ---- This workspace ----
const name = ref('')
const defaultConnection = ref('')
const saving = ref(false)
const message = ref('')
const stale = ref(false)
watch(info, (i) => {
  if (!i) return
  name.value = i.name
  defaultConnection.value = i.defaultConnection
}, { immediate: true })

const changed = computed(() => !!info.value && (name.value.trim() !== info.value.name || defaultConnection.value !== info.value.defaultConnection))
const defaultChanged = computed(() => !!info.value && defaultConnection.value !== info.value.defaultConnection)

async function save() {
  const i = info.value
  if (!i) return
  saving.value = true
  message.value = ''
  stale.value = false
  try {
    workspaceInfo.value = await App.SetWorkspaceSettings({
      name: name.value.trim(),
      defaultConnection: defaultConnection.value,
      teamSandbox: i.teamSandboxSet ? i.team.sandbox : '',
      teamAskBeforeChanges: i.team.askBeforeWrites,
      sandbox: i.local.sandbox,
      askBeforeChanges: i.local.askBeforeWrites,
      loadedTeamSandbox: i.teamSandboxSet ? i.team.sandbox : '',
      loadedTeamAsk: i.team.askBeforeWrites,
    })
    message.value = defaultChanged.value ? 'Saved. The default connection applies the next time the workspace opens.' : 'Saved'
    setTimeout(() => { message.value = '' }, 4000)
  } catch (e: unknown) {
    const se = settingsError(e)
    message.value = 'Error: ' + se.text
    stale.value = se.stale
  }
  saving.value = false
}

// ---- MCP ----
const mcp = ref<MCPStatus | null>(null)
const mcpBusy = ref(false)
const copied = ref('')

async function loadMCP() {
  try { mcp.value = await App.GetMCPStatus() } catch { mcp.value = null }
}

async function toggleMCP() {
  mcpBusy.value = true
  try {
    if (mcp.value?.running) await App.StopMCP()
    else await App.StartMCP()
    await loadMCP()
  } catch (e: unknown) {
    message.value = 'Error: ' + errorText(e)
    setTimeout(() => { message.value = '' }, 4000)
  }
  mcpBusy.value = false
}

async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = what
    setTimeout(() => { if (copied.value === what) copied.value = '' }, 2000)
  } catch { /* clipboard unavailable */ }
}

async function copyConfig(kind: 'desktop' | 'code') {
  try {
    const cfg = await App.GetClaudeConfig()
    await copy(kind === 'desktop' ? cfg.desktop : cfg.code, kind)
  } catch (e: unknown) {
    message.value = 'Error: ' + errorText(e)
    setTimeout(() => { message.value = '' }, 4000)
  }
}

let unsub: (() => void) | null = null
onMounted(() => {
  void loadMCP()
  void refreshDotEnv()
  if (hasWailsRuntime()) unsub = EventsOn('workspace:changed', loadMCP)
})
onUnmounted(() => unsub?.())
</script>

<template>
  <div v-if="info" id="settings-panel-workspace" role="tabpanel" aria-labelledby="settings-tab-workspace" class="col" style="max-width: 52rem; gap: var(--sh-space-6);">
    <!-- This workspace -->
    <div>
      <div class="sec-title"><h2 style="font-size: 22px;">This workspace</h2><span class="label">Saved in slaw.yaml</span></div>
      <div class="card card-pad col" style="gap: var(--sh-space-4);">
        <div>
          <label class="field-label" for="ws-name">Name</label>
          <input id="ws-name" v-model="name" class="field-input" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" data-testid="ws-name-input" />
        </div>
        <div>
          <label class="field-label" for="ws-folder">Folder</label>
          <input id="ws-folder" class="field-input" :value="info.root" readonly />
        </div>
        <div>
          <label class="field-label" for="ws-default">Default connection</label>
          <select id="ws-default" v-model="defaultConnection" class="field-input" aria-describedby="ws-default-h" data-testid="ws-default-select">
            <option v-for="c in info.connections" :key="c.name" :value="c.name">{{ c.name }}</option>
          </select>
          <div id="ws-default-h" class="help">Notebook cells and queries with no connection chosen run here. <code>local</code> is the safest default.</div>
        </div>
        <div class="row">
          <button class="btn btn-primary" type="button" :disabled="saving || !changed" data-testid="ws-save" @click="save">
            <Icon name="save" :size="16" /> {{ saving ? 'Saving…' : 'Save settings' }}
          </button>
          <span v-if="message" class="sh-body" role="status" :style="{ fontWeight: 600, color: message.startsWith('Error') ? 'var(--sh-error)' : 'var(--sh-teal-lo)' }">{{ message }}</span>
          <button v-if="stale" type="button" class="btn btn-sm" data-testid="reload-settings-ws" @click="refreshWorkspaceInfo().then(() => { message = ''; stale = false })">Reload settings</button>
        </div>
      </div>
    </div>

    <!-- The workspace's .env (#168) -->
    <div v-if="dotenv" data-testid="dotenv-section">
      <div class="sec-title"><h2 style="font-size: 22px;">.env file</h2><span class="label">Only yours, never shared</span></div>
      <div class="card card-pad col" style="gap: var(--sh-space-3);">
        <div class="row" style="gap: var(--sh-space-3); flex-wrap: wrap;">
          <span class="sh-body" style="font-weight: 600;" data-testid="dotenv-state" role="status">{{ dotenvLabel[dotenv.state] }}</span>
          <span v-if="dotenv.state === 'allowed' && dotenv.loaded" class="help">Loaded for this workspace.</span>
        </div>
        <div v-if="dotenv.state === 'none'" class="help">
          <template v-if="dotenv.ignored">Slaw is ignoring this workspace's .env: {{ dotenv.ignored }}.</template>
          <template v-else>A <code>.env</code> file in the workspace folder can hold sign-in variables such as <code>MOTHERDUCK_TOKEN</code> for connections that use Environment variables. Slaw asks before it loads one.</template>
        </div>
        <template v-else>
          <div class="help" data-testid="dotenv-settings-names">Variables: <template v-if="dotenv.names.length"><template v-for="(n, i) in dotenv.names" :key="n"><code>{{ n }}</code><template v-if="i < dotenv.names.length - 1">, </template></template></template><template v-else>none</template>. Values are never shown.</div>
          <div v-for="s in dotenv.satisfies" :key="s.connection" class="help">Signs in <b>{{ s.connection }}</b> through <code>{{ s.signIn }}</code> ({{ s.variables.join(', ') }}).</div>
          <div v-if="dotenv.state === 'changed'" class="help">The file changed after you allowed it. {{ dotenv.loaded ? 'The values from before the change stay loaded until you allow it again.' : 'Allow it again to load the new content.' }}</div>
          <div v-if="dotenv.state === 'denied'" class="help">Slaw won't load it, even after edits, until you revoke this.</div>
          <div v-if="dotenv.trackedByGit" class="help" role="alert" style="color: var(--sh-error); font-weight: 600;">Git tracks this file, so a credential is committed. Remove it from git (<code>git rm --cached .env</code>) and rotate the credential.</div>
          <div v-for="w in dotenv.warnings ?? []" :key="w" class="help">{{ w }}</div>
          <div v-if="dotenvError" class="help" role="alert" style="color: var(--sh-error);">{{ dotenvError }}</div>
          <div class="row" style="gap: var(--sh-space-2); flex-wrap: wrap;">
            <button v-if="dotenv.state !== 'allowed'" class="btn btn-primary btn-sm" type="button" :disabled="dotenvBusy" data-testid="dotenv-settings-allow" @click="allowDotEnv">Allow</button>
            <button v-if="dotenv.state !== 'denied'" class="btn btn-sm" type="button" :disabled="dotenvBusy" data-testid="dotenv-settings-deny" @click="denyDotEnv">Deny</button>
            <button v-if="dotenv.state === 'allowed' || dotenv.state === 'denied' || dotenv.state === 'changed'" class="btn btn-sm" type="button" :disabled="dotenvBusy" data-testid="dotenv-settings-revoke" @click="revokeDotEnv">Revoke</button>
          </div>
        </template>
      </div>
    </div>

    <ArtifactViewDecisions />

    <ReceiptsSection />

    <AgentSettings />

    <!-- Claude and other tools (MCP) -->
    <div>
      <div class="sec-title"><h2 style="font-size: 22px;">Claude and other tools</h2><span class="label">MCP</span></div>
      <div class="card card-pad col" style="gap: var(--sh-space-3);" data-testid="mcp-panel">
        <div class="row" style="gap: var(--sh-space-2);">
          <span class="status-dot" :class="mcp?.running ? 'ok' : 'off'"></span>
          <span class="sh-label" style="margin: 0;">{{ mcp?.running ? 'Running, only on this computer' : 'Off' }}</span>
        </div>

        <div v-if="mcp?.running && mcp.url">
          <label class="field-label">Address</label>
          <div class="row" style="gap: var(--sh-space-2);">
            <code class="break-all" style="flex: 1; user-select: all; padding: 8px 12px;">{{ mcp.url }}</code>
            <button class="btn btn-sm" type="button" @click="copy(mcp.url, 'url')"><Icon name="copy" :size="14" /> {{ copied === 'url' ? 'Copied' : 'Copy' }}</button>
          </div>
        </div>

        <div class="row" style="gap: var(--sh-space-2); flex-wrap: wrap;">
          <button class="btn" :class="mcp?.running ? 'btn-destructive' : 'btn-primary'" type="button" :disabled="mcpBusy || (!mcp?.running && !mcp?.canStart)" data-testid="mcp-toggle" @click="toggleMCP">
            {{ mcpBusy ? '…' : (mcp?.running ? 'Stop' : 'Start') }}
          </button>
          <button class="btn btn-sm" type="button" data-testid="copy-claude-code" @click="copyConfig('code')"><Icon name="copy" :size="14" /> {{ copied === 'code' ? 'Copied' : 'Copy Claude Code config' }}</button>
          <button class="btn btn-sm" type="button" data-testid="copy-claude-desktop" @click="copyConfig('desktop')"><Icon name="copy" :size="14" /> {{ copied === 'desktop' ? 'Copied' : 'Copy Claude Desktop config' }}</button>
        </div>
        <p v-if="mcp && !mcp.canStart && !mcp.running" class="help" style="margin: 0;">{{ mcp.reason }}</p>
        <div v-if="mcp?.startError" class="help" style="margin: 0;" data-testid="mcp-start-error">
          <p style="margin: 0;">It was on when you last used this workspace but did not restart. Press Start to try again.</p>
          <p style="margin: 4px 0 0;"><code class="break-all">{{ mcp.startError }}</code></p>
        </div>
        <p v-if="mcp?.saveError" class="help" style="margin: 0;" data-testid="mcp-save-error">Studio couldn't save this choice; the next time you open this workspace it uses the last saved one: <code class="break-all">{{ mcp.saveError }}</code></p>
        <p v-else-if="mcp?.remembered && !mcp.canStart" class="help" style="margin: 0;" data-testid="mcp-remembered">It is on for this workspace on this computer, but this window is read-only, so it isn't serving now.</p>
        <p v-else-if="mcp?.canStart" class="help" style="margin: 0;" data-testid="mcp-remembered">{{ mcp.remembered ? 'Remembered: Studio turns it on each time it opens this workspace on this computer. Stop turns that off.' : 'Press Start once and Studio will turn it on each time it opens this workspace on this computer.' }}</p>

        <div class="note info">
          <Icon name="info" :size="16" />
          <span>The copied config starts <code>slaw mcp serve</code> for this folder. It holds no password or token: when Studio has the workspace open, that program reaches Studio on its own.</span>
        </div>
        <div class="note warn">
          <Icon name="alert" :size="16" />
          <span>With Studio closed, tools using <code>slaw mcp serve</code> can't change anything while "Ask me before it changes data" is on. Turn it off to let them change the workspace's own <code>local</code> database only; other connections always need Studio.</span>
        </div>
        <p v-if="isReadOnly" class="help" style="margin: 0;">This window opened the workspace read-only, so it can't serve MCP until you take it over.</p>
      </div>
    </div>
  </div>
</template>
