/**
 * useNotebookActions: what a Library row does to a notebook that is not
 * opening it: delete (after a confirm) and import from a file. Errors set `message`
 * and clear themselves.
 */
import { ref, watch } from 'vue'
import { App } from '../lib/bindings'
import { errorText } from '../lib/errors'

export function useNotebookActions(opts: { reload: () => Promise<void>; open: (id: string) => void }) {
  const message = ref('')
  const importing = ref(false)
  const pendingDeleteId = ref<string | null>(null)

  watch(message, (msg) => {
    if (msg) setTimeout(() => { if (message.value === msg) message.value = '' }, 4000)
  })

  async function confirmDelete() {
    const id = pendingDeleteId.value
    pendingDeleteId.value = null
    if (!id) return
    try {
      await App.DeleteNotebook(id)
      await opts.reload()
    } catch (e) {
      message.value = "Couldn't delete the notebook: " + errorText(e)
    }
  }

  async function importNotebook() {
    importing.value = true
    try {
      const path = await App.SelectNotebookFile()
      if (!path) return
      const result = await App.ImportNotebook(path)
      await opts.reload()
      if (result?.warnings?.length) message.value = `Imported with ${result.warnings.length} ${result.warnings.length === 1 ? 'warning' : 'warnings'}: ${result.warnings[0]}`
      if (result?.notebook?.id) opts.open(result.notebook.id)
    } catch (e) {
      message.value = 'Failed to import notebook: ' + errorText(e)
    } finally {
      importing.value = false
    }
  }

  return { message, importing, pendingDeleteId, confirmDelete, importNotebook }
}
