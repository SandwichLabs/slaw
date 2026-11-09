/**
 * useImport — the Import wizard's state and actions (F10): pick a file, inspect
 * it (full-file type inference, suggested key), a debounced dry run on every
 * option change so the summary always describes what Import is about to do, and
 * the Import itself. ImportView only renders it.
 */
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { App } from '../lib/bindings'
import { errorText } from '../lib/errors'
import { KEY_AUTO, deriveTableName, nextFreeName } from '../lib/importTypes'

const MODE_LABELS: Record<'upsert' | 'replace' | 'append', string> = {
  upsert: 'Update matching rows',
  replace: 'Replace table',
  append: 'Add rows',
}
const MODE_DISABLED_REASON: Record<'upsert' | 'replace' | 'append', string> = {
  upsert: 'Needs a primary key in both the table and the file.',
  // Worded direction-neutral: the table can be blocked by a foreign key it
  // holds itself (outgoing) or one another table holds against it
  // (incoming) — "this table's foreign keys" would be wrong for the
  // incoming case.
  replace: 'A foreign key links this table to another — replacing would break it. Use Update matching rows instead.',
  append: 'Not available for this table.',
}
const MODE_ORDER: ('upsert' | 'replace' | 'append')[] = ['upsert', 'replace', 'append']

