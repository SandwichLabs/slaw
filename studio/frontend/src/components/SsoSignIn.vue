<script setup lang="ts">
// SsoSignIn — the body of the connection editor's fourth sign-in card, "AWS
// profile (SSO)" (S3 only): the profile name (the only thing stored, on this
// computer), the state of its session, and "Sign in now". No key fields: the
// short-lived keys are made at connect time and never stored.
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { App } from '../lib/bindings'
import SignInCode from './SignInCode.vue'
import { promptFor, errorFor, isStarting, startSignIn, cancelSignIn } from '../composables/useSignIn'
import { connectionList } from '../composables/useConnections'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { hasWailsRuntime } from '../lib/wails'

const props = defineProps<{
  /** The saved connection's name; '' for a new one. */
  connection: string
  /** True when the profile typed here is the one saved for the connection. */
  saved: boolean
}>()
const profile = defineModel<string>('profile', { required: true })

const PROFILE_RE = /^[A-Za-z0-9][A-Za-z0-9_.+@-]{0,63}$/
const valid = computed(() => !profile.value || PROFILE_RE.test(profile.value))

const status = ref<ConnCredentialStatus | null>(null)
async function loadStatus() {
  if (!props.connection || !props.saved) { status.value = null; return }
  try { status.value = (await App.GetConnectionCredentialStatus(props.connection)) as ConnCredentialStatus } catch { status.value = null }
}
const unsubs: Array<() => void> = []
onMounted(() => {
  void loadStatus()
  if (hasWailsRuntime()) {
    unsubs.push(EventsOn('signin:done', () => { void loadStatus() }), EventsOn('signin:failed', () => { void loadStatus() }))
  }
})
onUnmounted(() => { unsubs.splice(0).forEach((u) => u()) })

// Sign in now and Cancel swap places: keep the keyboard focus on the new one.
const root = ref<HTMLElement | null>(null)
async function act(go: () => Promise<void>, next: string) {
  await go()
  await nextTick()
  ;(root.value?.querySelector(`[data-testid="${next}"]`) as HTMLElement | null)?.focus()
}
watch(() => [props.connection, props.saved], loadStatus)
// A status change (a rotation that ended the session) re-reads the line too.
watch(() => connectionList.value.find((c) => c.name === props.connection)?.status, loadStatus)

const prompt = computed(() => promptFor(profile.value))
const error = computed(() => errorFor(profile.value))
const starting = computed(() => isStarting(profile.value))
const canSignIn = computed(() => props.saved && !!profile.value && valid.value && !starting.value && !prompt.value)

// A denied or failed new sign-in leaves the current session alone: say so.
const errorLine = computed(() => (status.value?.signedIn ? `That sign-in didn't finish (${error.value}). Your current session still works.` : error.value))

// The hint quotes the terminal command in backticks: show it as code.
const stateParts = computed(() => stateLine.value.split('`').map((t, i) => ({ t, code: i % 2 === 1 })))

const stateLine = computed(() => {
  if (!props.saved) return props.connection ? 'Save the connection with this profile to check its session.' : 'Save the connection to check its session.'
  const s = status.value
  if (!s) return ''
  if (s.signedIn) {
    const until = s.expiresAt ? new Date(s.expiresAt) : null
    return until && !Number.isNaN(until.getTime()) ? `Signed in. The session lasts until ${until.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}.` : 'Signed in.'
  }
  return s.detail || 'Not signed in.'
})
</script>

<template>
  <div ref="root" data-testid="sso-signin">
    <div class="field">
      <label class="field-label" for="f-sso-profile">AWS profile</label>
      <input id="f-sso-profile" v-model="profile" class="field-input" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" aria-describedby="f-sso-h" :aria-invalid="!valid" data-testid="f-sso-profile" />
      <div id="f-sso-h" class="help">The name of an IAM Identity Center (SSO) profile in <code>~/.aws/config</code>. Only the name is saved, on this computer. Nothing secret is stored: short-lived keys are made when Slaw connects.</div>
      <div v-if="!valid" role="alert" class="help err-text" data-testid="sso-profile-invalid">Use letters, numbers and <code>_ . + @ -</code>, starting with a letter or number.</div>
    </div>
    <div class="sso-state" role="status" aria-live="polite">
      <span v-if="stateLine" class="help" data-testid="sso-state"><template v-for="(p, i) in stateParts" :key="i"><code v-if="p.code">{{ p.t }}</code><template v-else>{{ p.t }}</template></template></span>
      <template v-if="prompt">
        <span class="help">Approve the sign-in in the page that opened, after checking it shows this code:</span>
        <SignInCode :prompt="prompt" testid="sso-code" />
      </template>
      <span v-if="error" class="help err-text" role="alert" data-testid="sso-error">{{ errorLine }}</span>
    </div>
    <div class="sso-actions">
      <button v-if="!prompt" type="button" class="btn btn-sm" :disabled="!canSignIn" data-testid="sso-signin-now" @click="act(() => startSignIn(connection, profile), 'sso-cancel')">{{ starting ? 'Starting…' : 'Sign in now' }}</button>
      <button v-else type="button" class="btn btn-sm" data-testid="sso-cancel" @click="act(() => cancelSignIn(profile), 'sso-signin-now')">Cancel</button>
      <span class="help">Or run <code>aws sso login --profile {{ profile || 'PROFILE' }}</code> in a terminal.</span>
    </div>
  </div>
</template>

<style scoped>
.sso-state { display: flex; flex-direction: column; gap: 4px; margin: 8px 0; }
.sso-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
</style>
