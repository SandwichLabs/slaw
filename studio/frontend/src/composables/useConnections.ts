// useConnections — the module-singleton mirror of the open workspace's
// connections (#117): the rail, the selector, the status dialogs and the
// relaunch prompt all read the same list, kept fresh by "connection:status",
// "workspace:changed" and "workspace:opened". No secret ever passes through
// here: credentials are sent to the backend by the dialogs and never read back.
import { ref, computed } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { errorText } from '../lib/errors'
import { takeOverOpen } from './useWorkspace'
import { hasWailsRuntime } from '../lib/wails'
import { App } from '../lib/bindings'
import { ssoProfileOf } from '../lib/connections'

export const connectionList = ref<ConnectionInfo[]>([])
export const relaunch = ref<{ required: boolean; message: string }>({ required: false, message: '' })
// catalogVersion bumps on "catalog:changed"; the rail reloads expanded levels.
export const catalogVersion = ref(0)
// Names whose connect is in flight (spinner replaces the status icon).
export const connecting = ref<Set<string>>(new Set())

// Dialog state is global so any surface (rail, selector, editor, query run) can
// raise the same prompt.
export const signInFor = ref<string | null>(null)
export const extensionFor = ref<{ connection: string; extension: string } | null>(null)

// An AWS sign-in is the device flow (useSignIn), not a password prompt, and it
// starts only from an explicit Sign in button. A prompt raised by some other use
// (a failed Run, a pick in the connection selector, a tree expand, a deep link)
// only reveals the banner. useSignIn registers its reveal here; it imports this
// module, so it cannot be imported back.
let revealAwsSignIn: ((profile: string) => void) | null = null
export function setAwsSignInReveal(f: ((profile: string) => void) | null): void { revealAwsSignIn = f }

let started = false
const unsubs: Array<() => void> = []

export async function refreshConnections(): Promise<void> {
  try {
    const res = await App.ListConnections()
    connectionList.value = res.connections ?? []
    relaunch.value = { required: !!res.relaunchRequired, message: res.relaunchMessage ?? '' }
  } catch {
    connectionList.value = []
  }
}

export function startConnectionTracking(): void {
  if (started || !hasWailsRuntime()) return
  started = true
  unsubs.push(
    EventsOn('connection:status', (p: { name: string; status: string; detail?: string }) => {
      if (!p) return
      // The event carries the headline only; re-read for the rest (extension,
      // server change, pid) so the row is never half-updated.
      void refreshConnections()
    }),
    EventsOn('workspace:changed', () => { void refreshConnections() }),
    EventsOn('workspace:opened', () => { void refreshConnections() }),
    EventsOn('workspace:closed', () => { connectionList.value = [] }),
    EventsOn('catalog:changed', () => { catalogVersion.value++ }),
  )
  void refreshConnections()
}

export const connectionByName = (name: string) => connectionList.value.find((c) => c.name === name)
export const defaultConnectionName = computed(() => connectionList.value.find((c) => c.default)?.name ?? 'local')

interface ConnectResult { ok: boolean; error?: string; info?: ConnectionInfo | null }

// connect dials a connection. When it can't be used yet, the matching prompt
// opens (sign in, install the extension, take over) instead of a bare error.
export async function connect(name: string): Promise<ConnectResult> {
  connecting.value = new Set(connecting.value).add(name)
  try {
    const info = await App.ConnectConnection({ name, scope: '' })
    await refreshConnections()
    return { ok: true, info }
  } catch (e: unknown) {
    await refreshConnections()
    const c = connectionByName(name)
    if (c) openPromptFor(c)
    return { ok: false, error: errorText(e), info: c ?? null }
  } finally {
    const s = new Set(connecting.value)
    s.delete(name)
    connecting.value = s
  }
}

// openPromptFor raises the prompt a status asks for, if any. seen guards a
// requirement chain against a loop.
export function openPromptFor(c: ConnectionInfo, seen: string[] = []): boolean {
  switch (c.status) {
    case 'needs-credentials':
      // A connection waiting on another's sign-in (a catalog and its storage
      // connection) has nothing to enter itself: the prompt, the AWS banner and
      // the Add it action are the blocker's.
      if (c.blockedBy) {
        if (seen.includes(c.blockedBy)) return false
        const b = connectionByName(c.blockedBy)
        return b ? openPromptFor(b, [...seen, c.name]) : false
      }
      // A changed server is confirmed in the editor, which the prompt links to (Edit
      // connection) without leaving the page that raised it; a new sign-in cannot fix it.
      if (ssoProfileOf(c) && !c.serverChanged && revealAwsSignIn) { revealAwsSignIn(ssoProfileOf(c)); return true }
      signInFor.value = c.name
      return true
    case 'extension-missing': extensionFor.value = { connection: c.name, extension: c.extension || '' }; return true
    case 'locked-by': takeOverOpen.value = true; return true
  }
  return false
}

export function _resetConnectionsForTests(): void {
  unsubs.splice(0).forEach((u) => u())
  started = false
  connectionList.value = []
  relaunch.value = { required: false, message: '' }
  connecting.value = new Set()
  signInFor.value = null
  extensionFor.value = null
  catalogVersion.value = 0
  revealAwsSignIn = null
}
