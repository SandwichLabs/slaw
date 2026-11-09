// useWorkspace — the module-singleton mirror of the open workspace (#111) and
// the flows that change it: open, create, switch, close, take over.
//
// The backend (studio/workspace.go) is the source of truth; this keeps one
// shared `workspaceInfo` ref in sync through the "workspace:*" events so the
// shell, the launcher, Settings and the banners read the same value without each
// polling a binding. Every flow that replaces the open workspace flushes pending
// notebook saves FIRST (usePendingSaves), while the old workspace is still open,
// so a debounced edit is never lost or sent to the wrong folder.
import { ref, computed } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { flushAll } from './usePendingSaves'
import { askConfirm } from './useConfirm'
import { errorText } from '../lib/errors'
import { hasWailsRuntime } from '../lib/wails'
import { App } from '../lib/bindings'

export const workspaceInfo = ref<WorkspaceInfo | null>(null)
export const workspaceOpen = computed(() => workspaceInfo.value !== null)
export const isReadOnly = computed(() => !!workspaceInfo.value?.readOnly)

// A plain-language note the launcher shows after the workspace was closed from
// outside (another program took it over), or a startup argument problem.
export const launcherNotice = ref('')
// appNotice is a plain-language note shown over whichever screen is up (a path the
// OS asked Slaw to open that could not be used while a workspace is already open).
export const appNotice = ref('')

// Dialog visibility is global so a keyboard shortcut works from any screen.
export const newWorkspaceOpen = ref(false)
export const takeOverOpen = ref(false)
export const switcherOpen = ref(false)


let started = false
const unsubs: Array<() => void> = []

// startWorkspaceTracking wires the shared listeners exactly once per page load.
export function startWorkspaceTracking(): void {
  if (started || !hasWailsRuntime()) return
  started = true
  unsubs.push(
    EventsOn('workspace:opened', (info: WorkspaceInfo) => {
      launcherNotice.value = ''
      switcherOpen.value = false
      if (info) workspaceInfo.value = info
      else void refreshWorkspaceInfo()
    }),
    EventsOn('workspace:closed', (p?: { reason?: string }) => {
      // A switch closes the old workspace and opens the next one in one call:
      // keep the shell mounted, "workspace:opened" replaces the info.
      if (p?.reason === 'switch') return
      workspaceInfo.value = null
      if (p?.reason === 'released') {
        launcherNotice.value = 'Another program asked Slaw to let go of this workspace, so it was closed. Your notebooks were saved first.'
      }
    }),
    // Settings, MCP on/off, slaw.yaml edited elsewhere: re-read the summary.
    EventsOn('workspace:changed', () => { void refreshWorkspaceInfo() }),
    // Another program (slaw mcp serve) asked Studio to let go: save what's open.
    EventsOn('workspace:releasing', () => { void flushAll() }),
  )
}

export async function refreshWorkspaceInfo(): Promise<void> {
  try {
    workspaceInfo.value = await App.GetWorkspaceInfo()
  } catch {
    workspaceInfo.value = null
  }
}

// syncWorkspaceState reads the backend once at startup (a reload mid-session
// keeps the workspace open on the Go side).
export async function syncWorkspaceState(): Promise<void> {
  try {
    if (await App.IsWorkspaceOpen()) await refreshWorkspaceInfo()
    else workspaceInfo.value = null
  } catch {
    workspaceInfo.value = null
  }
}

// flushOrConfirm saves every open notebook; a failed save asks the person
// before the workspace is replaced. Resolves false when they chose to stay.
export async function flushOrConfirm(action: string): Promise<boolean> {
  const { failed } = await flushAll()
  if (failed.length === 0) return true
  return askConfirm({
    title: 'Unsaved changes',
    message: `${failed[0].label} couldn't be saved: ${failed[0].error}. ${action} anyway?`,
    variant: 'destructive',
    confirmLabel: action,
  })
}

interface FlowResult {
  ok: boolean
  error?: string
  warnings?: string[]
}

// launching is true from startup until the launch request (a folder named on
// the command line, or the working directory's workspace) has been acted on, so
// a start that opens a workspace never flashes the Launcher (#164).
export const launching = ref(true)

