/**
 * useNotebookSave — the notebook editor's save path (F10): the 800ms debounced
 * autosave, the flush that runs on route leave / unmount / file close, the
 * compare-and-write revision, and the conflict banner (useConflict). The view
 * owns loading; this owns what is known about the file on disk:
 *
 *  - id / sessionId: captured ONCE at load time. vue-router reuses the component
 *    across /library/notebook/:id -> /library/notebook/:id2, so a debounced save must never
 *    read the live route (it would overwrite the wrong notebook), and the
 *    session id lets the backend refuse a save aimed at a file that has since
 *    been closed or swapped.
 *  - rev: the store's revision this copy last loaded or saved; every save sends
 *    it, so a copy that a git pull, an editor or an agent changed meanwhile is a
 *    conflict instead of an overwrite.
 *  - lastPersisted: JSON of the last payload known to match the server; a flush
 *    with nothing changed since skips its save so it can never overwrite a newer
 *    write from elsewhere with a stale local copy (#95 carry-over).
 *  - saveSeq: an in-flight save superseded by a later one must not stomp
 *    lastPersisted back to its own stale payload.
 *  - tombstones: ids deleted locally but not yet confirmed excluded by a save,
 *    so a "notebooks:changed" echo can never resurrect them.
 */
import { ref, type Ref } from 'vue'
import { App } from '../lib/bindings'
import { errorText, isConflict, isDeletedConflict } from '../lib/errors'
import { pruneConfirmedTombstones } from '../lib/notebookMerge'
import { buildSavePayload, editKey, type CellState, type SavePayload } from '../lib/notebookCells'
import { useConflict } from './useConflict'

const CONFLICT_UNSAVED = 'This notebook changed on disk and your edit is not saved yet'

interface NotebookSaveOptions {
  title: Ref<string>
  cells: Ref<CellState[]>
  /** Surfaces an error toast. */
  error: (msg: string) => void
  /** Surfaces a success toast. */
  status: (msg: string) => void
  /** Replaces the open copy with what is on disk now (drops local edits). */
  reload: () => Promise<void>
}

