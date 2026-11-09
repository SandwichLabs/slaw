// App.vue: the launcher vs the shell, the workspace shortcuts (they work on the
// launcher too), the startup folder, and the quit flow. There is no Save / Don't
// save prompt any more: quitting flushes open notebooks and quits.
type MockApp = Partial<Record<keyof AppApi, any>>
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { router } from './router/index'
import { register, _resetForTests as resetSaves } from './composables/usePendingSaves'

const handlers: Record<string, (p?: any) => void> = {}
const listeners: Record<string, Array<(p?: any) => void>> = {}
let eventsBroken = false
vi.mock('./wailsjs/runtime/runtime', () => ({
  EventsOn: (name: string, cb: (p?: any) => void) => {
    if (eventsBroken) throw new Error('no runtime')
    // Several listeners per event, as in the app (the shell's Evidence drawer listens too).
    const list = (listeners[name] ??= [])
    list.push(cb)
    handlers[name] = (p?: any) => list.slice().forEach((f) => f(p))
    return () => {
      list.splice(list.indexOf(cb), 1)
      if (list.length === 0) delete handlers[name]
    }
  },
  EventsOff: () => {},
  WindowSetTitle: () => {},
}))

import App from './App.vue'
import { resolveConfirm, resolveConfirmChoice, confirmVisible } from './composables/useConfirm'
import { bundleImportRequest } from './composables/useBundle'
import { workspaceInfo, newWorkspaceOpen, launcherNotice, switcherOpen, _resetForTests } from './composables/useWorkspace'

