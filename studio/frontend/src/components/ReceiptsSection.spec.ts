import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: () => () => {}, EventsOff: () => {} }))

import ReceiptsSection from './ReceiptsSection.vue'
import { usageLine, formatBytes } from '../composables/useReceiptSettings'
import { resolveConfirmChoice, confirmVisible, confirmMessage } from '../composables/useConfirm'

function info(over: Partial<ReceiptSettingsInfo> = {}): ReceiptSettingsInfo {
  const d = { keepParams: true, days: 30, maxCount: 20000, maxMB: 64 }
  return {
    settings: { ...d }, effective: { ...d }, defaults: { ...d },
    floors: { keepParams: true, days: 1, maxCount: 100, maxMB: 1 },
    ceilings: { keepParams: true, days: 365, maxCount: 200000, maxMB: 1024 },
    usage: { count: 12, bytes: 3 << 20, oldest: '2026-10-20T10:00:00Z' },
    available: true,
    ...over,
  }
}

function makeApp(first: ReceiptSettingsInfo, after?: ReceiptSettingsInfo) {
  let current = first
  const app = {
    GetReceiptSettings: vi.fn(async () => current),
    SetReceiptSettings: vi.fn(async () => { if (after) current = after }),
    ClearReceipts: vi.fn(async () => { current = info({ usage: { count: 0, bytes: 0, oldest: '' } }) }),
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  return app
}

const mountIt = async () => {
  const w = mount(ReceiptsSection, { global: { stubs: { Icon: true } } })
  await flushPromises()
  return w
}

describe('ReceiptsSection', () => {
  beforeEach(() => { confirmVisible.value = false })

  it('shows the scope, the values in force, the help line and the usage', async () => {
    makeApp(info())
    const w = await mountIt()
    expect(w.text()).toContain('This computer')
    expect(w.text()).toContain('.slaw-local/receipts/')
    expect((w.find('[data-testid="receipt-keep-params"]').element as HTMLInputElement).checked).toBe(true)
    expect((w.find('[data-testid="receipt-days"]').element as HTMLInputElement).value).toBe('30')
    expect((w.find('[data-testid="receipt-count"]').element as HTMLInputElement).value).toBe('20000')
    expect((w.find('[data-testid="receipt-mb"]').element as HTMLInputElement).value).toBe('64')
    const help = w.find('[data-testid="receipt-limits-help"]').text()
    expect(help).toContain('Defaults: 30 days')
    expect(help).toContain('1 to 365 days')
    expect(w.find('[data-testid="receipt-usage"]').text()).toMatch(/^12 receipts · 3\.0 MB · oldest from /)
    expect(w.text()).toContain('slaw receipts settings')
    expect(w.text()).toContain('.receipts on')
  })

  it('saves what was typed, then shows the value Go put in force', async () => {
    const app = makeApp(info(), info({ effective: { keepParams: false, days: 365, maxCount: 100, maxMB: 8 } }))
    const w = await mountIt()
    expect(w.find('[data-testid="receipt-save"]').attributes('disabled')).toBeDefined()
    await w.find('[data-testid="receipt-keep-params"]').setValue(false)
    await w.find('[data-testid="receipt-days"]').setValue('9999')
    await w.find('[data-testid="receipt-count"]').setValue('5')
    await w.find('[data-testid="receipt-mb"]').setValue('8')
    await w.find('[data-testid="receipt-save"]').trigger('click')
    await flushPromises()
    expect(app.SetReceiptSettings).toHaveBeenCalledWith({ keepParams: false, days: 9999, maxCount: 5, maxMB: 8 })
    expect((w.find('[data-testid="receipt-days"]').element as HTMLInputElement).value).toBe('365')
    expect((w.find('[data-testid="receipt-count"]').element as HTMLInputElement).value).toBe('100')
    expect(w.find('[data-testid="receipt-message"]').text()).toContain('Saved')
  })

  it('a blank box saves as the default (0), not as a mistake', async () => {
    const app = makeApp(info())
    const w = await mountIt()
    await w.find('[data-testid="receipt-days"]').setValue('')
    await w.find('[data-testid="receipt-save"]').trigger('click')
    await flushPromises()
    expect(app.SetReceiptSettings).toHaveBeenCalledWith({ keepParams: true, days: 0, maxCount: 20000, maxMB: 64 })
  })

  it('Delete all receipts asks first and does nothing on Cancel', async () => {
    const app = makeApp(info())
    const w = await mountIt()
    await w.find('[data-testid="receipt-clear"]').trigger('click')
    await flushPromises()
    expect(confirmVisible.value).toBe(true)
    expect(confirmMessage.value).toContain('12 receipts')
    resolveConfirmChoice('cancel')
    await flushPromises()
    expect(app.ClearReceipts).not.toHaveBeenCalled()
  })

  it('confirming deletes and refreshes the usage line', async () => {
    const app = makeApp(info())
    const w = await mountIt()
    await w.find('[data-testid="receipt-clear"]').trigger('click')
    await flushPromises()
    resolveConfirmChoice('confirm')
    await flushPromises()
    expect(app.ClearReceipts).toHaveBeenCalledTimes(1)
    expect(w.find('[data-testid="receipt-usage"]').text()).toMatch(/^0 receipts · 0 bytes$/)
    expect(w.find('[data-testid="receipt-clear"]').attributes('disabled')).toBeDefined()
  })

  it('says so when the folder cannot be used', async () => {
    makeApp(info({ available: false }))
    const w = await mountIt()
    expect(w.find('[data-testid="receipt-unavailable"]').exists()).toBe(true)
    expect(w.find('[data-testid="receipt-save"]').exists()).toBe(false)
  })

  it('shows a save error', async () => {
    const app = makeApp(info())
    app.SetReceiptSettings.mockRejectedValueOnce(new Error('Your saved settings for this workspace were made for a different folder.'))
    const w = await mountIt()
    await w.find('[data-testid="receipt-days"]').setValue('7')
    await w.find('[data-testid="receipt-save"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="receipt-error"]').text()).toContain('different folder')
  })
})

describe('usage formatting', () => {
  it('formats bytes and the usage line', () => {
    expect(formatBytes(512)).toBe('512 bytes')
    expect(formatBytes(2048)).toBe('2.0 KB')
    expect(usageLine({ count: 1, bytes: 0, oldest: '' })).toBe('1 receipt · 0 bytes')
  })
})
