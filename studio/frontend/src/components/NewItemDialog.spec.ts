// NewItemDialog (docs/plans/library.md L6): the picker's keyboard flow, the
// per-type rules and "Creates" lines, and the binding called per type.
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import NewItemDialog from './NewItemDialog.vue'
import pickerSrc from './TypePicker.vue?raw'

function app(over: Record<string, any> = {}) {
  const go = { NewArtifact: vi.fn(async () => ({})), CreateNotebook: vi.fn(async () => ({ id: 'nb1' })), ...over }
  ;(window as any).go = { main: { App: go } }
  return go
}
function open(initialType?: string) {
  return mount(NewItemDialog, { props: { visible: true, initialType: initialType as any }, attachTo: document.body, global: { stubs: { Icon: true } } })
}
const checked = (w: any) => w.findAll('[role=radio]').filter((r: any) => r.attributes('aria-checked') === 'true').map((r: any) => r.attributes('data-type'))
async function toName(w: any) { await w.get('[data-testid=new-next]').trigger('click'); await flushPromises() }

describe('NewItemDialog step 1', () => {
  it('is a labelled radiogroup with a roving tabindex, preselected card focused', async () => {
    app()
    const w = open('live')
    await flushPromises()
    expect(w.get('[role=radiogroup]').attributes('aria-label')).toBeTruthy()
    const radios = w.findAll('[role=radio]')
    expect(radios.map((r) => r.attributes('data-type'))).toEqual(['notebook', 'live', 'page'])
    expect(radios.every((r) => !!r.text())).toBe(true)
    expect(radios.map((r) => r.attributes('tabindex'))).toEqual(['-1', '0', '-1'])
    expect(document.activeElement).toBe(radios[1].element)
    w.unmount()
  })

  it('defaults to Notebook; arrows move and wrap, Space (click) selects, Enter advances', async () => {
    app()
    const w = open()
    await flushPromises()
    expect(checked(w)).toEqual(['notebook'])
    await w.get('[data-testid=type-notebook]').trigger('keydown', { key: 'ArrowRight' })
    expect(checked(w)).toEqual(['live'])
    await w.get('[data-testid=type-live]').trigger('keydown', { key: 'ArrowLeft' })
    await w.get('[data-testid=type-notebook]').trigger('keydown', { key: 'ArrowLeft' })
    expect(checked(w)).toEqual(['page'])
    await w.get('[data-testid=type-page]').trigger('keydown', { key: ' ' })
    expect(checked(w)).toEqual(['page'])
    await w.get('[data-testid=type-page]').trigger('keydown', { key: 'Home' })
    expect(checked(w)).toEqual(['notebook'])
    await w.get('[data-testid=type-notebook]').trigger('keydown', { key: 'End' })
    expect(checked(w)).toEqual(['page'])
    await w.get('[data-testid=type-page]').trigger('keydown', { key: 'ArrowLeft' })
    await w.get('[data-testid=type-live]').trigger('keydown', { key: 'ArrowLeft' })
    await w.get('[data-testid=type-notebook]').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(w.find('[data-testid=new-item-form]').exists()).toBe(true)
    expect(document.activeElement).toBe(w.get('[data-testid=new-item-field]').element)
    w.unmount()
  })

  it('shows the script checkbox only for Page, with the badge once ticked', async () => {
    app()
    const w = open('live')
    expect(w.find('[data-testid=with-script]').exists()).toBe(false)
    await w.get('[data-testid=type-page]').trigger('click')
    expect(w.get('[data-testid=with-script]').element.closest('[role=radiogroup]')).toBeNull()
    expect(w.find('[data-testid=badge-page-script]').exists()).toBe(false)
    await w.get('[data-testid=with-script]').setValue(true)
    expect(w.get('[data-testid=badge-page-script]').text()).toBe('Runs after you allow it')
    await w.get('[data-testid=type-live]').trigger('click')
    expect(w.find('[data-testid=with-script]').exists()).toBe(false)
    w.unmount()
  })

  it('Advanced adds Chart script to the same group, reachable by keyboard', async () => {
    app()
    const w = open('page')
    await w.get('[data-testid=advanced-toggle]').trigger('click')
    expect(w.findAll('[role=radiogroup] [role=radio]').map((r) => r.attributes('data-type'))).toEqual(['notebook', 'live', 'page', 'script'])
    expect(w.get('[data-testid=badge-script]').text()).toBe('Runs after you allow it')
    await w.get('[data-testid=type-page]').trigger('keydown', { key: 'ArrowRight' })
    expect(checked(w)).toEqual(['script'])
    w.unmount()
  })

  it('opens Advanced when preselected to script, and Escape closes', async () => {
    app()
    const w = open('script')
    expect(checked(w)).toEqual(['script'])
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(w.emitted('close')).toBeTruthy()
    w.unmount()
  })
})

