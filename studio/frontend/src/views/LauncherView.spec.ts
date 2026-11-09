import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {}, EventsOff: () => {} }))

import LauncherView from './LauncherView.vue'
import { workspaceInfo, newWorkspaceOpen, launcherNotice, _resetForTests } from '../composables/useWorkspace'

const R = (over: Partial<RecentWorkspace> = {}): RecentWorkspace => ({
  path: '/w/acme', id: 'i1', name: 'Acme Analytics', lastOpened: new Date(Date.now() - 2 * 3600e3).toISOString(),
  gitBranch: 'main', missing: false, ...over,
})

function makeApp(recents: RecentWorkspace[], over: Record<string, any> = {}) {
  const app = {
    ListRecentWorkspaces: vi.fn(async () => recents),
    OpenWorkspace: vi.fn(async (path: string) => ({ status: 'opened', path, info: { root: path, name: 'x', sessionId: 's' } })),
    RemoveRecentWorkspace: vi.fn(async (path: string) => recents.find((r) => r.path === path) ?? null),
    RestoreRecentWorkspace: vi.fn(async () => {}),
    SelectWorkspaceFolder: vi.fn(async () => ''),
    ChooseFolder: vi.fn(async () => ''),
    RelocateRecentWorkspace: vi.fn(async () => {}),
    IsWorkspaceOpen: vi.fn(async () => false),
    ...over,
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

const mountView = () => mount(LauncherView, { attachTo: document.body, global: { stubs: { Icon: true } } })

describe('LauncherView', () => {
  beforeEach(() => { _resetForTests(); document.body.innerHTML = '' })

  it('first run: the empty state offers New workspace, Open folder and Import .slaw (a bundle, not a legacy container)', async () => {
    makeApp([])
    const w = mountView()
    await flushPromises()
    expect(w.text()).toContain('Pick a folder to start')
    expect(w.find('[data-testid="new-workspace"]').exists()).toBe(true)
    expect(w.find('[data-testid="open-folder"]').exists()).toBe(true)
    expect(w.find('[data-testid="import-bundle"]').exists()).toBe(true)
    expect(w.text()).not.toMatch(/legacy/i)
    expect(w.text()).toContain('Notebooks, live views, pages')
    expect(w.text()).toContain('Library')
    await w.find('[data-testid="new-workspace"]').trigger('click')
    expect(newWorkspaceOpen.value).toBe(true)
    w.unmount()
  })

  it('lists recents with name, path, branch and a labelled row; the most recent row is focused', async () => {
    makeApp([R(), R({ path: '/w/harbor', name: 'Harbor Lane', gitBranch: '' })])
    const w = mountView()
    await flushPromises()
    const rows = w.findAll('[data-testid="recent-row"]')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('Acme Analytics')
    expect(rows[0].text()).toContain('/w/acme')
    expect(rows[0].text()).toContain('main')
    expect(rows[1].text()).toContain('no git')
    expect(rows[0].attributes('aria-label')).toBe('Open Acme Analytics, last opened 2 hours ago, branch main')
    expect(document.activeElement).toBe(rows[0].element)
    w.unmount()
  })

  it('Enter/click opens the workspace; Up/Down move between rows', async () => {
    const app = makeApp([R(), R({ path: '/w/harbor', name: 'Harbor Lane' })])
    const w = mountView()
    await flushPromises()
    const rows = w.findAll('[data-testid="recent-row"]')
    await rows[0].trigger('keydown', { key: 'ArrowDown' })
    expect(document.activeElement).toBe(rows[1].element)
    await rows[1].trigger('keydown', { key: 'ArrowUp' })
    expect(document.activeElement).toBe(rows[0].element)
    await rows[1].trigger('click')
    await flushPromises()
    expect(app.OpenWorkspace).toHaveBeenCalledWith('/w/harbor')
    expect(workspaceInfo.value?.root).toBe('/w/harbor')
    w.unmount()
  })

  it('a missing folder keeps its row, is not openable, and says why; Locate re-points it', async () => {
    const app = makeApp([R({ missing: true, name: 'Old Ledger', path: '/Volumes/Backup/ledger' })], { ChooseFolder: vi.fn(async () => '/new/place') })
    const w = mountView()
    await flushPromises()
    const row = w.find('[data-testid="recent-row"]')
    expect((row.element as HTMLButtonElement).disabled).toBe(true)
    expect(row.text()).toContain('Folder not found')
    expect(w.text()).toContain("Old Ledger's folder isn't there anymore")
    expect(row.attributes('aria-describedby')).toBe('missing-help')
    await w.find('.recent-li .linkbtn').trigger('click')
    await flushPromises()
    expect(app.RelocateRecentWorkspace).toHaveBeenCalledWith('/Volumes/Backup/ledger', '/new/place')
    w.unmount()
  })

  it('Delete removes the row from the list and offers Undo, which restores it', async () => {
    const entry = R()
    const app = makeApp([entry, R({ path: '/w/harbor', name: 'Harbor Lane' })])
    const w = mountView()
    await flushPromises()
    await w.findAll('[data-testid="recent-row"]')[0].trigger('keydown', { key: 'Delete' })
    await flushPromises()
    expect(app.RemoveRecentWorkspace).toHaveBeenCalledWith('/w/acme')
    expect(w.findAll('[data-testid="recent-row"]')).toHaveLength(1)
    const toast = w.find('[data-testid="undo-toast"]')
    expect(toast.text()).toContain('Removed Acme Analytics')
    await toast.find('button').trigger('click')
    await flushPromises()
    expect(app.RestoreRecentWorkspace).toHaveBeenCalledWith(entry)
    w.unmount()
  })

  it('shows the plain reason when opening fails', async () => {
    makeApp([R()], { OpenWorkspace: vi.fn(async () => { throw new Error('Couldn\'t open this workspace: slaw.yaml is not valid') }) })
    const w = mountView()
    await flushPromises()
    await w.find('[data-testid="recent-row"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="launcher-error"]').text()).toContain('slaw.yaml is not valid')
    w.unmount()
  })

  it('shows a notice when another program took the workspace', async () => {
    makeApp([])
    launcherNotice.value = 'Another program asked Slaw to let go.'
    const w = mountView()
    await flushPromises()
    expect(w.find('[data-testid="launcher-notice"]').text()).toContain('let go')
    w.unmount()
  })
})
