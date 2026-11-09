/**
 * useConnectionForm — the connection editor's form (F10): the definition and
 * sign-in being edited, the fields the type registry generates, the draft's
 * fingerprint (does a saved sign-in still belong to this server?), validation,
 * and loading a connection (or a duplicate, or a blank) into the form. Test,
 * save and remove are in useConnectionEditor; the MotherDuck picker is in
 * useMotherDuckPicker.
 */
import { ref, reactive, computed, watch, nextTick } from 'vue'
import { useRoute } from 'vue-router'
import { App } from '../lib/bindings'
import { workspaceInfo } from './useWorkspace'
import { errorText } from '../lib/errors'
import { NAME_RE, CRED_ENV_SUFFIX, CRED_KEY, fieldVisible } from '../lib/connections'
import { presetFill, endpointProblem, followRegion, REGION_PLACEHOLDER, type FilledTemplate } from '../lib/storagePresets'
import { useStorageDetect, type DetectKey } from './useStorageDetect'
import { parseStoragePrefill, type StoragePrefill } from '../lib/storagePrefill'
import { takeDraft } from './useConnectionDraft'
import { connectionByName } from './useConnections'

// 'aws-sso' is set only by loading a connection that signs in with an AWS SSO
// profile (chosen with the CLI); the editor shows it read-only and keeps it.
type SignIn = 'keychain' | 'env' | 'prompt' | 'aws-sso'
const emptyCreds = (): CredentialInput => ({ username: '', keyId: '', password: '', token: '', secret: '' })

