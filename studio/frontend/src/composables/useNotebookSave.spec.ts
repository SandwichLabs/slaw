// useNotebookSave — the notebook's save path (F10): the 800ms debounce, the flush
// that runs on close, and the conflict flow. NotebookView.spec.ts drives the same
// code through the component; these pin it directly.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ref, type Ref } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { useNotebookSave } from './useNotebookSave'
import { newCellState, type CellState } from '../lib/notebookCells'

function setup(app: Record<string, any> = {}) {
  const calls = { SaveNotebook: vi.fn(), ResolveConflict: vi.fn() }
  const api = {
    SaveNotebook: calls.SaveNotebook.mockImplementation(async (nb: any) => ({ notebook: { ...nb, rev: 'rev-' + (calls.SaveNotebook.mock.calls.length + 1) }, warnings: [] })),
    ResolveConflict: calls.ResolveConflict.mockImplementation(async () => ({ rev: 'rev-kept', title: 'My copy' })),
    GetNotebook: vi.fn(async () => ({ id: 'nb1', title: 'T', cells: [], rev: 'disk-rev' })),
    ...app,
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: api } }
  const title = ref('Notebook')
  // Cast: ref() would unwrap the CodeMirror EditorView type inside each cell's editor handle.
  const cells = ref([newCellState({ id: 'c1', type: 'sql', source: 'SELECT 1', view: 'table' })]) as Ref<CellState[]>
  const error = vi.fn()
  const status = vi.fn()
  const reload = vi.fn(async () => {})
  const save = useNotebookSave({ title, cells, error, status, reload })
  // a loaded notebook: the way NotebookView's load() hands it over
  save.begin('nb1')
  save.adopt('session-1', 'rev-0')
  save.end('nb1')
  return { save, title, cells, error, status, reload, api }
}

