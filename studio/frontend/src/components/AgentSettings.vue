<script setup lang="ts">
import { App } from '../lib/bindings'
// Settings > Workspace > The agent (#79, #111, SPEC 09). Two layers, because a
// committed file must not be able to loosen anyone's safeguards:
//   - Team limit: saved in slaw.yaml and shared through git. It can only make
//     things STRICTER (cap the tier, require the gate for everyone).
//   - On this computer: this person's own choice, kept outside the folder. Raise
//     the tier up to the team limit, or turn the gate off (never below what the
//     team requires).
// The switch only covers the workspace's own `local` database; every other
// connection always asks.
import { ref, computed } from 'vue'
import { SANDBOX_MODES } from '../lib/agentSandbox'
import { settingsError } from '../lib/errors'
import { workspaceInfo, refreshWorkspaceInfo } from '../composables/useWorkspace'
import { askConfirm } from '../composables/useConfirm'
import Icon from './Icon.vue'

const info = computed(() => workspaceInfo.value)
const status = ref('')
const stale = ref(false)
const saving = ref(false)

const RANK: Record<string, number> = { 'read-only': 0, 'read-write': 1, full: 2 }
const teamLimit = computed(() => (info.value?.teamSandboxSet ? info.value.team.sandbox : 'full'))
const teamRequiresAsk = computed(() => !!info.value?.team.askBeforeWrites)

function overLimit(mode: string): boolean {
  return RANK[mode] > RANK[teamLimit.value]
}

async function persist(patch: Partial<WorkspaceSettings>) {
  const i = info.value
  if (!i) return
  saving.value = true
  status.value = ''
  stale.value = false
  const next: WorkspaceSettings = {
    name: i.name,
    defaultConnection: i.defaultConnection,
    teamSandbox: i.teamSandboxSet ? i.team.sandbox : '',
    teamAskBeforeChanges: i.team.askBeforeWrites,
    sandbox: i.local.sandbox,
    askBeforeChanges: i.local.askBeforeWrites,
    // What the form was opened with: a slaw.yaml changed meanwhile is refused.
    loadedTeamSandbox: i.teamSandboxSet ? i.team.sandbox : '',
    loadedTeamAsk: i.team.askBeforeWrites,
    ...patch,
  }
  try {
    workspaceInfo.value = await App.SetWorkspaceSettings(next)
    status.value = 'Saved'
    setTimeout(() => { if (status.value === 'Saved') status.value = '' }, 2000)
  } catch (e: unknown) {
    const se = settingsError(e)
    status.value = 'Error: ' + se.text
    stale.value = se.stale
  }
  saving.value = false
}

// Raising the team limit changes slaw.yaml for everyone who uses the workspace.
async function setTeamTier(m: string) {
  if (RANK[m] > RANK[teamLimit.value]) {
    const ok = await askConfirm({
      title: 'Raise the team limit?',
      message: `This changes slaw.yaml for everyone who uses this workspace: the Slaw assistant will be allowed to do more (up to "${SANDBOX_MODES.find((x) => x.value === m)?.label ?? m}") for each of them once they pull the change. Anyone can still choose less on their own computer.`,
      variant: 'destructive',
      confirmLabel: 'Raise the limit',
    })
    if (!ok) return
  }
  await persist({ teamSandbox: m === 'full' ? '' : m, sandbox: overLimitAfter(m) })
}
// Lowering the team limit below this person's own tier lowers theirs with it.
function overLimitAfter(limit: string): string {
  const mine = info.value?.local.sandbox ?? 'read-write'
  return RANK[mine] > RANK[limit] ? limit : mine
}
const setMyTier = (m: string) => persist({ sandbox: m })
async function setTeamAsk(on: boolean, el?: HTMLInputElement) {
  if (!on) {
    const ok = await askConfirm({
      title: 'Stop requiring approval?',
      message: 'This changes slaw.yaml for everyone who uses this workspace: it will no longer require the Slaw assistant to ask before changes. Each person can still keep it on for themselves.',
      variant: 'destructive',
      confirmLabel: 'Stop requiring it',
    })
    if (!ok) { if (el) el.checked = true; return }
  }
  await persist({ teamAskBeforeChanges: on, askBeforeChanges: on ? true : (info.value?.local.askBeforeWrites ?? true) })
}
const setMyAsk = (on: boolean) => persist({ askBeforeChanges: on })
// The top tier with the gate still on: say the two settings stack (feedback-2026-11 §2).
const askHint = computed(() => info.value?.local.sandbox === 'full' && (info.value.local.askBeforeWrites || teamRequiresAsk.value))

// Which connections the agent can change, and what it does first.
const writable = computed(() => (info.value?.connections ?? []).filter((c) => !c.readOnly))
const isOwn = (c: ConnectionInfo) => c.name === 'local' && c.type === 'duckdb'
</script>