export function useConnectionForm() {
  const route = useRoute()

  const types = ref<ConnTypeSpec[]>([])
  const loading = ref(true)
  const loadError = ref('')
  const isNew = computed(() => route.path === '/connections/new')
  const editName = computed(() => (isNew.value ? '' : String(route.params.name ?? '')))
  const typeId = ref('')
  const spec = computed(() => types.value.find((t) => t.id === typeId.value) ?? null)

  // ---- form state ----
  const f = reactive({ name: '', host: '', port: '', database: '', path: '', region: '', scope: '', options: {} as Record<string, string> })
  const readOnly = ref(true)
  const connScope = ref<ConnScope>('local')
  const origScope = ref<ConnScope>('local')
  const method = ref<SignIn>('keychain')
  const envPrefix = ref('')
  const ssoProfile = ref('')
  const typed = reactive<CredentialInput>(emptyCreds())
  const status = ref<ConnCredentialStatus | null>(null)
  const detail = ref<ConnectionDetail | null>(null)
  const advancedOpen = ref(false)
  const initial = ref('')
  // the sign-in method as saved, to notice a switch away from the keychain
  const origMethod = ref<SignIn>('keychain')
  const hadKeychainSecret = ref(false)
  // what the last save / test said
  const saved = ref(false)
  const saveError = ref('')
  const result = ref<TestResultView | null>(null)
  // The form was filled from a link (?type=&prefix=): the person is told to check it.
  const prefilled = ref(false)
  // The lake being edited changed on disk while its storage connection was set up.
  const changedWhileAway = ref(false)

  const isMD = computed(() => typeId.value === 'motherduck')
  const remote = computed(() => !!spec.value?.capabilities.remote)
  // A required option (a DuckLake's data folder, R2's account id) is not an
  // advanced setting: it sits with the main fields, with its asterisk.
  // A field with a visibleWhen rule (an iceberg_catalog's rest-only URL) is shown,
  // validated and saved only while its rule holds for the options now set.
  const visible = (fd: ConnField) => fieldVisible(fd, spec.value?.fields ?? [], f.options)
  const mainFields = computed(() => (spec.value?.fields ?? []).filter((x) => (!x.option || x.required) && x.name !== 'port' && visible(x)))
  const advFields = computed(() => (spec.value?.fields ?? []).filter((x) => x.option && !x.required && visible(x)))
  // These sources trust their own metadata (it decides which files are read): any
  // scope may define one, and the form warns. Go says which types (and the
  // warning text); nothing here lists them.
  const metadataDriven = computed(() => !!spec.value?.metadataDriven)
  const metadataWarning = computed(() => spec.value?.warning ?? '')
  const insideWorkspace = computed(() => {
    if (!metadataDriven.value) return false
    const root = workspaceInfo.value?.root ?? ''
    const loc = [f.path, f.options['data_path'] ?? ''].map((x) => x.trim()).filter(Boolean)
    return loc.some((x) => {
      if (x.includes('://')) return false
      if (x.startsWith('/') || x.startsWith('~') || /^[A-Za-z]:[\\/]/.test(x)) return !!root && x.startsWith(root)
      return true // relative paths resolve inside the workspace folder
    })
  })
  // What a qualified table name looks like for this type.
  const exampleTail = computed(() => {
    switch (typeId.value) {
      case 'postgres': return '.public.orders'
      case 'mysql': return `.${f.database.trim() || 'database'}.orders`
      case 'duckdb': case 'sqlite': case 'ducklake': case 'motherduck': return '.main.orders'
      default: return ''
    }
  })
  const portField = computed(() => (spec.value?.fields ?? []).find((x) => x.name === 'port'))
  // Only the credentials the definition signs in with: a bearer catalog a token, an OAuth2 one a client id and secret, an s3_tables or auth-none one nothing.
  const credFields = computed(() => (spec.value?.credentials ?? []).filter(visible))
  const readableCreds = computed(() => credFields.value.filter((c) => !c.secret))
  const secretCreds = computed(() => credFields.value.filter((c) => c.secret))
  const primarySecret = computed(() => secretCreds.value[0]?.name ?? '')
  const needsSignIn = computed(() => credFields.value.length > 0)
  // What was typed for the credentials this definition signs in with: a token typed
  // before switching to OAuth2 is not sent, so it is never stored.
  function visibleCreds(): CredentialInput {
    const out = emptyCreds()
    for (const c of credFields.value) {
      const k = CRED_KEY[c.name]
      if (k) out[k] = typed[k]
    }
    return out
  }

  const methodString = computed(() => (method.value === 'env' ? `env:${envPrefix.value.trim() || 'SLAW'}` : method.value === 'aws-sso' ? `aws-sso:${ssoProfile.value}` : method.value))
  const envVars = computed(() => {
    const p = envPrefix.value.trim() || 'SLAW'
    const known = new Map((status.value?.env ?? []).map((e) => [e.name, e.set]))
    return credFields.value.map((c) => {
      const n = `${p}_${CRED_ENV_SUFFIX[c.name] ?? c.name.toUpperCase()}`
      return { name: n, set: known.has(n) ? known.get(n)! : null }
    })
  })

  function val(field: ConnField): string {
    if (field.option) return f.options[field.name] ?? ''
    if (field.name === 'scope') return f.scope
    return (f as any)[field.name] ?? ''
  }
  function setVal(field: ConnField, v: string) {
    if (field.option) f.options[field.name] = v
    else if (field.name === 'scope') f.scope = v
    else (f as any)[field.name] = v
  }

  function buildDef(): ConnectionDef {
    const d: ConnectionDef = { name: f.name.trim(), type: typeId.value }
    const set = (k: keyof ConnectionDef, v: string) => { if (v.trim() !== '') (d as any)[k] = v.trim() }
    for (const fd of spec.value?.fields ?? []) {
      if (fd.option || !visible(fd)) continue
      const v = val(fd)
      if (fd.name === 'port') { if (v.trim() !== '') d.port = parseInt(v, 10) || 0 }
      else set(fd.name as keyof ConnectionDef, v)
    }
    const opts: Record<string, string> = {}
    for (const fd of (spec.value?.fields ?? []).filter((x) => x.option && visible(x))) { // required options too
      const v = (f.options[fd.name] ?? '').trim()
      if (v) opts[fd.name] = v
    }
    if (Object.keys(opts).length) d.options = opts
    if (!isMD.value && !readOnly.value) d.read_only = false
    return d
  }

  // What a saved sign-in is bound to is Go's call (workspace.Fingerprint: type,
  // host, port, database, scope, path, region and every option). The loaded
  // definition's fingerprint arrives with the detail; the draft's comes from the
  // ConnectionFingerprint binding, which sees no credential. When they differ,
  // the saved secret will not be used for this server.
  const draftFingerprint = ref('')
  let fpSeq = 0
  async function refreshDraftFingerprint(): Promise<void> {
    const seq = ++fpSeq
    if (isNew.value || !detail.value || !typeId.value) { draftFingerprint.value = ''; return }
    try {
      const fp = await App.ConnectionFingerprint(buildDef())
      if (seq === fpSeq) draftFingerprint.value = fp
    } catch {
      if (seq === fpSeq) draftFingerprint.value = ''
    }
  }
  watch(() => [isNew.value, detail.value?.fingerprint, JSON.stringify(buildDef())], () => { void refreshDraftFingerprint() }, { immediate: true })
  const fingerprintChanged = computed(() => !isNew.value && !!detail.value && draftFingerprint.value !== '' && draftFingerprint.value !== detail.value.fingerprint)

  // bindingChanged: the saved sign-in is not bound to this definition any more,
  // because a pull changed it (the binding says so) or because it is edited here.
  // An environment sign-in would follow the new server, so Save must ask.
  const bindingChanged = computed(() => !isNew.value && !!detail.value && (
    !detail.value.credential.fingerprintMatches || !!detail.value.info.serverChanged || fingerprintChanged.value))

  const snapshot = () => JSON.stringify([typeId.value, buildDef(), connScope.value, methodString.value, typed.username, typed.keyId, typed.password, typed.token, typed.secret])
  const dirty = computed(() => !loading.value && !!typeId.value && !saved.value && snapshot() !== initial.value)

  // The fields a saved sign-in is bound to, in words, from the type's own list.
  const fingerprintHelp = computed(() => {
    const labels: Record<string, string> = { host: 'host', port: 'port', database: 'database', scope: 'scope', region: 'region' }
    const have = new Set((spec.value?.fields ?? []).filter((x) => !x.option).map((x) => x.name))
    const names = (spec.value?.fingerprintFields ?? []).filter((f) => labels[f] && have.has(f)).map((f) => labels[f])
    if (!names.length) return ''
    const list = names.length === 1 ? names[0] : `${names.slice(0, -1).join(', ')} or ${names[names.length - 1]}`
    return `If the ${list} change${names.length === 1 ? 's' : ''}, everyone is asked to sign in again to confirm.`
  })

  const secretIsSet = (name: string) => !isNew.value && !fingerprintChanged.value && name === primarySecret.value && !!status.value?.secretSet && status.value.fingerprintMatches && method.value !== 'env' && (status.value.method === methodString.value || status.value.method === method.value)

  // A saved secret that will not be used because the server changed says so where
  // it would have been shown.
  const serverChangedHelp = computed(() => (!isNew.value && (fingerprintChanged.value || (!!status.value?.secretSet && status.value.fingerprintMatches === false)) && method.value === 'keychain'
    ? "The server changed, so your saved password won't be used. Enter it again." : ''))

  // ---- S3-compatible store presets, and the storage line of lakes and tables ----
  const presets = computed(() => spec.value?.presets ?? [])
  // The template the last preset filled in, so Region keeps the endpoint right.
  let filled: FilledTemplate | null = null
  const defaultRegion = () => (spec.value?.fields ?? []).find((x) => x.name === 'region')?.default ?? ''
  function applyPreset(p: ConnPreset) {
    if (p.id !== 'other' && filled?.hintRegion && f.region === filled.hintRegion && !Object.values(p.options).some((v) => v.includes(REGION_PLACEHOLDER))) {
      f.region = defaultRegion() // a region only the previous preset's hint set
    }
    const before = f.region
    const r = presetFill(p, f.options, f.region)
    f.options = r.options
    f.region = r.region
    advancedOpen.value = true
    if (p.id === 'other') return
    const template = Object.values(p.options).find((v) => v.includes(REGION_PLACEHOLDER))
    const endpoint = r.options['endpoint'] ?? ''
    filled = template && p.options['endpoint'] === template ? { template, endpoint, hintRegion: r.region !== before ? r.region : '' } : null
  }
  watch(() => f.region, (region) => {
    const next = followRegion(filled, f.options['endpoint'] ?? '', region)
    if (next === null || !filled) return
    f.options['endpoint'] = next
    filled = { ...filled, endpoint: next }
  })
  const storage = useStorageDetect(typeId, f)
  // Whether a location field has a storage line at all (its slot stays mounted).
  const storageSlot = (fd: ConnField): boolean => ['ducklake', 'iceberg', 'delta'].includes(typeId.value) && (fd.name === 'path' || fd.name === 'data_path')
  // The storage line under a location field: its path, and a lake's data path.
  const storageLine = (fd: ConnField): StorageNeedView | null =>
    storageSlot(fd) ? storage.needFor(fd.name as DetectKey) : null
  const storageFailed = computed(() => storage.failed.value)

  // ---- shadowing ----
  const privateCopy = computed(() => !isNew.value && detail.value && detail.value.scope === 'local' && detail.value.shadows.length > 0 && !detail.value.shadowedBy)
  const shadowedBy = computed(() => detail.value?.shadowedBy ?? '')

  const failedTest = computed(() => !!result.value && !result.value.ok)
  const title = computed(() => (isNew.value ? 'New connection' : `Edit ${f.name}`))

  // ---- loading ----
  function applyDef(d: ConnectionDef) {
    f.name = d.name
    f.host = d.host ?? ''
    f.port = d.port ? String(d.port) : ''
    f.database = d.database ?? ''
    f.path = d.path ?? ''
    f.region = d.region ?? ''
    f.scope = d.scope ?? ''
    f.options = { ...(d.options ?? {}) }
    readOnly.value = d.read_only === undefined || d.read_only === null ? d.type !== 'motherduck' : !!d.read_only
    advancedOpen.value = Object.keys(f.options).length > 0
  }

  function applyMethod(m: string) {
    if (m.startsWith('env:')) { method.value = 'env'; envPrefix.value = m.slice(4) }
    else if (m.startsWith('aws-sso:')) { method.value = 'aws-sso'; ssoProfile.value = m.slice(8) }
    else if (m === 'prompt') method.value = 'prompt'
    else method.value = 'keychain'
  }

  function chooseType(id: string, from?: ConnectionDef) {
    typeId.value = id
    const t = types.value.find((x) => x.id === id)
    for (const fd of t?.fields ?? []) {
      // An option's default is what leaving it blank means (the select's
      // "Default" entry), so it is not written into the YAML as noise.
      if (fd.default && !from && !fd.option) setVal(fd, fd.default)
    }
    // Settings the type cannot do without (a DuckLake's data folder) or that
    // most people must look at (Azure's sign-in, flagged prominent) live under
    // Advanced: show them open on a new connection.
    if (!from && (t?.fields ?? []).some((fd) => fd.option && (fd.required || fd.prominent))) advancedOpen.value = true
    if (id === 'motherduck') readOnly.value = false
    if (['ducklake', 'iceberg', 'delta'].includes(id) && connScope.value === 'shared') connScope.value = 'local'
    envPrefix.value = id === 'motherduck' ? 'MOTHERDUCK' : ''
    initial.value = snapshot()
    void nextTick(() => document.getElementById('f-name')?.focus())
  }

  // applyStoragePrefill fills a new storage connection from a validated link
  // (lib/storagePrefill). It fills visible fields only; the sign-in stays the
  // keychain default and nothing is saved.
  function applyStoragePrefill(pre: StoragePrefill) {
    chooseType(pre.type)
    // A name a connection already has is left empty: saving it would shadow that one.
    f.name = connectionByName(pre.name) ? '' : pre.name
    f.scope = pre.prefix
    connScope.value = pre.scope
    origScope.value = pre.scope
    prefilled.value = true
    initial.value = snapshot()
  }

  // resumeDraft puts the stashed lake form back after its storage connection
  // was saved (or the set-up was cancelled), over what the route loaded.
  let focusAfterLoad = ''
  function resumeDraft() {
    const d = takeDraft(isNew.value ? '' : editName.value)
    if (!d) return
    if (isNew.value) { typeId.value = d.def.type; initial.value = '' }
    else if (d.fingerprint && detail.value && detail.value.fingerprint !== d.fingerprint) changedWhileAway.value = true
    applyDef(d.def)
    advancedOpen.value = d.advancedOpen
    connScope.value = d.connScope
    // Back where the person was: the field whose storage line they followed.
    focusAfterLoad = d.focus
  }

  // loadForm fills the form from the route: a blank form, a duplicate (?from=),
  // or the connection being edited.
  async function loadForm() {
    loading.value = true
    loadError.value = ''
    try {
      types.value = await App.ListConnectionTypes()
      saved.value = false
      result.value = null
      saveError.value = ''
      prefilled.value = false
      changedWhileAway.value = false
      Object.assign(typed, emptyCreds())
      if (isNew.value) {
        const from = typeof route.query.from === 'string' ? route.query.from : ''
        if (from) {
          const d = await App.GetConnection(from)
          typeId.value = d.def.type
          applyDef({ ...d.def, name: `${d.def.name}_copy` })
          // A duplicate never inherits a sign-in method: it starts at the keychain,
          // needing credentials, so nothing stored for the original is used for it.
          method.value = 'keychain'
          envPrefix.value = ''
          origMethod.value = 'keychain'
          hadKeychainSecret.value = false
          connScope.value = 'local'
          origScope.value = 'local'
          status.value = null
          detail.value = null
          initial.value = snapshot()
        } else {
          typeId.value = ''
          applyDef({ name: '', type: '' })
          connScope.value = 'local'
          origScope.value = 'local'
          method.value = 'keychain'
          status.value = null
          detail.value = null
          initial.value = ''
          const pre = parseStoragePrefill(route.query)
          if (pre && types.value.some((t) => t.id === pre.type)) applyStoragePrefill(pre)
        }
      } else {
        const scope = typeof route.query.scope === 'string' ? (route.query.scope as ConnScope) : ('' as const)
        const d = await App.GetConnectionInScope({ name: editName.value, scope })
        detail.value = d
        typeId.value = d.def.type
        applyDef(d.def)
        applyMethod(d.signIn)
        origMethod.value = method.value
        hadKeychainSecret.value = method.value === 'keychain' && d.credential.secretSet && !d.credential.sessionOnly
        connScope.value = d.scope
        origScope.value = d.scope
        status.value = d.credential
        typed.username = d.credential.username ?? ''
        typed.keyId = d.credential.keyId ?? ''
        initial.value = snapshot()
      }
      if (route.query.resume === '1') resumeDraft()
    } catch (e: unknown) {
      loadError.value = errorText(e)
    }
    loading.value = false
    if (focusAfterLoad) {
      const id = focusAfterLoad
      focusAfterLoad = ''
      await nextTick()
      document.getElementById(id)?.focus()
    }
  }

  // validate returns what is wrong with the form, or '' when it can be saved. A
  // missing required field is marked (invalidField) until it is filled, and its
  // message in saveError goes away with it.
  const invalidField = ref('')
  let lastProblem = ''
  const missing = (fd: ConnField): string => { invalidField.value = fd.name; lastProblem = `${fd.label} is needed.`; return lastProblem }
  watch(() => { const fd = spec.value?.fields.find((x) => x.name === invalidField.value); return fd ? val(fd).trim() : null }, (v) => {
    if (!v) return
    invalidField.value = ''
    if (saveError.value === lastProblem) saveError.value = ''
  })
  function validate(): string {
    invalidField.value = ''
    if (!spec.value) return 'Choose a connection type first.'
    if (isNew.value && !NAME_RE.test(f.name.trim())) return 'Name it with lowercase letters, numbers and underscores, like prod_pg.'
    for (const fd of spec.value.fields.filter(visible)) {
      if (fd.required && !fd.option && val(fd).trim() === '') return missing(fd)
    }
    for (const fd of (spec.value.fields ?? []).filter((x) => x.option && visible(x))) {
      if (fd.required && !(f.options[fd.name] ?? '').trim()) return missing(fd)
    }
    const ep = endpointProblem((f.options['endpoint'] ?? '').trim())
    if (ep) return ep
    return ''
  }

  return {
    types, loading, loadError, isNew, editName, typeId, spec, f, readOnly, connScope, origScope, method, envPrefix, ssoProfile,
    typed, status, detail, advancedOpen, origMethod, hadKeychainSecret, saved, saveError, result, isMD, remote,
    mainFields, advFields, metadataDriven, metadataWarning, insideWorkspace, exampleTail, portField, credFields, readableCreds,
    secretCreds, needsSignIn, visibleCreds, invalidField, visible, methodString, envVars, val, setVal, buildDef, refreshDraftFingerprint,
    fingerprintChanged, bindingChanged, dirty, fingerprintHelp, secretIsSet, serverChangedHelp, privateCopy,
    shadowedBy, failedTest, title, chooseType, loadForm, validate, presets, applyPreset, storageLine, storageSlot, storageFailed, prefilled, changedWhileAway,
  }
}

export type ConnectionForm = ReturnType<typeof useConnectionForm>
