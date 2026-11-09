/**
 * useSkillsEditor — the Skills view's state and actions (F10): the skill list and
 * selection, the create/edit form, save, delete, the changed-on-disk conflict
 * flow (useConflict) and the pending-save flush for workspace switches.
 * SkillsView only renders it. Built-ins are embedded in the binary, not files in
 * skills/, so the view never offers edit/delete for them.
 */
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { App } from '../lib/bindings'
import { errorText, isConflict } from '../lib/errors'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { register as registerPendingSave } from './usePendingSaves'
import { useConflict } from './useConflict'

const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,63}$/

export function useSkillsEditor() {
  const skills = ref<SkillSummary[]>([])
  const loading = ref(true)
  const selectedName = ref<string | null>(null)
  const errorMessage = ref('')
  const viewMode = ref<'rendered' | 'raw'>('rendered')

  const showEditor = ref(false)
  const editingOriginalName = ref<string | null>(null) // null = creating new
  const editingRev = ref('') // the revision the skill had when the editor opened
  const formName = ref('')
  const formDescription = ref('')
  const formInstructions = ref('')
  const formTableName = ref('')
  const formNameError = ref('')
  const saving = ref(false)
  const showConfirmDelete = ref(false)
  const pendingDeleteName = ref<string | null>(null)

  watch(errorMessage, (msg) => {
    if (msg) setTimeout(() => { if (errorMessage.value === msg) errorMessage.value = '' }, 4000)
  })

  // The skill's file changed on disk while the editor had it open (a git pull,
  // another editor, the agent): ConflictBanner offers reload / keep mine / copy.
  const cf = useConflict({
    currentId: () => editingOriginalName.value ?? formName.value,
    fetchRev: async (id) => ((await App.ListSkills()) as SkillSummary[]).find((x) => x.name === id)?.rev,
  })
  const { conflict } = cf

  // What the editor was opened with, to tell "I edited" from "untouched".
  let openedWith = ''
  const formSnapshot = () => JSON.stringify([formDescription.value, formInstructions.value, formTableName.value])

  const selected = computed(() => skills.value.find((s) => s.name === selectedName.value) || null)

  async function loadSkills() {
    loading.value = true
    try {
      skills.value = (await App.ListSkills()) || []
      if (skills.value.length > 0 && !selectedName.value) {
        selectedName.value = skills.value[0].name
      }
    } catch (e: any) {
      errorMessage.value = "Couldn't load skills: " + errorText(e)
    }
    loading.value = false
  }

  function selectSkill(name: string) {
    selectedName.value = name
    viewMode.value = 'rendered'
  }

  function openForm(s: SkillSummary | null) {
    editingOriginalName.value = s?.name ?? null
    editingRev.value = s?.rev || ''
    formName.value = s?.name ?? ''
    formDescription.value = s?.description ?? ''
    formInstructions.value = s?.instructions ?? ''
    formTableName.value = s?.table_name || ''
    formNameError.value = ''
    cf.conflict.value = null
    showEditor.value = true
    openedWith = formSnapshot()
  }
  const openCreate = () => openForm(null)
  const openEdit = (s: SkillSummary) => openForm(s)

  // refill takes the disk version into the open form.
  function refill(disk: SkillSummary) {
    editingRev.value = disk.rev || ''
    formDescription.value = disk.description
    formInstructions.value = disk.instructions
    formTableName.value = disk.table_name || ''
    openedWith = formSnapshot()
  }

  const save = () => App.SaveSkill(formName.value, formDescription.value, formInstructions.value, formTableName.value, editingRev.value)

  async function submitEditor() {
    if (!NAME_PATTERN.test(formName.value)) {
      formNameError.value = 'Lowercase letters, numbers and hyphens, like revenue-rules'
      return
    }
    if (!formDescription.value.trim() || !formInstructions.value.trim()) {
      formNameError.value = 'Add a description and instructions'
      return
    }
    saving.value = true
    try {
      await save()
      showEditor.value = false
      await loadSkills()
      selectedName.value = formName.value
      viewMode.value = 'rendered'
    } catch (e: any) {
      if (isConflict(e)) {
        cf.raise(false)
        formNameError.value = ''
      } else {
        formNameError.value = errorText(e)
      }
    }
    saving.value = false
  }

  // flushPending is registered with usePendingSaves so switching workspaces,
  // closing and quitting save an open, edited skill first. A conflict or an
  // unfinished skill makes it fail so the caller can ask.
  async function flushPending() {
    if (!showEditor.value || formSnapshot() === openedWith) return
    if (conflict.value) throw new Error('this skill changed on disk and your edit is not saved yet')
    if (!NAME_PATTERN.test(formName.value) || !formDescription.value.trim() || !formInstructions.value.trim()) {
      throw new Error('the skill being edited is not finished (it needs a name, description and instructions)')
    }
    try {
      await save()
    } catch (e: any) {
      if (isConflict(e)) cf.raise(false)
      throw new Error(errorText(e))
    }
    await loadSkills()
    editingRev.value = skills.value.find((x) => x.name === formName.value)?.rev || ''
    openedWith = formSnapshot()
  }

  // resolveConflict applies the person's choice (see ConflictBanner). "reload"
  // refills the form from the disk version; "overwrite" writes the form over it;
  // "saveCopy" writes the form as <name>-copy and shows the disk version here.
  async function resolveConflict(action: 'reload' | 'overwrite' | 'saveCopy') {
    const id = editingOriginalName.value ?? formName.value
    if (action === 'reload' && conflict.value?.deleted) {
      // The file is gone: "Close it" just closes the editor.
      cf.clear()
      showEditor.value = false
      await loadSkills()
      return
    }
    await cf.resolving(async () => {
      const res = await App.ResolveConflict({
        kind: 'skill', id, action, rev: cf.rev(),
        data: { description: formDescription.value, instructions: formInstructions.value, tableName: formTableName.value },
      })
      cf.clear()
      if (action === 'overwrite') {
        editingRev.value = res.rev
        showEditor.value = false
        await loadSkills()
        selectedName.value = id
        return
      }
      await loadSkills()
      const disk = skills.value.find((x) => x.name === id)
      if (disk) refill(disk)
      else showEditor.value = false
      if (action === 'saveCopy') errorMessage.value = `Saved your version as "${res.title}".`
    }, (t) => { formNameError.value = t }, '')
  }

  // onDiskChange: another program changed skills/. The open editor refills quietly
  // when the person hasn't touched it, and shows the banner when they have.
  function onDiskChange(p: { kind: string; id: string; op: string }) {
    if (p?.kind !== 'skill' || !showEditor.value || p.id !== editingOriginalName.value) return
    if (formSnapshot() === openedWith && p.op !== 'deleted') {
      void loadSkills().then(() => {
        const disk = skills.value.find((x) => x.name === p.id)
        if (disk) refill(disk)
      })
      return
    }
    cf.raise(p.op === 'deleted')
  }

  function deleteSkill(s: SkillSummary) {
    pendingDeleteName.value = s.name
    showConfirmDelete.value = true
  }

  async function confirmDelete() {
    const name = pendingDeleteName.value
    showConfirmDelete.value = false
    pendingDeleteName.value = null
    if (!name) return
    try {
      await App.DeleteSkill(name)
      if (selectedName.value === name) selectedName.value = null
      await loadSkills()
    } catch (e: any) {
      errorMessage.value = "Couldn't delete the skill: " + errorText(e)
    }
  }

  // Import/app-import writes a workspace-<table> skill; other views (Import, the
  // agent) mutating data should refresh this list without a manual reload.
  const unsubs: Array<() => void> = []
  onMounted(() => {
    unsubs.push(registerPendingSave(() => 'The skill editor', flushPending))
    loadSkills()
    unsubs.push(EventsOn('data:changed', loadSkills))
    unsubs.push(EventsOn('workspace:changed-on-disk', onDiskChange))
  })
  onUnmounted(() => { for (const off of unsubs) off() })

  return {
    skills, loading, selectedName, errorMessage, viewMode, showEditor, editingOriginalName, formName,
    formDescription, formInstructions, formTableName, formNameError, saving, showConfirmDelete,
    pendingDeleteName, conflict, conflictBusy: cf.busy, selected,
    selectSkill, openCreate, openEdit, submitEditor, resolveConflict, deleteSkill, confirmDelete,
  }
}