export function useNotebookSave(opts: NotebookSaveOptions) {
  const saveState = ref<'idle' | 'saving' | 'saved'>('idle')
  let loadedId = ''
  let loadedSessionId = ''
  let loadedRev = ''
  let lastPersisted = ''
  let saveSeq = 0
  let saveTimer: ReturnType<typeof setTimeout> | null = null
  // true while loading, so the initial populate doesn't trigger a save
  let suppressAutosave = true
  let tombstones = new Set<string>()

  const cf = useConflict({
    currentId: () => loadedId,
    fetchRev: async (id) => ((await App.GetNotebook(id)) as { rev?: string } | null)?.rev,
  })

  // --- what is loaded ---

  const id = () => loadedId
  const sessionId = () => loadedSessionId

  // begin: a load of notebook `nid` has started.
  function begin(nid: string) {
    cf.conflict.value = null
    suppressAutosave = true
    loadedId = nid
  }
  // adopt: the notebook (and the cells/title refs) now match the server.
  function adopt(session: string, rev: string) {
    loadedSessionId = session
    loadedRev = rev
    lastPersisted = JSON.stringify(buildSavePayload(loadedId, opts.title.value, opts.cells.value))
  }
  // abandon: nothing was loaded for nid, so a later flush must not write an empty notebook.
  function abandon(nid: string) {
    if (loadedId === nid) loadedId = ''
  }
  // end: the load settled; autosave may run if nid is still the loaded one.
  function end(nid: string) {
    if (loadedId === nid) suppressAutosave = false
  }
  // forget: the workspace closed; nothing here may keep trying to save into it.
  function forget() {
    loadedId = ''
    loadedSessionId = ''
    lastPersisted = ''
  }

  function tombstone(cellId: string) { tombstones.add(cellId) }
  const tombstoned = () => tombstones

  // adoptRemote: after merging appended cells from `remote`. Adopt its revision
  // only when the remote notebook is exactly what this view last saved or loaded
  // plus the appended cells; otherwise keep the old revision, so the next save
  // is a conflict instead of silently writing over a change made elsewhere.
  function adoptRemote(remoteBase: SavePayload, rev: string | undefined) {
    if (JSON.stringify(remoteBase) === lastPersisted) loadedRev = rev ?? loadedRev
    else cf.raise(false)
  }

  // hasEdit: is there a local edit that is not on disk? An auto-chosen view is not one.
  function hasEdit(): boolean {
    const current = capture()
    return !!current && !!lastPersisted && editKey(current) !== editKey(JSON.parse(lastPersisted))
  }

  // --- save ---

  // capture snapshots the CURRENT title/cells under the id this component
  // actually loaded (never the live route param). Null if nothing has loaded yet.
  function capture(): SavePayload | null {
    if (!loadedId) return null
    return buildSavePayload(loadedId, opts.title.value, opts.cells.value)
  }

  function cancelTimer() {
    if (saveTimer) {
      clearTimeout(saveTimer)
      saveTimer = null
    }
  }

  async function saveNow() {
    const payload = capture()
    if (!payload) return
    const seq = ++saveSeq
    saveState.value = 'saving'
    try {
      const result = await App.SaveNotebook({ ...payload, rev: loadedRev }, loadedSessionId)
      loadedRev = result?.notebook?.rev ?? loadedRev
      if (result?.warnings?.length) {
        opts.error(`Saved with ${result.warnings.length} ${result.warnings.length === 1 ? 'warning' : 'warnings'}: ${result.warnings[0]}`)
      }
      saveState.value = 'saved'
      tombstones = pruneConfirmedTombstones(tombstones, new Set(payload.cells.map((c) => c.id)))
      if (seq === saveSeq) lastPersisted = JSON.stringify(payload)
    } catch (e: any) {
      saveState.value = 'idle'
      if (isConflict(e)) {
        cf.raise(isDeletedConflict(e))
        return
      }
      opts.error("Couldn't save the notebook: " + errorText(e))
    }
  }

  // doFlush cancels any pending debounce and saves NOW, using a payload captured
  // before whatever prompted the flush (a route change, an unmount, a file close)
  // can tear anything down. It sets the toast AND rethrows on failure: the copy
  // registered with usePendingSaves is awaited by AppShell.closeFile/openFile via
  // flushAll(), which must learn that a save failed so it can ask the person
  // before closing/switching files and losing the edit. Internal call sites that
  // have nowhere to propagate a failure use flushSave, which never throws.
  async function doFlush(payload?: SavePayload | null) {
    cancelTimer()
    // An unresolved conflict means the edits here were NOT written: say so, so a
    // workspace switch or close can ask before they are lost.
    if (cf.conflict.value) throw new Error(CONFLICT_UNSAVED)
    const p = payload ?? capture()
    if (!p) return
    // #95 carry-over: nothing has changed since the last load/save, so there's
    // nothing local to write.
    const serialized = JSON.stringify(p)
    if (serialized === lastPersisted) return
    const seq = ++saveSeq
    try {
      const saved = await App.SaveNotebook({ ...p, rev: loadedRev }, loadedSessionId)
      loadedRev = saved?.notebook?.rev ?? loadedRev
      tombstones = pruneConfirmedTombstones(tombstones, new Set(p.cells.map((c) => c.id)))
      if (seq === saveSeq) lastPersisted = serialized
    } catch (e: any) {
      if (isConflict(e)) {
        cf.raise(isDeletedConflict(e))
        throw new Error(CONFLICT_UNSAVED)
      }
      const message = "Couldn't save the notebook: " + errorText(e)
      opts.error(message)
      throw new Error(message)
    }
  }

  async function flushSave(payload?: SavePayload | null) {
    try {
      await doFlush(payload)
    } catch {
      // doFlush already surfaced a toast; nothing further to do here.
    }
  }

  function scheduleSave() {
    if (suppressAutosave || cf.conflict.value) return
    saveState.value = 'idle'
    if (saveTimer) clearTimeout(saveTimer)
    saveTimer = setTimeout(saveNow, 800)
  }

  // --- conflict ---

  async function resolveConflict(action: 'reload' | 'overwrite' | 'saveCopy') {
    const nid = loadedId
    if (!nid) return
    if (action === 'reload') {
      cf.clear()
      cancelTimer()
      await opts.reload()
      return
    }
    await cf.resolving(async () => {
      cancelTimer()
      const payload = capture()
      const res = await App.ResolveConflict({ kind: 'notebook', id: nid, action, data: (payload ?? {}) as Record<string, any>, rev: cf.rev() })
      cf.clear()
      if (action === 'overwrite') {
        loadedRev = res.rev
        if (payload) lastPersisted = JSON.stringify(payload)
        saveState.value = 'saved'
        opts.status('Kept your version.')
      } else {
        await opts.reload()
        opts.status(`Saved your version as "${res.title}". This is the version on disk.`)
      }
    }, opts.error)
  }

  return {
    saveState, conflict: cf.conflict, conflictBusy: cf.busy, raiseConflict: cf.raise,
    id, sessionId, begin, adopt, abandon, end, forget, tombstone, tombstoned, adoptRemote, hasEdit,
    capture, cancelTimer, saveNow, doFlush, flushSave, scheduleSave, resolveConflict,
  }
}
