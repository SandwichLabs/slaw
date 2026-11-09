/**
 * useNewItem: the state behind the New dialog (docs/plans/library.md L6). Step
 * 1 picks a type (the opener may preselect one), step 2 names it; Back keeps
 * the choice. Create calls CreateNotebook or NewArtifact(name, kind) and hands
 * the new item's route to `onCreated`. A second Create while one is running is
 * ignored, so a double click never makes two items.
 */
import { ref, computed, watch, type Ref } from 'vue'
import { App } from '../lib/bindings'
import { errorText } from '../lib/errors'
import { artifactPath, notebookPath, type LibraryType } from '../lib/libraryRoutes'
import { nameProblem, starterKind, titleTooLong } from '../lib/newItem'
import { agentDraft, openAgent } from '../lib/agentDraft'
import { setPendingAsk } from './usePendingAsk'

export function useNewItem(open: Ref<boolean>, preselect: Ref<LibraryType | null | undefined>, onCreated: (to: string) => void, onDescribed: () => void = () => {}) {
  const step = ref<'type' | 'name' | 'describe'>('type')
  const type = ref<LibraryType>('notebook')
  const withScript = ref(false)
  const advanced = ref(false)
  const value = ref('')
  const description = ref('')
  const error = ref('')
  const busy = ref(false)

  watch(open, (isOpen) => {
    if (!isOpen) return
    step.value = 'type'
    type.value = preselect.value ?? 'notebook'
    advanced.value = type.value === 'script'
    withScript.value = false
    value.value = ''
    description.value = ''
    error.value = ''
    busy.value = false
  }, { immediate: true })

  const isNotebook = computed(() => type.value === 'notebook')
  const warnLong = computed(() => isNotebook.value && titleTooLong(value.value))
  const slug = computed(() => value.value.trim() || '<name>')

  function next() { step.value = 'name'; error.value = '' }
  function back() { step.value = 'type'; error.value = '' }
  function describe() { step.value = 'describe'; error.value = '' }

  // Describe it (L7). A notebook is created and opened with its AI cell dialog
  // filled in (usePendingAsk); the other kinds open the agent panel with an
  // unsent request. Nothing is sent from here.
  async function submitDescribe() {
    if (busy.value) return
    const text = description.value.trim()
    if (!text) { error.value = 'Describe what you want first.'; return }
    if (!isNotebook.value) {
      openAgent(agentDraft(type.value, text, withScript.value))
      onDescribed()
      return
    }
    const problem = nameProblem('notebook', value.value)
    if (problem) { error.value = problem; return }
    busy.value = true
    error.value = ''
    try {
      const nb = await App.CreateNotebook(value.value)
      setPendingAsk(nb.id, text)
      onCreated(notebookPath(nb.id))
    } catch (e) {
      error.value = "Couldn't create the notebook: " + errorText(e)
    } finally {
      busy.value = false
    }
  }

  async function submit() {
    if (busy.value) return
    const problem = nameProblem(type.value, value.value)
    if (problem) { error.value = problem; return }
    busy.value = true
    error.value = ''
    try {
      const kind = starterKind(type.value, withScript.value)
      if (kind === null) {
        const nb = await App.CreateNotebook(value.value)
        onCreated(notebookPath(nb.id))
      } else {
        const n = value.value.trim()
        await App.NewArtifact(n, kind)
        onCreated(artifactPath(n))
      }
    } catch (e) {
      error.value = isNotebook.value ? "Couldn't create the notebook: " + errorText(e) : errorText(e)
    } finally {
      busy.value = false
    }
  }

  return { step, type, withScript, advanced, value, description, error, busy, isNotebook, warnLong, slug, next, back, describe, submit, submitDescribe }
}