describe('useNotebookSave', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  describe('debounce', () => {
    it('writes once, 800ms after the last edit, carrying the loaded rev and session', async () => {
      const { save, cells, api } = setup()
      cells.value[0].source = 'SELECT 2'
      save.scheduleSave()
      vi.advanceTimersByTime(700)
      cells.value[0].source = 'SELECT 3'
      save.scheduleSave() // restarts the clock
      vi.advanceTimersByTime(700)
      expect(api.SaveNotebook).not.toHaveBeenCalled()
      vi.advanceTimersByTime(100)
      await flushPromises()
      expect(api.SaveNotebook).toHaveBeenCalledTimes(1)
      const [payload, session] = api.SaveNotebook.mock.calls[0]
      expect(payload).toMatchObject({ id: 'nb1', rev: 'rev-0', cells: [{ id: 'c1', source: 'SELECT 3' }] })
      expect(session).toBe('session-1')
      expect(save.saveState.value).toBe('saved')
    })

    it('does not schedule while loading (autosave is suppressed) or while a conflict shows', async () => {
      const { save, api } = setup()
      save.begin('nb1') // a (re)load starts: suppressed again
      save.scheduleSave()
      vi.advanceTimersByTime(2000)
      expect(api.SaveNotebook).not.toHaveBeenCalled()
      save.end('nb1')
      save.raiseConflict(true)
      save.scheduleSave()
      vi.advanceTimersByTime(2000)
      expect(api.SaveNotebook).not.toHaveBeenCalled()
    })

    it('each save sends the rev the previous one returned', async () => {
      const { save, cells, api } = setup()
      cells.value[0].source = 'A'
      await save.saveNow()
      cells.value[0].source = 'B'
      await save.saveNow()
      expect(api.SaveNotebook.mock.calls[0][0].rev).toBe('rev-0')
      expect(api.SaveNotebook.mock.calls[1][0].rev).toBe('rev-2')
    })

    it('never includes a cell result in the payload', async () => {
      const { save, cells, api } = setup()
      cells.value[0].result = { columns: ['x'], types: ['INTEGER'], rows: [[1]] }
      cells.value[0].source = 'edited'
      await save.saveNow()
      expect(JSON.stringify(api.SaveNotebook.mock.calls[0][0])).not.toContain('"rows"')
    })
  })

  describe('flush on close', () => {
    it('cancels the pending debounce and saves now', async () => {
      const { save, cells, api } = setup()
      cells.value[0].source = 'edited'
      save.scheduleSave()
      await save.doFlush()
      expect(api.SaveNotebook).toHaveBeenCalledTimes(1)
      vi.advanceTimersByTime(2000) // the debounce must not fire a second write
      await flushPromises()
      expect(api.SaveNotebook).toHaveBeenCalledTimes(1)
    })

    it('skips the write when nothing changed since load, so it cannot clobber a newer write elsewhere', async () => {
      const { save, api } = setup()
      await save.doFlush()
      expect(api.SaveNotebook).not.toHaveBeenCalled()
    })

    it('uses the payload captured before teardown, under the id that was loaded', async () => {
      const { save, cells, title, api } = setup()
      cells.value[0].source = 'edited'
      const captured = save.capture()!
      // the component is torn down, the route has moved on
      title.value = 'another'
      cells.value = []
      save.begin('nb2')
      await save.flushSave(captured)
      expect(api.SaveNotebook).toHaveBeenCalledTimes(1)
      expect(api.SaveNotebook.mock.calls[0][0]).toMatchObject({ id: 'nb1', title: 'Notebook', cells: [{ source: 'edited' }] })
    })

    it('rejects, with a toast, when the write fails, so a close can ask the person first', async () => {
      const { save, cells, error } = setup({ SaveNotebook: vi.fn(async () => { throw new Error('failed to save: disk full') }) })
      cells.value[0].source = 'edited'
      await expect(save.doFlush()).rejects.toThrow("Couldn't save the notebook: disk full")
      expect(error).toHaveBeenCalledWith("Couldn't save the notebook: disk full")
      await expect(save.flushSave()).resolves.toBeUndefined() // the internal variant never throws
    })

    it('rejects while a conflict is unresolved: the edit is not on disk', async () => {
      const { save, cells, api } = setup()
      cells.value[0].source = 'edited'
      save.raiseConflict(false)
      await expect(save.doFlush()).rejects.toThrow(/changed on disk and your edit is not saved yet/)
      expect(api.SaveNotebook).not.toHaveBeenCalled()
    })
  })

  describe('conflict on save', () => {
    const refused = () => new Error('conflict: notebook changed on disk')

    it('a refused autosave raises the banner and fetches the rev on disk', async () => {
      const { save, cells, error } = setup({ SaveNotebook: vi.fn(async () => { throw refused() }) })
      cells.value[0].source = 'edited'
      await save.saveNow()
      await flushPromises()
      expect(save.conflict.value).toEqual({ deleted: false })
      expect(save.saveState.value).toBe('idle')
      expect(error).not.toHaveBeenCalled() // a conflict is not a toast
    })

    it('a refused flush raises the banner and rejects', async () => {
      const { save, cells } = setup({ SaveNotebook: vi.fn(async () => { throw new Error('conflict: notebook was deleted on disk') }) })
      cells.value[0].source = 'edited'
      await expect(save.doFlush()).rejects.toThrow(/not saved yet/)
      expect(save.conflict.value).toEqual({ deleted: true })
    })

    it('Keep mine overwrites only the revision the banner was about, then resets', async () => {
      const { save, cells, api, status } = setup({ SaveNotebook: vi.fn(async () => { throw refused() }) })
      cells.value[0].source = 'mine'
      await save.saveNow()
      await flushPromises()
      await save.resolveConflict('overwrite')
      expect(api.ResolveConflict).toHaveBeenCalledWith(expect.objectContaining({ kind: 'notebook', id: 'nb1', action: 'overwrite', rev: 'disk-rev' }))
      expect(api.ResolveConflict.mock.calls[0][0].data).toMatchObject({ id: 'nb1', cells: [{ source: 'mine' }] })
      expect(save.conflict.value).toBeNull()
      expect(save.saveState.value).toBe('saved')
      expect(status).toHaveBeenCalledWith('Kept your version.')
      // and the kept version is now the baseline: a flush has nothing to write
      api.SaveNotebook.mockClear()
      await save.doFlush()
      expect(api.SaveNotebook).not.toHaveBeenCalled()
    })

    it('a second on-disk edit during Keep mine re-raises the banner, with the "again" text and nothing overwritten', async () => {
      let disk = 0
      const { save, cells, api, error } = setup({
        SaveNotebook: vi.fn(async () => { throw refused() }),
        GetNotebook: vi.fn(async () => ({ id: 'nb1', rev: `disk-${++disk}` })),
        ResolveConflict: vi.fn(async () => { throw refused() }),
      })
      cells.value[0].source = 'mine'
      await save.saveNow()
      await flushPromises()
      await save.resolveConflict('overwrite')
      await flushPromises()
      expect(error).toHaveBeenCalledWith(expect.stringMatching(/changed on disk again, so nothing was overwritten/))
      expect(save.conflict.value).toEqual({ deleted: false }) // shown afresh
      expect(save.conflictBusy.value).toBe(false)
      // the next Keep mine carries the newest revision
      api.ResolveConflict.mockImplementationOnce(async () => ({ rev: 'k', title: '' }))
      await flushPromises()
      await save.resolveConflict('overwrite')
      expect(api.ResolveConflict.mock.calls.at(-1)![0].rev).toBe('disk-2')
    })

    it('Keep mine after a delete writes it back (no rev)', async () => {
      const { save, cells, api } = setup({ SaveNotebook: vi.fn(async () => { throw new Error('conflict: notebook was deleted on disk') }) })
      cells.value[0].source = 'mine'
      await save.saveNow()
      await flushPromises()
      expect(save.conflict.value).toEqual({ deleted: true })
      await save.resolveConflict('overwrite')
      expect(api.ResolveConflict).toHaveBeenCalledWith(expect.objectContaining({ action: 'overwrite', rev: '' }))
      expect(save.conflict.value).toBeNull()
    })

    it('Reload drops the banner and the pending write and reloads from disk', async () => {
      const { save, cells, reload, api } = setup()
      cells.value[0].source = 'mine'
      save.scheduleSave()
      save.raiseConflict(false)
      await save.resolveConflict('reload')
      expect(save.conflict.value).toBeNull()
      expect(reload).toHaveBeenCalledTimes(1)
      vi.advanceTimersByTime(2000)
      expect(api.SaveNotebook).not.toHaveBeenCalled()
    })

    it('Save a copy writes the copy, then reloads the disk version', async () => {
      const { save, reload, status } = setup()
      save.raiseConflict(false)
      await save.resolveConflict('saveCopy')
      expect(reload).toHaveBeenCalledTimes(1)
      expect(status).toHaveBeenCalledWith('Saved your version as "My copy". This is the version on disk.')
    })
  })

  describe('tombstones', () => {
    it('keeps a deleted id until a save that excludes it succeeds', async () => {
      const { save, cells } = setup()
      save.tombstone('c1')
      expect(save.tombstoned().has('c1')).toBe(true)
      cells.value = []
      await save.saveNow()
      expect(save.tombstoned().has('c1')).toBe(false)
    })
  })
})
