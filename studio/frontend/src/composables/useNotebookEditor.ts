/**
 * useNotebookEditor — the notebook editor (#66 part A) wired together (F10): it
 * loads a notebook, owns the toasts, and connects the cell list
 * (useNotebookCells), the autosave / flush / conflict path (useNotebookSave) and
 * the agent-append / on-disk sync (useNotebookSync) to the route, the Wails
 * events and the pending-save registry. NotebookView only renders it.
 *
 * Outputs are never persisted: a cell's `result` lives only in local state (never
 * sent to SaveNotebook, which only ever carries source/view/page — see
 * notebooks.Coerce). Every sql cell re-runs on open (runAll in loadNotebook).
 */
import { ref, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import { useRoute, useRouter, onBeforeRouteLeave } from 'vue-router'
import { App, hasBinding } from '../lib/bindings'
import { backToLibrary, libraryPath } from '../lib/libraryRoutes'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { register as registerPendingSave } from './usePendingSaves'
import { takePendingAsk } from './usePendingAsk'
import { useNotebookCells } from './useNotebookCells'
import { useNotebookSave } from './useNotebookSave'
import { useNotebookSync } from './useNotebookSync'
import { errorText } from '../lib/errors'
import { hasWailsRuntime } from '../lib/wails'
import { newCellState, type RawCell } from '../lib/notebookCells'

export function useNotebookEditor() {
  const route = useRoute()
  const router = useRouter()
  const loadedNotebookIdForView = computed(() => String(route.params.id))

  const title = ref('')
  const loading = ref(true)
  const notFound = ref(false)
  const errorMessage = ref('')
  // infoMessage is the neutral/marigold toast — for informational notices that
  // are NOT errors (e.g. the AI-cell apply note), so they never appear in the red
  // --sh-error toast, which the SchoolHouse palette reserves for failed states.
  const infoMessage = ref('')
  // statusMessage is a success confirmation (#91: notebook export) — same
  // click-to-dismiss shape as infoMessage, plus a 4s auto-dismiss timeout.
  const statusMessage = ref('')

  const nb = useNotebookCells({ onDelete: (id) => save.tombstone(id), notebookId: () => save.id() })
  const { cells } = nb
  const save = useNotebookSave({
    title, cells,
    error: (m) => { errorMessage.value = m },
    status: (m) => { statusMessage.value = m },
    reload: reloadFromDisk,
  })
  const sync = useNotebookSync({
    loading, title, save, nb,
    reload: reloadFromDisk,
    leave: () => { router.push(libraryPath('notebook')) },
    status: (m) => { statusMessage.value = m },
    info: (m) => { infoMessage.value = m },
  })

  async function loadNotebook(id: string) {
    nb.beginLoad()
    loading.value = true
    notFound.value = false
    const ask = takePendingAsk(id)
    save.begin(id)
    // A fresh load already reflects current server state once it resolves —
    // anything queued for whatever was loading/loaded before this is moot.
    sync.clearQueue()
    try {
      const [loaded, sessionId] = await Promise.all([
        App.GetNotebook(id),
        // The file-open session in effect now: every save sends it back and the
        // backend refuses one whose token no longer matches the open file.
        hasBinding('GetSessionID') ? App.GetSessionID().catch(() => '') : Promise.resolve(''),
      ])
      if (save.id() !== id) return // superseded by another load before this one resolved
      title.value = loaded.title
      cells.value = (loaded.cells || []).map((c: RawCell) => newCellState(c))
      save.adopt(sessionId ?? '', (loaded as { rev?: string }).rev ?? '')
      // loading must flip BEFORE the nextTick below — the cell list template is
      // behind `v-if="!loading"`, so while loading is still true the cell DOM (and
      // therefore every editorEl ref) does not exist yet, and mountEditors() would
      // silently no-op for every cell.
      loading.value = false
      await nextTick()
      nb.mountEditors()
      nb.setupCellObserver()
      if (hasWailsRuntime()) App.SetActiveNotebook(id)
      // A notebook made from "describe it" opens its AI cell dialog with the description (taken at the start, used once the load succeeded).
      if (ask !== null) nb.aiCellDialog.openCreate(ask)
      await nb.runAll()
      await sync.replayPending(id)
    } catch (e: any) {
      notFound.value = true
      errorMessage.value = "Couldn't load the notebook: " + errorText(e)
      loading.value = false
      // Nothing was loaded, so there is nothing to save: a later flush (switching
      // workspaces, closing) must not try to write an empty notebook back.
      save.abandon(id)
    }
    await nextTick()
    save.end(id)
  }

  // reloadFromDisk replaces the open copy with what is on disk now, so its SQL
  // cells re-run, and drops any local edit. loadNotebook replaces cells wholesale
  // but does not tear down the CURRENT cells' CodeMirror editors first, so do that
  // here. A running or proposed AI-cell task belongs to the old content: closed too.
  async function reloadFromDisk() {
    save.cancelTimer()
    nb.destroyEditors()
    nb.aiCellDialog.close()
    await loadNotebook(save.id())
  }

  // onAiCellApplied schedules a save and, when apply had to insert beside instead
  // of replacing (a drifted edit or a type mismatch), surfaces that as a toast.
  function onAiCellApplied(note: string) {
    save.scheduleSave()
    if (note) infoMessage.value = note
  }

  watch(statusMessage, (msg) => {
    if (msg) setTimeout(() => { statusMessage.value = '' }, 4000)
  })
  watch(title, save.scheduleSave)
  watch(() => cells.value.map((c) => `${c.id}:${c.type}:${c.source}:${c.view}:${c.viewPinned}:${c.label}:${c.collapsed}:${c.connection}:${JSON.stringify(c.motion)}`).join('|'), save.scheduleSave)
  watch(() => cells.value.length, save.scheduleSave)

  // vue-router reuses this component instance across a /library/notebook/:id ->
  // /library/notebook/:id2 navigation, so switching notebooks must flush THIS notebook's
  // edits (captured under its own id, before anything changes), tear down its
  // editors/observer/in-flight queries, and only THEN load the new one.
  watch(
    () => route.params.id,
    async (newId) => {
      const id = String(newId)
      if (!id || id === save.id()) return
      const payload = save.capture()
      nb.teardown()
      // A running/proposed AI-cell task belongs to the notebook it was opened
      // against — its eventual Apply must never land in the one we're opening.
      nb.aiCellDialog.close()
      await save.flushSave(payload)
      await loadNotebook(id)
    },
  )

  function backToList() {
    backToLibrary(router, libraryPath('notebook'))
  }

  async function exportNotebook() {
    if (!save.id()) return
    try {
      const path = await App.ExportNotebook(save.id(), '')
      if (path) statusMessage.value = `Exported to ${path}`
    } catch (e: any) {
      errorMessage.value = "Couldn't export the notebook: " + errorText(e)
    }
  }

  function handleWorkspaceClosed() {
    // The shell already awaited flushAll() BEFORE the workspace closed, so the
    // happy-path flush succeeded while it was still open. Kept as a defensive
    // fallback; the loaded id is cleared so nothing here keeps trying to save into
    // a workspace that may already be gone.
    save.flushSave()
    save.forget()
  }

  onBeforeRouteLeave(async () => {
    await save.flushSave()
  })

  // Unsubscribe functions EventsOn returns: calling the global EventsOff would
  // remove EVERY listener for that event name, including useWorkspace's own.
  const unsubs: Array<() => void> = []

  onMounted(() => {
    loadNotebook(String(route.params.id))
    if (hasWailsRuntime()) {
      unsubs.push(EventsOn('workspace:closed', handleWorkspaceClosed))
      unsubs.push(EventsOn('workspace:changed-on-disk', sync.handleChangedOnDisk))
      unsubs.push(EventsOn('notebooks:changed', sync.handleNotebooksChanged))
    }
    // Register this view's flush with the shared registry so AppShell's close path
    // (and the app-quit flow) can await it before the container actually closes —
    // doFlush directly (not flushSave) so a real failure propagates. The label is
    // read lazily, so it shows the live title.
    unsubs.push(registerPendingSave(() => title.value || 'Untitled notebook', () => save.doFlush()))
  })

  onBeforeUnmount(() => {
    // Capture BEFORE any teardown touches editor/observer/abort state.
    const payload = save.capture()
    save.cancelTimer()
    nb.teardown()
    for (const off of unsubs) off()
    nb.aiCellDialog.close()
    if (hasWailsRuntime()) App.SetActiveNotebook('')
    save.flushSave(payload)
  })

  return {
    nb, save, sync, cells, title, loading, notFound, errorMessage, infoMessage, statusMessage,
    loadedNotebookIdForView, backToList, exportNotebook, onAiCellApplied, handleWorkspaceClosed, reloadFromDisk,
  }
}