<template>
  <div v-if="info" data-testid="agent-settings">
    <div class="sec-title">
      <h2 style="font-size: 22px;">The Slaw assistant</h2>
      <span class="label">Two layers</span>
    </div>

    <div class="card card-pad col" style="gap: var(--sh-space-5);">
      <!-- Team limit -->
      <fieldset class="col" style="gap: var(--sh-space-3); border: none; padding: 0; margin: 0;">
        <legend class="field-label" style="padding: 0; margin-bottom: 4px;">Team limit <span class="scope shared" style="margin-left: 6px;">Team</span></legend>
        <p class="help" style="margin: 0;">What the Slaw assistant may do for everyone on the team. Saved in <code>slaw.yaml</code> and shared through git. This file can only make things <b>stricter</b>: a change someone pulls can never switch its safeguards off on your computer.</p>
        <div class="seg" role="group" aria-label="Team limit">
          <button
            v-for="m in SANDBOX_MODES"
            :key="m.value"
            type="button"
            :class="{ on: teamLimit === m.value }"
            :aria-pressed="teamLimit === m.value"
            :title="m.help"
            :data-testid="`team-sandbox-${m.value}`"
            @click="setTeamTier(m.value)"
          >{{ m.label }}</button>
        </div>
        <label class="row" style="gap: 10px; cursor: pointer;">
          <input type="checkbox" :checked="teamRequiresAsk" data-testid="team-ask" @change="setTeamAsk(($event.target as HTMLInputElement).checked, $event.target as HTMLInputElement)" />
          <span><span style="font-weight: 600; display: block;">Everyone is asked before changes</span><span class="sh-meta">The Slaw assistant shows each change and waits for an OK, for every person on the team, and nobody can turn that off.</span></span>
        </label>
      </fieldset>

      <!-- This computer -->
      <fieldset class="col" style="gap: var(--sh-space-3); border: none; padding: var(--sh-space-4) 0 0; margin: 0; border-top: 1.5px dashed var(--slaw-border-subtle);">
        <legend class="field-label" style="padding: 0 8px 0 0; margin-bottom: 4px;">On this computer <span class="scope local" style="margin-left: 6px;">This computer</span></legend>
        <p class="help" style="margin: 0;">Only you. Saved outside the folder, so it is never committed. Here you can <b>loosen</b>: raise the tier up to the team limit, or stop being asked before changes to the workspace's own database.</p>
        <div class="seg" role="group" aria-label="What the Slaw assistant can do on this computer">
          <button
            v-for="m in SANDBOX_MODES"
            :key="m.value"
            type="button"
            :class="{ on: info.local.sandbox === m.value }"
            :aria-pressed="info.local.sandbox === m.value"
            :disabled="overLimit(m.value)"
            :title="overLimit(m.value) ? `The team limit is ${teamLimit}` : m.help"
            :data-testid="`agent-sandbox-${m.value}`"
            @click="setMyTier(m.value)"
          >{{ m.label }}</button>
        </div>
        <p class="sh-meta" style="margin: 0;">{{ SANDBOX_MODES.find((m) => m.value === info!.local.sandbox)?.help }}</p>

        <label class="row" style="gap: 10px; cursor: pointer;">
          <input
            type="checkbox"
            role="switch"
            :checked="info.local.askBeforeWrites"
            :disabled="teamRequiresAsk"
            data-testid="agent-ask-before-writes"
            @change="setMyAsk(($event.target as HTMLInputElement).checked)"
          />
          <span>
            <span style="font-weight: 600; display: block;">Ask me before it changes data</span>
            <span class="sh-meta">{{ teamRequiresAsk ? 'The team requires this, so it stays on.' : "The Slaw assistant shows you each change and waits for your OK. This covers the workspace's own local database. For every other connection it always asks, and that can't be turned off." }}</span>
          </span>
        </label>
        <p v-if="askHint" class="sh-meta" data-testid="agent-ask-hint" style="margin: 0;">{{ teamRequiresAsk ? 'The team requires asking before changes, so every change waits for your OK here, and a tool connected without Studio open (slaw mcp serve) is refused.' : "Ask before changes is still on, so every change waits for your OK here, and a tool connected without Studio open (slaw mcp serve) is refused. Turn it off to let changes to this workspace's own files through." }}</p>
      </fieldset>

      <!-- What can change -->
      <div style="padding-top: var(--sh-space-3); border-top: 1.5px dashed var(--slaw-border-subtle);">
        <table class="brutal-table" data-testid="agent-write-table">
          <caption class="sr">Connections the Slaw assistant can change</caption>
          <thead><tr><th scope="col">Connection</th><th scope="col">The Slaw assistant can change it</th></tr></thead>
          <tbody>
            <tr v-for="c in writable" :key="c.name">
              <td><code>{{ c.name }}</code></td>
              <td>
                <template v-if="isOwn(c)">Yes, and it asks you first unless you turned that off.</template>
                <template v-else><span style="display: inline-flex; align-items: center; gap: 6px;"><Icon name="lock" :size="12" /> Yes, and it always asks first.</span></template>
              </td>
            </tr>
            <tr>
              <td>Everything else</td>
              <td>Read-only. It can look but not change.</td>
            </tr>
          </tbody>
        </table>
      </div>

      <p v-if="status" role="status" class="sh-body" :style="{ fontWeight: 600, color: status.startsWith('Error') ? 'var(--sh-error)' : 'var(--sh-teal-lo)' }">{{ status }}</p>
      <button v-if="stale" type="button" class="btn btn-sm" style="align-self: flex-start;" data-testid="reload-settings" @click="refreshWorkspaceInfo().then(() => { status = ''; stale = false })">Reload settings</button>
    </div>
  </div>
</template>