export function useImport() {
  const router = useRouter()

  const path = ref('')
  const sheet = ref('')
  const hasHeader = ref(true)
  const tableName = ref('')
  const mode = ref<ImportMode | ''>('')
  const key = ref<string>(KEY_AUTO)
  const typeOverrides = ref<Record<string, ImportColType>>({})

  const tables = ref<string[]>([])
  const inspection = ref<ImportInspection | null>(null)
  const summary = ref<ImportSummary | null>(null)
  const successSummary = ref<ImportSummary | null>(null)

  const inspecting = ref(false)
  const dryRunning = ref(false)
  const importing = ref(false)
  const error = ref('')
  // Distinguishes a failure while previewing (file pick, sheet/header change,
  // or a dry run) from an actual failed Import click, so the error banner's
  // title doesn't claim "Import failed" before the user has clicked Import.
  const errorContext = ref<'preview' | 'import'>('preview')

  // #91 R-F2 guard: disable Import once these exact options have already been
  // imported, until something changes.
  const importedKey = ref('')

  // Lower-cased compare: DuckDB's own quoted-identifier catalog lookup is
  // case-insensitive (a table stored as "Customers" IS the table named by
  // "customers"), so a case-sensitive `includes` here disagreed with the
  // backend — the wizard showed no mode group, sent mode "" (create), and
  // got stuck on a Blocked banner ("pass mode upsert|replace|append") for a
  // table that, from the backend's point of view, plainly already existed.
  const tableExists = computed(
    () => !!tableName.value && tables.value.some((t) => t.toLowerCase() === tableName.value.toLowerCase()),
  )
  const keyCandidates = computed(() => inspection.value?.columns.filter((c) => c.candidate) ?? [])
  // The key picker only makes sense when a schema is being decided: a brand
  // new table, or a replace (which recreates the schema from scratch).
  const showKeySelect = computed(() => !tableExists.value || mode.value === 'replace')

  const mismatchText = computed(() => {
    const parts: string[] = []
    if (summary.value?.missing?.length) parts.push(`The file is missing: ${summary.value.missing.join(', ')}.`)
    if (summary.value?.extra?.length) parts.push(`The file has extra: ${summary.value.extra.join(', ')}.`)
    return parts.join(' ')
  })

  // An append with no primary key still succeeds (ok:true) even when some
  // file rows already match a table row exactly — it's worth calling out
  // (attn styling, same banner as a blocker), not a silent plain-meta line.
  const hasExactDupeWarning = computed(() =>
    !!summary.value && summary.value.mode === 'append' && summary.value.exact_dupes > 0,
  )

  // True while the table already exists and no mode has been chosen yet —
  // the mode group is showing (it needs summary.existing_rows/modes, which
  // the dry run still fetches with mode:"" — see requestKey's watcher), but
  // there's nothing to preview until the user picks one. A mode:"" dry run
  // gets back the backend's own Blocked sentence ('table "x" already exists
  // (N rows); pass mode replace|append') — CLI wording never meant for this
  // banner — alongside summarySentence's unrelated, contradictory "Will
  // import N rows" (summarySentence has no case for mode "", so it fell
  // through to its create-mode phrasing). Both are suppressed for this case
  // (hasProblems below, and the template's mode-prompt taking priority over
  // summary-line/problem-banner) in favor of one neutral prompt.
  const modeNotChosen = computed(() => !!path.value && tableExists.value && !mode.value)

  const hasProblems = computed(() => {
    const s = summary.value
    if (!s || modeNotChosen.value) return false
    return !!s.blocked || !!(s.bad_values && s.bad_values.length) || !!mismatchText.value || hasExactDupeWarning.value
  })

  const canImport = computed(() =>
    !!path.value &&
    !!tableName.value &&
    !inspecting.value &&
    !dryRunning.value &&
    !importing.value &&
    !!summary.value?.ok &&
    requestKey.value !== importedKey.value,
  )

  function isKeyColumn(name: string): boolean {
    return showKeySelect.value ? key.value === name : summary.value?.table_key === name
  }

  function typeSelectDisabled(): boolean {
    return tableExists.value && (mode.value === 'append' || mode.value === 'upsert')
  }

  function setColumnType(name: string, t: ImportFriendlyType) {
    const current = typeOverrides.value[name]
    typeOverrides.value = {
      ...typeOverrides.value,
      [name]: { type: t, scale: t === 'decimal' ? (current?.type === 'decimal' ? current.scale : 2) : undefined },
    }
  }

  function buildOptions(dryRun: boolean): ImportOptions {
    return {
      path: path.value,
      sheet: sheet.value,
      has_header: hasHeader.value,
      table: tableName.value,
      mode: mode.value,
      key: key.value,
      types: typeOverrides.value,
      dry_run: dryRun,
    }
  }

  async function loadTables() {
    try {
      tables.value = (await App.ListTables()) || []
    } catch { /* leave the existing list; the dry run will still be accurate */ }
  }

  function resetAll() {
    path.value = ''
    sheet.value = ''
    hasHeader.value = true
    tableName.value = ''
    mode.value = ''
    key.value = KEY_AUTO
    typeOverrides.value = {}
    inspection.value = null
    summary.value = null
    successSummary.value = null
    importedKey.value = ''
    error.value = ''
  }

  async function selectFile() {
    error.value = ''
    errorContext.value = 'preview'
    try {
      const p = await App.SelectImportFile()
      if (!p) return
      resetAll()
      path.value = p
      tableName.value = deriveTableName(p)
      await loadTables()
      await inspect()
    } catch (e: any) {
      error.value = errorText(e)
    }
  }

  async function inspect() {
    if (!path.value) return
    inspecting.value = true
    error.value = ''
    errorContext.value = 'preview'
    try {
      const insp = await App.InspectImport({
        path: path.value,
        sheet: sheet.value,
        has_header: hasHeader.value,
      })
      inspection.value = insp
      if (insp.sheet && !sheet.value) sheet.value = insp.sheet
      key.value = insp.suggested_key || KEY_AUTO
      typeOverrides.value = Object.fromEntries(insp.columns.map((c) => [c.name, { ...c.inferred }]))
    } catch (e: any) {
      error.value = errorText(e)
      inspection.value = null
    }
    inspecting.value = false
  }

  function chooseNewTable() {
    tableName.value = nextFreeName(tableName.value, tables.value)
  }

  // pickDefaultMode chooses the wizard's pre-selected mode from what the
  // backend reports as usable: upsert (idempotent, never duplicates) — and
  // ONLY upsert. Every other case (the file has no id column, so upsert isn't
  // offered — exactly the shape of a re-imported people.csv) returns '', so
  // no radio is pre-checked and the user must choose explicitly. Append used
  // to be the second-choice default, but Append is only idempotent when the
  // table's key IS in the file (upsert's own precondition); without a key to
  // compare against, a re-import of the same file duplicates every row
  // silently — the append pre-flight's exact-duplicate check now catches and
  // warns about that, but a SILENT default that reproduces it every time is
  // the bug, not the warning. Replace is never auto-picked either, since it
  // drops the table's current rows.
  function pickDefaultMode(modes: ImportMode[]): ImportMode | '' {
    return modes.includes('upsert') ? 'upsert' : ''
  }

  // requestKey identifies the exact set of options a dry run (or Import)
  // would run with. It drives the debounced dry run below and, unchanged,
  // the #91 re-click guard after a successful import.
  const requestKey = computed(() => JSON.stringify({
    path: path.value,
    sheet: sheet.value,
    has_header: hasHeader.value,
    table: tableName.value,
    exists: tableExists.value, // a dry run's meaning changes the instant the table starts/stops existing
    mode: mode.value,
    key: key.value,
    types: typeOverrides.value,
  }))

  let dryRunTimer: ReturnType<typeof setTimeout> | null = null
  let dryRunSeq = 0

  async function runDryRun() {
    const seq = ++dryRunSeq
    dryRunning.value = true
    try {
      const s = await App.ImportFile(buildOptions(true))
      if (seq !== dryRunSeq) return // a newer dry run has already started; ignore this stale response
      summary.value = s
      // The wizard always pre-selects the safest available mode so the
      // default never duplicates rows AND never drops the table (Replace is
      // only ever chosen explicitly — see pickDefaultMode).
      if (s.exists && s.modes?.length && !s.modes.includes(mode.value as ImportMode)) {
        mode.value = pickDefaultMode(s.modes)
      }
    } catch (e: any) {
      if (seq !== dryRunSeq) return
      errorContext.value = 'preview'
      error.value = errorText(e)
      summary.value = null
    } finally {
      if (seq === dryRunSeq) dryRunning.value = false
    }
  }

  watch(requestKey, () => {
    summary.value = null
    if (dryRunTimer) clearTimeout(dryRunTimer)
    if (!path.value || !tableName.value) return
    // Still dry-runs with mode:"" when the table exists and none is chosen
    // yet — that response is what carries existing_rows/modes for the mode
    // group itself (Exists/ExistingRows/Modes are set before the backend's
    // own Blocked check). Only the DISPLAY of that response's Blocked
    // sentence and summary line are suppressed while modeNotChosen — see
    // hasProblems and the template below.
    dryRunTimer = setTimeout(runDryRun, 300)
  })

  // Whenever the table name starts or stops matching an existing table, the
  // previously selected mode is no longer meaningful — let the next dry run
  // pick a fresh default.
  watch(tableExists, () => { mode.value = '' })

  async function runImport() {
    if (!canImport.value) return
    importing.value = true
    error.value = ''
    errorContext.value = 'import'
    const key0 = requestKey.value
    try {
      const s = await App.ImportFile(buildOptions(false))
      summary.value = s
      if (s.ok) {
        successSummary.value = s
        importedKey.value = key0
        await loadTables()
      } else {
        error.value = s.blocked || "The import didn't finish. See the details below."
      }
    } catch (e: any) {
      error.value = errorText(e)
    }
    importing.value = false
  }

  function openTable() {
    router.push({ path: '/tables', query: { table: successSummary.value?.table ?? tableName.value } })
  }

  function importAnother() {
    resetAll()
  }

  onMounted(loadTables)

  return {
    path, sheet, hasHeader, tableName, mode, key, typeOverrides, inspection, summary, successSummary, importing, error, errorContext, tableExists, keyCandidates, showKeySelect, MODE_LABELS, MODE_DISABLED_REASON, MODE_ORDER, mismatchText, modeNotChosen, hasProblems, canImport, isKeyColumn, typeSelectDisabled, setColumnType, selectFile, inspect, chooseNewTable, runImport, openTable, importAnother,
  }
}