describe('NewItemDialog a11y and focus', () => {
  it('the selected card keeps a visible focus ring on top of its selected shadow', () => {
    expect(pickerSrc).toMatch(/\.tp-card:focus-visible\s*{[^}]*var\(--sh-focus-ring\)/)
  })
  it('cards are named by their heading and described by their line', async () => {
    app()
    const w = open()
    const r = w.get('[data-testid=type-live]')
    expect(document.getElementById(r.attributes('aria-labelledby')!)!.textContent).toContain('Live view')
    expect(document.getElementById(r.attributes('aria-describedby')!)!.textContent).toContain('controls')
    w.unmount()
  })
  it('the step 2 field is aria-invalid with its error and Creates line described', async () => {
    app()
    const w = open('live')
    await toName(w)
    const f = w.get('[data-testid=new-item-field]')
    expect(f.attributes('aria-invalid')).toBe('false')
    await f.setValue('Bad Name')
    await w.get('form').trigger('submit')
    expect(f.attributes('aria-invalid')).toBe('true')
    expect(f.attributes('aria-describedby')).toBe('new-item-error new-item-creates')
    w.unmount()
  })
  it('Escape is ignored while a create is running', async () => {
    let release: (v?: unknown) => void = () => {}
    app({ NewArtifact: vi.fn(() => new Promise((r) => { release = r })) })
    const w = open('live')
    await toName(w)
    await w.get('[data-testid=new-item-field]').setValue('orders')
    await w.get('form').trigger('submit')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(w.emitted('close')).toBeUndefined()
    release()
    await flushPromises()
    w.unmount()
  })
  it('Tab wraps inside the dialog', async () => {
    app()
    const w = open('notebook')
    await flushPromises()
    ;(w.get('[data-testid=new-next]').element as HTMLElement).focus()
    const tab = new KeyboardEvent('keydown', { key: 'Tab', cancelable: true })
    document.dispatchEvent(tab)
    expect(tab.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(w.get('[role=radio][tabindex="0"]').element)
    w.unmount()
  })
})

describe('NewItemDialog step 2', () => {
  it('notebook: title rule, 200 note, no Allow line, CreateNotebook, lands on the notebook', async () => {
    const go = app()
    const w = open('notebook')
    await toName(w)
    expect(w.get('[data-testid=new-item-creates]').text()).toBe('Creates a notebook in notebooks/')
    expect(w.find('[data-testid=new-item-allow]').exists()).toBe(false)
    await w.get('[data-testid=new-item-field]').setValue('   ')
    await w.get('form').trigger('submit')
    expect(w.get('[data-testid=new-item-error]').text()).toBe('Give it a title')
    expect(go.CreateNotebook).not.toHaveBeenCalled()
    await w.get('[data-testid=new-item-field]').setValue('x'.repeat(201))
    expect(w.find('[data-testid=new-item-long]').exists()).toBe(true)
    await w.get('[data-testid=new-item-field]').setValue('Churn')
    expect(w.find('[data-testid=new-item-long]').exists()).toBe(false)
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(go.CreateNotebook).toHaveBeenCalledWith('Churn')
    expect(w.emitted('created')?.[0]).toEqual(['/library/notebook/nb1'])
    w.unmount()
  })

  it.each([
    ['live', false, 'a2ui', false],
    ['page', false, 'html', true],
    ['page', true, 'html-script', true],
    ['script', false, 'script', true],
  ])('%s (script %s) calls NewArtifact with %s', async (type, script, kind, allow) => {
    const go = app()
    const w = open(type)
    if (script) await w.get('[data-testid=with-script]').setValue(true)
    await toName(w)
    expect(w.find('[data-testid=new-item-allow]').exists()).toBe(allow)
    if (allow) expect(w.get('[data-testid=new-item-allow]').text()).toBe('It opens as source with an Allow button.')
    expect(w.get('[data-testid=new-item-creates]').text()).toContain('Creates artifacts/<name>/')
    await w.get('[data-testid=new-item-field]').setValue('Bad Name')
    await w.get('form').trigger('submit')
    expect(go.NewArtifact).not.toHaveBeenCalled()
    expect(w.get('[data-testid=new-item-error]').text()).toMatch(/lowercase/)
    await w.get('[data-testid=new-item-field]').setValue('weekly-sales')
    expect(w.get('[data-testid=new-item-creates]').text()).toContain('artifacts/weekly-sales/')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(go.NewArtifact).toHaveBeenCalledWith('weekly-sales', kind)
    expect(w.emitted('created')?.[0]).toEqual(['/library/artifact/weekly-sales'])
    w.unmount()
  })

  it('Back keeps the choice including the script tick', async () => {
    app()
    const w = open('page')
    await w.get('[data-testid=with-script]').setValue(true)
    await toName(w)
    await w.get('[data-testid=new-back]').trigger('click')
    await flushPromises()
    expect(checked(w)).toEqual(['page'])
    expect((w.get('[data-testid=with-script]').element as HTMLInputElement).checked).toBe(true)
    expect(document.activeElement).toBe(w.get('[data-testid=type-page]').element)
    w.unmount()
  })

  it('shows a Go refusal inline and stays open; a double submit creates once', async () => {
    let release: (v?: unknown) => void = () => {}
    const go = app({ NewArtifact: vi.fn(() => new Promise((r) => { release = r })) })
    const w = open('live')
    await toName(w)
    await w.get('[data-testid=new-item-field]').setValue('orders')
    await w.get('form').trigger('submit')
    await w.get('form').trigger('submit')
    expect(go.NewArtifact).toHaveBeenCalledTimes(1)
    release()
    await flushPromises()
    w.unmount()

    app({ NewArtifact: vi.fn(async () => { throw new Error('an artifact named "orders" already exists') }) })
    const w2 = open('live')
    await toName(w2)
    await w2.get('[data-testid=new-item-field]').setValue('orders')
    await w2.get('form').trigger('submit')
    await flushPromises()
    expect(w2.get('[data-testid=new-item-error]').text()).toContain('already exists')
    expect(w2.emitted('created')).toBeUndefined()
    w2.unmount()
  })
})

describe('NewItemDialog describe it (library L7)', () => {
  async function toDescribe(w: any) { await w.get('[data-testid=describe-row]').trigger('click'); await flushPromises() }

  it('the row is after the cards, Advanced and the checkbox in tab order', async () => {
    app()
    const w = open('page')
    const order = [...w.element.querySelectorAll('[role=radio], [data-testid=with-script], [data-testid=advanced-toggle], [data-testid=describe-row]')].map((e: any) => e.dataset.testid)
    expect(order[order.length - 1]).toBe('describe-row')
    w.unmount()
  })

  it('uses the chosen type without a second picker; Change goes back keeping it', async () => {
    app()
    const w = open('live')
    await toDescribe(w)
    expect(w.get('[data-testid=describe-type]').text()).toContain('live view')
    expect(w.find('[role=radiogroup]').exists()).toBe(false)
    expect(document.activeElement).toBe(w.get('[data-testid=describe-box]').element)
    await w.get('[data-testid=describe-change]').trigger('click')
    await flushPromises()
    expect(checked(w)).toEqual(['live'])
    w.unmount()
  })

  it('for a live view: opens the assistant panel with an unsent draft, creates nothing, and closes', async () => {
    const go = app()
    const seen = vi.fn()
    window.addEventListener('slaw:open-agent', seen)
    const w = open('live')
    await toDescribe(w)
    expect(w.get('[data-testid=describe-submit]').attributes('disabled')).toBeDefined()
    await w.get('[data-testid=describe-box]').setValue('weekly sales by region')
    await w.get('[data-testid=describe-form]').trigger('submit')
    await flushPromises()
    window.removeEventListener('slaw:open-agent', seen)
    const draft = (seen.mock.calls[0][0] as CustomEvent).detail.draft as string
    expect(draft).toContain('live view')
    expect(draft).toContain('write_artifact')
    expect(draft).toContain('weekly sales by region')
    expect(go.NewArtifact).not.toHaveBeenCalled()
    expect(go.CreateNotebook).not.toHaveBeenCalled()
    expect(w.emitted('close')).toBeTruthy()
    w.unmount()
  })

  it('a page with the script box ticked says so in the draft', async () => {
    app()
    const seen = vi.fn()
    window.addEventListener('slaw:open-agent', seen)
    const w = open('page')
    await w.get('[data-testid=with-script]').setValue(true)
    await toDescribe(w)
    await w.get('[data-testid=describe-box]').setValue('board summary')
    await w.get('[data-testid=describe-form]').trigger('submit')
    window.removeEventListener('slaw:open-agent', seen)
    const draft = (seen.mock.calls[0][0] as CustomEvent).detail.draft as string
    expect(draft).toContain('view: html')
    expect(draft).toContain('script')
    w.unmount()
  })

  it('for a notebook: needs a title, creates it, sets the pending ask and routes to it', async () => {
    const go = app()
    const w = open('notebook')
    await toDescribe(w)
    expect(document.activeElement).toBe(w.get('[data-testid=new-item-field]').element)
    await w.get('[data-testid=describe-box]').setValue('revenue by region')
    await w.get('[data-testid=describe-form]').trigger('submit')
    await flushPromises()
    expect(go.CreateNotebook).not.toHaveBeenCalled()
    expect(w.get('[data-testid=describe-error]').text()).toBe('Give it a title')
    await w.get('[data-testid=new-item-field]').setValue('Revenue')
    await Promise.all([w.get('[data-testid=describe-form]').trigger('submit'), w.get('[data-testid=describe-form]').trigger('submit')])
    await flushPromises()
    expect(go.CreateNotebook).toHaveBeenCalledTimes(1)
    expect(w.emitted('created')![0]).toEqual(['/library/notebook/nb1'])
    const { takePendingAsk } = await import('../composables/usePendingAsk')
    expect(takePendingAsk('nb1')).toBe('revenue by region')
    w.unmount()
  })

  it('a failed notebook create shows the error and sets no pending ask', async () => {
    app({ CreateNotebook: vi.fn(async () => { throw new Error('disk full') }) })
    const w = open('notebook')
    await toDescribe(w)
    await w.get('[data-testid=new-item-field]').setValue('T')
    await w.get('[data-testid=describe-box]').setValue('q')
    await w.get('[data-testid=describe-form]').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid=describe-error]').text()).toContain('disk full')
    expect(w.emitted('created')).toBeUndefined()
    w.unmount()
  })
})
