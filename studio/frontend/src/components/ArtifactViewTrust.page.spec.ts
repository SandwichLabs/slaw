// ArtifactViewTrust for an HTML page (#162): the files are shown, nothing is hosted until
// the person allows, an allowed static page goes to the script-less frame, and a scripted
// one is handed to its page window (ScriptedPage), never to a frame.
type MockApp = Partial<Record<keyof AppApi, any>>
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {} }))

import ArtifactViewTrust from './ArtifactViewTrust.vue'

const disclosure: PageDisclosure = {
  host: 'This page has no script. It shows inside Studio.',
  capabilities: [], network: '', credential: 'Slaw never asks for a password, key or token inside a page.', askProvider: '',
}

const status = (over: Partial<ArtifactViewStatus> = {}): ArtifactViewStatus => ({
  name: 'pg', kind: 'html', state: 'undecided', hash: 'b'.repeat(64), page_kind: 'static',
  manifest: 'version: 1\ntitle: Pg\nview: html\n', source: '', queries: ['big'], disclosure,
  files: [{ path: 'index.html.tmpl', role: 'entry', size: 12, text: '<p>hi</p>' }], ...over,
})

function makeApp(initial: ArtifactViewStatus) {
  let cur = initial
  const app: MockApp = {
    ArtifactViewState: vi.fn(async () => cur),
    AllowArtifactView: vi.fn(async () => (cur = status({ ...cur, state: 'allowed' }))),
    DenyArtifactView: vi.fn(async () => (cur = status({ ...cur, state: 'denied' }))),
    RevokeArtifactView: vi.fn(async () => (cur = status({ ...cur, state: 'undecided' }))),
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

const detail = { name: 'pg', title: 'Pg', params: [], queries: [], view_kind: 'html', defaults: {}, rev: 'r1', updated_at: '' } as unknown as ArtifactDetail

async function mountTrust() {
  const w = mount(ArtifactViewTrust, {
    props: { name: 'pg', detail, values: {}, refresh: 0 },
    global: { stubs: { Icon: true, RouterLink: { props: ['to'], template: '<a :href="JSON.stringify(to)"><slot /></a>' }, ScriptedPage: { template: '<div data-testid="scripted-page-stub" />' }, StaticPage: { template: '<div data-testid="static-page-stub" />' }, CustomView: { template: '<div data-testid="custom-view-stub" />' } } },
  })
  await flushPromises()
  return w
}

describe('ArtifactViewTrust, HTML page', () => {
  it('lists the files and hosts nothing until the person allows', async () => {
    makeApp(status())
    const w = await mountTrust()
    expect(w.text()).toContain('HTML page')
    expect(w.get('[data-testid="page-prompt-host"]').text()).toContain('This page has no script')
    expect(w.text()).not.toContain('runs code written by someone else')
    expect(w.findAll('.page-files__item').map((i) => i.get('.page-files__path').text())).toEqual(['artifact.yaml', 'index.html.tmpl'])
    expect(w.get('[data-testid="view-trust-state"]').text()).toContain('Not allowed yet')
    expect(w.find('[data-testid="static-page-stub"]').exists()).toBe(false)
    expect(w.find('[data-testid="custom-view-stub"]').exists()).toBe(false)
  })

  it('Allow sends the hash on screen, then hosts the static frame, never the script worker', async () => {
    const app = makeApp(status())
    const w = await mountTrust()
    await w.get('[data-testid="view-trust-allow"]').trigger('click')
    await flushPromises()
    expect(app.AllowArtifactView).toHaveBeenCalledWith('pg', 'b'.repeat(64))
    expect(w.find('[data-testid="static-page-stub"]').exists()).toBe(true)
    expect(w.find('[data-testid="custom-view-stub"]').exists()).toBe(false)
  })

  it('an allowed scripted page goes to its own window and hosts no frame', async () => {
    makeApp(status({ state: 'allowed', page_kind: 'scripted' }))
    const w = await mountTrust()
    expect(w.find('[data-testid="static-page-stub"]').exists()).toBe(false)
    expect(w.find('[data-testid="scripted-page-stub"]').exists()).toBe(true)
    expect(w.find('iframe').exists()).toBe(false)
  })

  it('the allow screen lists every query a script can run with its connection, the capabilities and the warnings', async () => {
    makeApp(status({
      page_kind: 'scripted',
      query_uses: [{ name: 'big', connection: 'prod_pg', used: true }, { name: 'secrets', connection: 'prod_pg', used: false }, { name: 'small', used: true }],
      disclosure: {
        host: 'This page has script.', network: 'On macOS a scripted page can open a network connection.', credential: disclosure.credential, askProvider: 'Anthropic (m)',
        capabilities: [{ name: 'state', text: 'state: keeps small notes' }, { name: 'ask', text: 'ask: This page can send text, which may include query results, to Anthropic (m).' }],
      },
    }))
    const w = await mountTrust()
    const qs = w.get('[data-testid="page-prompt-queries"]').text()
    expect(qs).toContain('big on prod_pg')
    expect(w.get('[data-testid="page-prompt-query-secrets"]').text()).toContain('secrets on prod_pg')
    expect(w.get('[data-testid="page-prompt-query-secrets"]').text()).toContain('script only')
    expect(w.get('[data-testid="page-prompt-query-small"]').text()).toContain('small on the workspace database')
    expect(w.get('[data-testid="page-prompt-cap-ask"]').text()).toContain('to Anthropic (m)')
    expect(w.get('[data-testid="page-prompt-cap-state"]').text()).toContain('keeps small notes')
    expect(w.get('[data-testid="page-prompt-network"]').text()).toContain('network connection')
    expect(w.get('[data-testid="page-prompt-credential"]').text()).toContain('never asks for a password')
    expect(w.text()).toContain('queries it can run')
  })

  it('fails closed without the prompt text: says so, lists the declared capabilities, and keeps Allow off', async () => {
    const app = makeApp(status({ page_kind: 'scripted', disclosure: undefined, capabilities: ['ask', 'download'] }))
    const w = await mountTrust()
    expect(w.get('[data-testid="page-prompt-missing"]').text()).toContain("Couldn't load what this page can do")
    expect(w.get('[data-testid="page-prompt-caps"]').text()).toContain('ask')
    expect(w.text()).not.toContain('no capabilities')
    expect(w.find('[data-testid="page-prompt-credential"]').exists()).toBe(false)
    expect(w.get('[data-testid="view-trust-allow"]').attributes('disabled')).toBeDefined()
    await w.get('[data-testid="view-trust-allow"]').trigger('click')
    expect(app.AllowArtifactView).not.toHaveBeenCalled()
  })

  it('shows no prompt once the page is allowed', async () => {
    makeApp(status({ state: 'allowed' }))
    const w = await mountTrust()
    expect(w.find('[data-testid="page-prompt"]').exists()).toBe(false)
  })

  it('a denied page says so and hosts nothing', async () => {
    makeApp(status({ state: 'denied' }))
    const w = await mountTrust()
    expect(w.get('[data-testid="view-trust-denied"]').text()).toContain('You denied this page')
    expect(w.find('[data-testid="static-page-stub"]').exists()).toBe(false)
  })
})

describe('ArtifactViewTrust, HTML page source', () => {
  it('folds the files into details once allowed, and makes the viewer only when opened', async () => {
    makeApp(status({ state: 'allowed' }))
    const w = await mountTrust()
    const box = w.get('[data-testid="view-trust-page-src"]')
    expect(box.element.tagName).toBe('DETAILS')
    expect(w.find('[data-testid="page-files"]').exists()).toBe(false)
    ;(box.element as HTMLDetailsElement).open = true
    await box.trigger('toggle')
    expect(w.find('[data-testid="page-files"]').exists()).toBe(true)
  })

  it('shows the files unfolded before the person decides', async () => {
    makeApp(status())
    const w = await mountTrust()
    expect(w.get('[data-testid="view-trust-page-src"]').element.tagName).toBe('DIV')
    expect(w.find('[data-testid="page-files"]').exists()).toBe(true)
  })

  it('lists the metrics the page reads and says their files are part of the allow (#282)', async () => {
    makeApp(status({
      metrics_used: [{ name: 'net_revenue', query: 'net_revenue_base', measure: 'sum(amount)', dimensions: ['region'] }, { name: 'ghost', missing: true }],
      metrics_note: 'It reads these metrics through their saved queries. Your allow covers their definitions as they are now: if a metric file or its saved query changes, Slaw asks again.',
    }))
    const w = await mountTrust()
    expect(w.get('[data-testid="metrics-read-net_revenue"]').text()).toContain('net_revenue_base')
    expect(w.get('[data-testid="metrics-read-measure-net_revenue"]').text()).toBe('sum(amount)')
    expect(w.get('[data-testid="metrics-read-net_revenue"]').text()).toContain('by region')
    expect(w.get('[data-testid="metrics-read-link-net_revenue"]').attributes('href')).toContain('metric')
    expect(w.get('[data-testid="metrics-read-ghost"]').text()).toContain('no such metric yet')
    expect(w.get('[data-testid="metrics-read-note"]').text()).toContain('Slaw asks again')
  })

  it('shows nothing about metrics for a page that calls none', async () => {
    makeApp(status())
    const w = await mountTrust()
    expect(w.find('[data-testid="metrics-read"]').exists()).toBe(false)
  })

  it('says a metric changed, not the page, when only what it reads moved (#282)', async () => {
    makeApp(status({
      state: 'changed', deps_changed: true, metrics_used: [{ name: 'net_revenue', query: 'base' }],
      metrics_note: 'Slaw asks again.',
    }))
    const w = await mountTrust()
    const line = w.get('[data-testid="view-trust-line"]').text()
    expect(line).toContain('A metric this page reads changed (net_revenue)')
    expect(line).not.toContain('Read the files below')
  })

  it('says the page changed, once, when its own files changed', async () => {
    makeApp(status({ state: 'changed' }))
    const w = await mountTrust()
    const line = w.get('[data-testid="view-trust-line"]').text()
    expect(line).toContain('changed since you allowed it')
    expect(line.match(/before you allow/g)).toHaveLength(1)
  })
})
