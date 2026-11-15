import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { register, _resetForTests as resetSaves } from '../composables/usePendingSaves'

vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {}, EventsOff: () => {} }))

import WorkspaceSwitcher from './WorkspaceSwitcher.vue'
import { workspaceInfo, switcherOpen, newWorkspaceOpen, launcherNotice, _resetForTests } from '../composables/useWorkspace'

const INFO = { root: '/w/acme', name: 'Acme Analytics', gitBranch: 'main', sessionId: 's1' } as unknown as WorkspaceInfo
const recents: RecentWorkspace[] = [
  { path: '/w/acme', id: 'a', name: 'Acme Analytics', lastOpened: '2026-10-06T10:00:00Z', gitBranch: 'main', missing: false },
  { path: '/w/harbor', id: 'h', name: 'Harbor Lane Hardware', lastOpened: '2026-10-05T10:00:00Z', gitBranch: '', missing: false },
  { path: '/w/old', id: 'o', name: 'Old Ledger', lastOpened: '2026-01-05T10:00:00Z', gitBranch: '', missing: true },
]

function makeApp(over: Record<string, any> = {}) {
  const app = {
    ListRecentWorkspaces: vi.fn(async () => recents),
    OpenWorkspace: vi.fn(async (path: string) => ({ status: 'opened', path, info: { ...INFO, root: path, name: 'Harbor Lane Hardware', sessionId: 's2' } })),
    CloseWorkspace: vi.fn(async () => {}),
    SelectWorkspaceFolder: vi.fn(async () => ''),
    ChooseFolder: vi.fn(async () => ''),
    RelocateRecentWorkspace: vi.fn(async () => {}),
    IsWorkspaceOpen: vi.fn(async () => true),
    ...over,
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

const mountSwitcher = () => mount(WorkspaceSwitcher, { attachTo: document.body, global: { stubs: { Icon: true } } })

describe('WorkspaceSwitcher', () => {
  beforeEach(() => { _resetForTests(); resetSaves(); document.body.innerHTML = ''; workspaceInfo.value = INFO })

  it('shows the workspace name, branch and an accessible menu button', () => {
    makeApp()
    const w = mountSwitcher()
    const btn = w.find('[data-testid="ws-switch"]')
    expect(btn.text()).toContain('Acme Analytics')
    expect(btn.text()).toContain('main')
    expect(btn.attributes('aria-haspopup')).toBe('menu')
    expect(btn.attributes('aria-expanded')).toBe('false')
    expect(btn.attributes('aria-label')).toBe('Workspace: Acme Analytics. Switch workspace')
    w.unmount()
  })

  it('opens a menu with current, recents (missing disabled), Open, New and Manage; focus lands on the current item', async () => {
    makeApp()
    const w = mountSwitcher()
    await w.find('[data-testid="ws-switch"]').trigger('click')
    await flushPromises()
    const menu = w.find('[role="menu"]')
    expect(menu.exists()).toBe(true)
    expect(menu.text()).toContain('Current workspace')
    expect(menu.text()).toContain('Harbor Lane Hardware')
    expect(menu.text()).toContain('Switching saves your open notebooks first.')
    const old = w.findAll('[data-testid="ws-recent"]').find((x) => x.text().includes('Old Ledger'))!
    expect(old.attributes('aria-disabled')).toBe('true')
    expect(old.text()).toContain('Folder not found')
    expect((document.activeElement as HTMLElement).getAttribute('aria-checked')).toBe('true')
    expect(w.find('[data-testid="ws-open-folder"]').exists()).toBe(true)
    expect(w.find('[data-testid="ws-new"]').exists()).toBe(true)
    expect(w.find('[data-testid="ws-manage"]').exists()).toBe(true)
    w.unmount()
  })

  it('arrow keys move focus, Escape closes the menu and returns focus to the button', async () => {
    makeApp()
    const w = mountSwitcher()
    await w.find('[data-testid="ws-switch"]').trigger('click')
    await flushPromises()
    const menu = w.find('[role="menu"]')
    const first = document.activeElement
    await menu.trigger('keydown', { key: 'ArrowDown' })
    expect(document.activeElement).not.toBe(first)
    await menu.trigger('keydown', { key: 'Escape' })
    await flushPromises()
    expect(w.find('[role="menu"]').exists()).toBe(false)
    expect(document.activeElement).toBe(w.find('[data-testid="ws-switch"]').element)
    w.unmount()
  })

  it('switching flushes pending notebook saves first, then opens the other workspace', async () => {
    const calls: string[] = []
    const app = makeApp({ OpenWorkspace: vi.fn(async (p: string) => { calls.push('open'); return { status: 'opened', path: p, info: { ...INFO, root: p, name: 'Harbor Lane Hardware' } } }) })
    register(() => 'nb', async () => { calls.push('flush') })
    const w = mountSwitcher()
    await w.find('[data-testid="ws-switch"]').trigger('click')
    await flushPromises()
    await w.findAll('[data-testid="ws-recent"]').find((x) => x.text().includes('Harbor'))!.trigger('click')
    await flushPromises()
    expect(calls).toEqual(['flush', 'open'])
    expect(app.OpenWorkspace).toHaveBeenCalledWith('/w/harbor')
    expect(workspaceInfo.value?.name).toBe('Harbor Lane Hardware')
    expect(switcherOpen.value).toBe(false)
    w.unmount()
  })

  it('a missing workspace cannot be switched to', async () => {
    const app = makeApp()
    const w = mountSwitcher()
    await w.find('[data-testid="ws-switch"]').trigger('click')
    await flushPromises()
    await w.findAll('[data-testid="ws-recent"]').find((x) => x.text().includes('Old Ledger'))!.trigger('click')
    await flushPromises()
    expect(app.OpenWorkspace).not.toHaveBeenCalled()
    w.unmount()
  })

  it('New workspace… opens the dialog; Manage workspaces… closes to the launcher', async () => {
    const app = makeApp()
    const w = mountSwitcher()
    await w.find('[data-testid="ws-switch"]').trigger('click')
    await flushPromises()
    await w.find('[data-testid="ws-new"]').trigger('click')
    expect(newWorkspaceOpen.value).toBe(true)

    await w.find('[data-testid="ws-switch"]').trigger('click')
    await flushPromises()
    await w.find('[data-testid="ws-manage"]').trigger('click')
    await flushPromises()
    expect(app.CloseWorkspace).toHaveBeenCalled()
    expect(workspaceInfo.value).toBeNull()
    w.unmount()
  })

  it('shows the error on the Launcher when a failed switch leaves nothing open', async () => {
    makeApp({
      OpenWorkspace: vi.fn(async () => { throw new Error('nope. Acme could not be reopened either, so nothing is open') }),
      IsWorkspaceOpen: vi.fn(async () => false),
    })
    const w = mountSwitcher()
    await w.find('[data-testid="ws-switch"]').trigger('click')
    await flushPromises()
    await w.findAll('[data-testid="ws-recent"]').find((x) => x.text().includes('Harbor'))!.trigger('click')
    await flushPromises()
    expect(w.emitted('error')).toBeUndefined()
    expect(launcherNotice.value).toContain('nothing is open')
    w.unmount()
  })

  it('emits an error when the switch fails', async () => {
    makeApp({
      OpenWorkspace: vi.fn(async () => { throw new Error('Couldn\'t open this workspace: nope') }),
      GetWorkspaceInfo: vi.fn(async () => INFO),
    })
    const w = mountSwitcher()
    await w.find('[data-testid="ws-switch"]').trigger('click')
    await flushPromises()
    await w.findAll('[data-testid="ws-recent"]').find((x) => x.text().includes('Harbor'))!.trigger('click')
    await flushPromises()
    expect(w.emitted('error')?.[0]?.[0]).toContain('nope')
    w.unmount()
  })
})
