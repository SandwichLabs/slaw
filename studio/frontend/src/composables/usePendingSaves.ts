// usePendingSaves — a shared registry of "I have a debounced save that
// might not have fired yet" flush functions (review fix, step 4a round 2
// #1). Reproduced bug: AppShell's closeFile() called CloseFile() directly,
// which closes (and Save()s) the container server side; NotebookView's own
// flush (on unmount, once AppShell's file-closed event propagates and
// unmounts it) then ran AFTER the container was already gone, so its
// SaveNotebook call failed with "no file open" and the error was silently
// swallowed — a pending edit was lost.
//
// Fix: any view holding a debounced save registers its flush function here
// on mount and unregisters on unmount. AppShell's close path (and the app
// quit flow driven by Wails' OnBeforeClose, see App.vue) awaits flushAll()
// BEFORE calling CloseFile/Quit, so the flush runs while the container is
// still open and its result — success or failure — is not lost.
//
// Round 3 review fix: a failed flush used to still let the file close (the
// registered flush caught its own error so flushAll always resolved
// cleanly) — the toast NotebookView set then unmounted along with the
// view, and the edit was gone with no way for the person to stop the
// close. Now the flush registered here is expected to REJECT on failure —
// see NotebookView's doFlush — and flushAll reports every failure by label
// so a caller (AppShell.closeFile, UnlockView.openFile) can ask the person
// whether to proceed anyway instead of silently losing the edit.
import { errorText } from '../lib/errors'

type FlushFn = () => Promise<void>
type LabelFn = () => string

interface Entry {
  label: LabelFn
  fn: FlushFn
}

const registry = new Set<Entry>()

/**
 * Register a flush function with a label getter (evaluated only if/when
 * that flush actually fails, so it can read live state — e.g. the
 * notebook's current title — rather than a name captured before the
 * notebook even finished loading). Returns an unregister function.
 */
export function register(label: LabelFn, fn: FlushFn): () => void {
  const entry: Entry = { label, fn }
  registry.add(entry)
  return () => {
    registry.delete(entry)
  }
}

interface FlushFailure {
  label: string
  error: string
}

interface FlushAllResult {
  failed: FlushFailure[]
}

// withTimeout rejects a flush that is still running after ms, so that entry
// (and only that entry) is reported as failed under its own label.
function withTimeout(p: Promise<void>, ms?: number): Promise<void> {
  if (!ms) return p
  let t: ReturnType<typeof setTimeout> | undefined
  const late = new Promise<never>((_, reject) => {
    t = setTimeout(() => reject(new Error('saving is taking too long')), ms)
  })
  return Promise.race([p, late]).finally(() => clearTimeout(t))
}

/**
 * Await every registered flush and report which ones failed. Unlike a
 * bare Promise.all, one failing flush never stops the others from being
 * attempted (Promise.allSettled) — but a failure is no longer swallowed:
 * it comes back in `failed` for the caller to act on (see AppShell.closeFile
 * / UnlockView.openFile, which ask the person before proceeding).
 */
export async function flushAll(opts: { timeoutMs?: number } = {}): Promise<FlushAllResult> {
  const entries = Array.from(registry)
  const results = await Promise.allSettled(entries.map((e) => withTimeout(e.fn(), opts.timeoutMs)))
  const failed: FlushFailure[] = []
  results.forEach((r, i) => {
    if (r.status === 'rejected') {
      const e = r.reason
      const message = errorText(e)
      failed.push({ label: entries[i].label(), error: message })
    }
  })
  return { failed }
}

// Exposed for tests only — resets the registry between test cases so one
// spec file's registrations can't leak into another's.
export function _resetForTests(): void {
  registry.clear()
}
