// ScriptedPage (#162, H4): an allowed scripted page lives in its own native window. This
// panel opens it, follows its state, and hosts nothing from the page.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { ARTIFACT_RUNTIME } from '../a2ui/artifactRuntime'

type Handler = (p?: unknown) => void
const handlers: Record<string, Handler> = {}
vi.mock('../wailsjs/runtime/runtime', () => ({
  EventsOn: (name: string, cb: Handler) => { handlers[name] = cb; return () => { delete handlers[name] } },
}))
vi.mock('../lib/wails', () => ({ hasWailsRuntime: () => true }))

import ScriptedPage from './ScriptedPage.vue'

const detail = {
  name: 'pg', title: 'Sales', description: '', queries: [], view_kind: 'html', defaults: { n: 3 }, rev: 'r', updated_at: '',
  params: [{ name: 'n', type: 'integer', default: '3' }],
} as unknown as ArtifactDetail

const app = {
  PageWindowSupport: vi.fn(async () => ''),
  OpenArtifactPageWindow: vi.fn(async (name: string) => ({ name, hash: 'h1' })),
  ClosePageWindow: vi.fn(async () => {}),
}

async function mountPage(values: Record<string, number> = { n: 3 }) {
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  const runtime = {
    result: () => ({ loading: false, error: '', data: null, fetched: true, truncated: false }),
    params: { param: (n: string) => detail.params.find((p) => p.name === n), display: () => 3, error: () => '', isDefault: () => true, set: vi.fn(), reset: vi.fn() },
  }
  const w = mount(ScriptedPage, { props: { name: 'pg', detail, values }, global: { provide: { [ARTIFACT_RUNTIME as symbol]: runtime }, stubs: { Icon: true } } })
  await flushPromises()
  return w
}

beforeEach(() => {
  for (const k of Object.keys(handlers)) delete handlers[k]
  app.PageWindowSupport.mockResolvedValue('')
  app.OpenArtifactPageWindow.mockClear()
  app.ClosePageWindow.mockClear()
})

describe('ScriptedPage', () => {
  it('offers Open page, says it runs in its own window, and hosts no frame or script', async () => {
    const w = await mountPage()
    expect(w.get('[data-testid="scripted-page-open"]').text()).toBe('Open page')
    expect(w.get('[data-testid="scripted-page-about"]').text()).toContain('opens in its own window')
    expect(w.get('[data-testid="scripted-page-about"]').text()).toContain('never asks you for a password')
    expect(w.find('iframe').exists()).toBe(false)
    expect(w.find('script').exists()).toBe(false)
  })

  it('opens the window with the controls\' query string and the theme, then follows its events', async () => {
    document.documentElement.style.setProperty('--slaw-bg', '#112233')
    document.documentElement.dataset.themeType = 'dark'
    const w = await mountPage({ n: 7 })
    await w.get('[data-testid="scripted-page-open"]').trigger('click')
    await flushPromises()
    expect(app.OpenArtifactPageWindow).toHaveBeenCalledTimes(1)
    const [name, query, theme] = app.OpenArtifactPageWindow.mock.calls[0] as unknown as [string, string, Record<string, string>]
    expect(name).toBe('pg')
    expect(query).toBe('n=7')
    expect(theme['--slaw-bg']).toBe('#112233')
    expect(theme['--slaw-theme-type']).toBe('dark')
    expect(w.find('[data-testid="scripted-page-opening"]').exists()).toBe(true)

    handlers['pagewindow:state']({ name: 'pg', state: 'open', hash: 'h1' })
    await flushPromises()
    expect(w.get('[data-testid="scripted-page-is-open"]').text()).toContain('own window')
    await w.get('[data-testid="scripted-page-close"]').trigger('click')
    expect(app.ClosePageWindow).toHaveBeenCalledWith('pg')
  })

  it('says what the window can reach on this platform', async () => {
    const w = await mountPage()
    const note = w.get('[data-testid="scripted-page-network"]')
    expect(note.text()).toBe('The window has no network of its own.')
  })

  it('a reopen does not mistake the old window\'s closing for the new one\'s', async () => {
    const w = await mountPage()
    await w.get('[data-testid="scripted-page-open"]').trigger('click')
    await flushPromises()
    handlers['pagewindow:state']({ name: 'pg', state: 'closed', hash: 'h1' })
    await flushPromises()
    expect(w.find('[data-testid="scripted-page-opening"]').exists()).toBe(true)
    expect(w.find('[data-testid="scripted-page-open"]').exists()).toBe(false)
  })

  it('says "This page stopped responding" and offers Reopen', async () => {
    const w = await mountPage()
    await w.get('[data-testid="scripted-page-open"]').trigger('click')
    await flushPromises()
    handlers['pagewindow:state']({ name: 'pg', state: 'stopped', hash: 'h1' })
    await flushPromises()
    expect(w.get('[data-testid="scripted-page-stopped"]').text()).toContain('This page stopped responding')
    await w.get('[data-testid="scripted-page-reopen"]').trigger('click')
    expect(app.OpenArtifactPageWindow).toHaveBeenCalledTimes(2)
  })

  it('says a setup failure is a setup failure, not a hang, and offers Reopen', async () => {
    const w = await mountPage()
    await w.get('[data-testid="scripted-page-open"]').trigger('click')
    await flushPromises()
    handlers['pagewindow:state']({ name: 'pg', state: 'setup', hash: 'h1' })
    await flushPromises()
    expect(w.get('[data-testid="scripted-page-setup"]').text()).toBe("This page window couldn't be set up on this Mac.")
    expect(w.find('[data-testid="scripted-page-stopped"]').exists()).toBe(false)
    expect(w.find('[data-testid="scripted-page-reopen"]').exists()).toBe(true)
  })

  it('says "This page changed" and offers Reopen, which asks Go to check again', async () => {
    const w = await mountPage()
    await w.get('[data-testid="scripted-page-open"]').trigger('click')
    await flushPromises()
    handlers['pagewindow:state']({ name: 'pg', state: 'changed', hash: 'h1' })
    await flushPromises()
    expect(w.get('[data-testid="scripted-page-changed"]').text()).toContain('This page changed')
    expect(w.find('[data-testid="scripted-page-reopen"]').exists()).toBe(true)
  })

  it('ignores another artifact\'s window and an older window\'s end', async () => {
    const w = await mountPage()
    await w.get('[data-testid="scripted-page-open"]').trigger('click')
    await flushPromises()
    handlers['pagewindow:state']({ name: 'other', state: 'stopped', hash: 'h1' })
    handlers['pagewindow:state']({ name: 'pg', state: 'stopped', hash: 'old' })
    await flushPromises()
    expect(w.find('[data-testid="scripted-page-stopped"]').exists()).toBe(false)
  })

  it('shows Go\'s fixed reason when this system cannot open a page window, and no Open button', async () => {
    app.PageWindowSupport.mockResolvedValue('this system cannot isolate a page window (it needs bubblewrap and unprivileged user namespaces), so the page cannot be opened')
    const w = await mountPage()
    expect(w.get('[data-testid="scripted-page-unavailable"]').text()).toContain('cannot isolate a page window')
    expect(w.find('[data-testid="scripted-page-open"]').exists()).toBe(false)
  })

  it('shows a refusal from Go as text', async () => {
    app.OpenArtifactPageWindow.mockRejectedValueOnce(new Error('this view is not allowed to run'))
    const w = await mountPage()
    await w.get('[data-testid="scripted-page-open"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid="scripted-page-error"]').text()).toBe('this view is not allowed to run')
    expect(w.find('[data-testid="scripted-page-open"]').exists()).toBe(true)
  })
})