function makeApp(over: MockApp = {}) {
  const app: MockApp = {
    IsWorkspaceOpen: vi.fn(async () => false),
    GetWorkspaceInfo: vi.fn(async () => ({ root: '/w/a', name: 'A', sessionId: 's', policy: { sandbox: 'read-write', askBeforeWrites: true } })),
    GetInitialWorkspace: vi.fn(async () => ({ path: '', message: '' })),
    MarkLaunchHandled: vi.fn(async () => {}),
    ListRecentWorkspaces: vi.fn(async () => []),
    OpenWorkspace: vi.fn(async (path: string) => ({ status: 'opened', path, info: { root: path, name: 'Opened', sessionId: 's', policy: { sandbox: 'read-write', askBeforeWrites: true }, connections: [] } })),
    SelectWorkspaceFolder: vi.fn(async () => ''),
    CheckGitAvailable: vi.fn(async () => true),
    ListTemplates: vi.fn(async () => []),
    ListSkills: vi.fn(async () => []),
    Quit: vi.fn(async () => {}),
    ...over,
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  ;(window as any).runtime = {}
  return app
}

let mounted: ReturnType<typeof mount>[] = []
const mountApp = () => {
  const w = mount(App, { global: { plugins: [router], stubs: { Icon: true, AgentPanel: true } } })
  mounted.push(w)
  return w
}

describe('App.vue', () => {
  beforeEach(async () => {
    vi.useRealTimers()
    _resetForTests()
    resetSaves()
    for (const k of Object.keys(handlers)) delete handlers[k]
    for (const k of Object.keys(listeners)) delete listeners[k]
    await router.push('/')
    await router.isReady()
  })

  afterEach(() => { mounted.forEach((w) => w.unmount()); mounted = [] })

  it('shows the launcher with no workspace and the shell with one', async () => {
    makeApp()
    const w = mountApp()
    await flushPromises()
    expect(w.find('[data-testid="launcher"]').exists()).toBe(true)
    expect(w.find('.topbar').exists()).toBe(false)

    handlers['workspace:opened']({ root: '/w/a', name: 'A', sessionId: 's', policy: { sandbox: 'read-write', askBeforeWrites: true }, connections: [] })
    await flushPromises()
    expect(w.find('.topbar').exists()).toBe(true)
    expect(w.find('[data-testid="ws-switch"]').text()).toContain('A')
    // The container-era chrome is gone.
    expect(w.find('[data-testid="save-file"]').exists()).toBe(false)
    expect(w.find('[data-testid="close-file"]').exists()).toBe(false)
    expect(w.text()).not.toContain('Encrypted')
  })

  it('picks up a workspace that is already open (a reload mid-session)', async () => {
    makeApp({ IsWorkspaceOpen: vi.fn(async () => true) })
    const w = mountApp()
    await flushPromises()
    expect(workspaceInfo.value?.name).toBe('A')
    expect(w.find('.topbar').exists()).toBe(true)
  })

  it('opens the folder named on the command line, or shows why it could not', async () => {
    const app = makeApp({ GetInitialWorkspace: vi.fn(async () => ({ path: '/w/from-cli', message: '' })) })
    mountApp()
    await flushPromises()
    expect(app.OpenWorkspace).toHaveBeenCalledWith('/w/from-cli')

    _resetForTests()
    makeApp({ GetInitialWorkspace: vi.fn(async () => ({ path: '', message: '.slaw files are bundles: use Import .slaw… to bring one into a workspace.' })) })
    mountApp()
    await flushPromises()
    expect(launcherNotice.value).toContain('.slaw files are bundles')
  })

  it('consumes the launch target only after the open is handled (#33)', async () => {
    let finish: (v: unknown) => void = () => {}
    const app = makeApp({
      GetInitialWorkspace: vi.fn(async () => ({ path: '/w/from-cli', message: '' })),
      OpenWorkspace: vi.fn(() => new Promise((r) => { finish = r })),
    })
    mountApp()
    await flushPromises()
    expect(app.OpenWorkspace).toHaveBeenCalledWith('/w/from-cli')
    expect(app.MarkLaunchHandled).not.toHaveBeenCalled() // a reload now would still reopen it
    finish({ status: 'opened', path: '/w/from-cli', info: { root: '/w/from-cli', name: 'X', sessionId: 's', policy: { sandbox: 'read-write', askBeforeWrites: true }, connections: [] } })
    await flushPromises()
    expect(app.MarkLaunchHandled).toHaveBeenCalledTimes(1)
  })

  it('consumes the launch target when the open fails, and when a workspace is already open (#33)', async () => {
    const failing = makeApp({
      GetInitialWorkspace: vi.fn(async () => ({ path: '/w/bad', message: '' })),
      OpenWorkspace: vi.fn(async () => { throw new Error('nope') }),
    })
    mountApp()
    await flushPromises()
    expect(failing.MarkLaunchHandled).toHaveBeenCalledTimes(1)

    _resetForTests()
    const open = makeApp({ IsWorkspaceOpen: vi.fn(async () => true) })
    mountApp()
    await flushPromises()
    expect(open.MarkLaunchHandled).toHaveBeenCalledTimes(1)
  })

  it('does not flash the launcher while a launch workspace is opening', async () => {
    let release: (v: { path: string; message: string }) => void = () => {}
    makeApp({ GetInitialWorkspace: vi.fn(() => new Promise<{ path: string; message: string }>((r) => { release = r })) })
    const w = mountApp()
    await flushPromises()
    expect(w.find('[data-testid="launcher"]').exists()).toBe(false)
    release({ path: '/w/from-cwd', message: '' })
    await flushPromises()
    expect(w.find('[data-testid="launcher"]').exists()).toBe(false)
    expect(w.find('.topbar').exists()).toBe(true)
  })

  it('falls back to the launcher when the runtime events are unavailable', async () => {
    eventsBroken = true
    try {
      makeApp()
      const w = mountApp()
      await flushPromises()
      expect(w.find('[data-testid="launcher"]').exists()).toBe(true)
    } finally {
      eventsBroken = false
    }
  })

  it('shows the launcher behind the "make this a workspace" question', async () => {
    makeApp({
      GetInitialWorkspace: vi.fn(async () => ({ path: '/w/plain', message: '' })),
      OpenWorkspace: vi.fn(async (path: string) => ({ status: 'not-workspace', path })),
    })
    const w = mountApp()
    await flushPromises()
    expect(w.find('[data-testid="launcher"]').exists()).toBe(true)
    expect(confirmVisible.value).toBe(true)
    resolveConfirm(false)
    await flushPromises()
  })

  it('routes OS open requests: a .slaw opens Import, a note shows, a folder opens', async () => {
    const app = makeApp({ GetInitialWorkspace: vi.fn(async () => ({ path: '', message: '', bundlePath: '/tmp/x.slaw' })) })
    mountApp()
    await flushPromises()
    expect(bundleImportRequest.value).toEqual({ mode: 'new', path: '/tmp/x.slaw' })

    // A request that arrives after startup comes as an event.
    expect(handlers['app:open-path']).toBeTruthy()
    handlers['app:open-path']({ path: '/w/from-finder' })
    await flushPromises()
    expect(app.OpenWorkspace).toHaveBeenCalledWith('/w/from-finder')
  })

  it('Ctrl+N opens New workspace, Ctrl+O opens the folder picker, F5 is intercepted', async () => {
    const app = makeApp()
    mountApp()
    await flushPromises()

    const f5 = new KeyboardEvent('keydown', { key: 'F5', cancelable: true })
    window.dispatchEvent(f5)
    expect(f5.defaultPrevented).toBe(true)

    const n = new KeyboardEvent('keydown', { key: 'n', ctrlKey: true, cancelable: true })
    window.dispatchEvent(n)
    expect(n.defaultPrevented).toBe(true)
    expect(newWorkspaceOpen.value).toBe(true)

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'o', ctrlKey: true, cancelable: true }))
    await flushPromises()
    expect(app.SelectWorkspaceFolder).toHaveBeenCalled()
  })

  it('Ctrl+Shift+O toggles the switcher only while a workspace is open', async () => {
    makeApp()
    mountApp()
    await flushPromises()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'O', ctrlKey: true, shiftKey: true }))
    expect(switcherOpen.value).toBe(false)

    workspaceInfo.value = { root: '/w/a', name: 'A', sessionId: 's' } as unknown as WorkspaceInfo
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'O', ctrlKey: true, shiftKey: true }))
    expect(switcherOpen.value).toBe(true)
  })

  it('app:before-close flushes pending notebook saves, then quits with no prompt', async () => {
    const calls: string[] = []
    const app = makeApp({ Quit: vi.fn(async () => { calls.push('quit') }) })
    register(() => 'nb', async () => { calls.push('flush') })
    mountApp()
    await flushPromises()
    handlers['app:before-close']()
    await flushPromises()
    expect(calls).toEqual(['flush', 'quit'])
    expect(app.Quit).toHaveBeenCalledTimes(1)
  })

  it('a different workspace lands on Home, not on the old route; the same one keeps it', async () => {
    makeApp()
    mountApp()
    await flushPromises()
    handlers['workspace:opened']({ root: '/w/a', name: 'A', sessionId: 's1', policy: { sandbox: 'read-write', askBeforeWrites: true }, connections: [] })
    await flushPromises()
    await router.push('/skills')
    handlers['workspace:opened']({ root: '/w/a', name: 'A', sessionId: 's2', policy: { sandbox: 'read-write', askBeforeWrites: true }, connections: [] })
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/skills')
    handlers['workspace:opened']({ root: '/w/b', name: 'B', sessionId: 's3', policy: { sandbox: 'read-write', askBeforeWrites: true }, connections: [] })
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/')
    expect(switcherOpen.value).toBe(false)
  })

  it('quitting with an edit that could not be saved asks first; staying cancels the quit', async () => {
    const app = makeApp({ CancelQuit: vi.fn(async () => {}), HoldQuit: vi.fn(async () => {}) })
    register(() => 'Weekly revenue', async () => { throw new Error('changed on disk') })
    mountApp()
    await flushPromises()
    handlers['app:before-close']()
    await vi.waitFor(() => expect(confirmVisible.value).toBe(true))
    expect(app.HoldQuit).toHaveBeenCalled() // the watchdog is stopped before asking
    resolveConfirmChoice('cancel')
    await flushPromises()
    expect(app.CancelQuit).toHaveBeenCalled()
    expect(app.Quit).not.toHaveBeenCalled()

    handlers['app:before-close']()
    await vi.waitFor(() => expect(confirmVisible.value).toBe(true))
    resolveConfirmChoice('confirm')
    await flushPromises()
    expect(app.Quit).toHaveBeenCalledTimes(1)
  })

  it('closing and opening a different workspace from the launcher starts at Home', async () => {
    makeApp()
    mountApp()
    await flushPromises()
    handlers['workspace:opened']({ root: '/w/a', name: 'A', sessionId: 's1', policy: { sandbox: 'read-write', askBeforeWrites: true }, connections: [] })
    await flushPromises()
    await router.push('/skills')
    handlers['workspace:closed']({ reason: 'closed' })
    await flushPromises()
    handlers['workspace:opened']({ root: '/w/b', name: 'B', sessionId: 's2', policy: { sandbox: 'read-write', askBeforeWrites: true }, connections: [] })
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/')
  })
})