// finishOpen turns an OpenWorkspaceResult into the shared state, asking
// "Make this folder a workspace?" when the folder has no slaw.yaml.
async function finishOpen(res: OpenWorkspaceResult): Promise<FlowResult> {
  if (res.status === 'not-workspace') {
    // The question belongs over the Launcher, not over the blank startup page.
    launching.value = false
    const yes = await askConfirm({
      title: 'Make this folder a workspace?',
      message: `${res.path}\n\nThis folder doesn't have a slaw.yaml yet. Slaw will add one, plus notebooks/, queries/, skills/ and data/. Your other files are left alone and nothing is moved.`,
      variant: 'primary',
      confirmLabel: 'Make it a workspace',
    })
    if (!yes) return { ok: false }
    const created = await App.CreateWorkspace({
      name: '', dir: res.path, parentDir: '', git: false, template: '', inPlace: true,
    })
    if (created.info) workspaceInfo.value = created.info
    return { ok: true, warnings: created.warnings }
  }
  if (res.info) workspaceInfo.value = res.info
  return { ok: true, warnings: res.warnings }
}

// openWorkspacePath opens (or, while one is open, switches to) the workspace at
// path. The old workspace's pending saves are flushed first.
export async function openWorkspacePath(path: string): Promise<FlowResult> {
  try {
    if (workspaceOpen.value && !(await flushOrConfirm('Switch'))) return { ok: false }
    return await finishOpen(await App.OpenWorkspace(path))
  } catch (e: unknown) {
    await syncWorkspaceState() // the old workspace is closed even when the new one failed
    return { ok: false, error: errorText(e) }
  }
}

// openFolderDialog is "Open folder…": the native picker, then the open flow.
export async function openFolderDialog(): Promise<FlowResult> {
  try {
    const path = await App.SelectWorkspaceFolder()
    if (!path) return { ok: false }
    return await openWorkspacePath(path)
  } catch (e: unknown) {
    return { ok: false, error: errorText(e) }
  }
}

export async function createWorkspace(req: CreateWorkspaceRequest): Promise<FlowResult> {
  try {
    if (workspaceOpen.value && !(await flushOrConfirm('Switch'))) return { ok: false }
    const res = await App.CreateWorkspace(req)
    if (res.info) workspaceInfo.value = res.info
    return { ok: true, warnings: res.warnings }
  } catch (e: unknown) {
    await syncWorkspaceState()
    return { ok: false, error: errorText(e) }
  }
}

// closeWorkspace flushes, then closes (the launcher takes over).
export async function closeWorkspace(): Promise<FlowResult> {
  try {
    if (!(await flushOrConfirm('Close'))) return { ok: false }
    await App.CloseWorkspace()
    workspaceInfo.value = null
    return { ok: true }
  } catch (e: unknown) {
    return { ok: false, error: errorText(e) }
  }
}

// takeOverWorkspace asks the program that has the workspace open to let go.
export async function takeOverWorkspace(): Promise<FlowResult> {
  try {
    const res = await App.TakeOverWorkspace()
    if (res.info) workspaceInfo.value = res.info
    return { ok: true }
  } catch (e: unknown) {
    await syncWorkspaceState()
    return { ok: false, error: errorText(e) }
  }
}

// ---- small display helpers ----

export function baseName(path: string): string {
  const parts = path.replace(/[\\/]+$/, '').split(/[\\/]/)
  return parts[parts.length - 1] || path
}

// relativeTime renders "2 hours ago" for an RFC 3339 timestamp.
export function relativeTime(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso)
  if (!iso || Number.isNaN(t)) return ''
  const s = Math.max(0, Math.round((now - t) / 1000))
  if (s < 45) return 'Just now'
  const m = Math.round(s / 60)
  if (m < 60) return m === 1 ? '1 minute ago' : `${m} minutes ago`
  const h = Math.round(m / 60)
  if (h < 24) return h === 1 ? '1 hour ago' : `${h} hours ago`
  const d = Math.round(h / 24)
  if (d === 1) return 'Yesterday'
  if (d < 30) return `${d} days ago`
  const mo = Math.round(d / 30)
  if (mo < 12) return mo === 1 ? '1 month ago' : `${mo} months ago`
  const y = Math.round(mo / 12)
  return y === 1 ? '1 year ago' : `${y} years ago`
}

// slugify mirrors the Go side's workspaceSlug (folder name from a name).
export function slugify(name: string): string {
  const s = name.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  return s || 'workspace'
}

// _resetForTests clears the module singleton between component tests.
export function _resetForTests(): void {
  unsubs.splice(0).forEach((u) => u())
  started = false
  launching.value = true
  workspaceInfo.value = null
  launcherNotice.value = ''
  newWorkspaceOpen.value = false
  takeOverOpen.value = false
  switcherOpen.value = false
}
